package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestInternalMonitor_LifecycleProcessIntegration tests the four-step lifecycle
// reproduction with real tmux sessions and pgrep process inspection:
// 1. Start tmux session and internal-monitor -> pgrep asserts monitor is running
// 2. Kill session via raw tmux (tmux kill-session -t <session>:)
// 3. Assert pgrep finds no internal-monitor process within bounded time
// 4. Relaunch session and monitor -> assert exactly 1 monitor running
// 5. Kill again -> assert 0 monitors remaining
func TestInternalMonitor_LifecycleProcessIntegration(t *testing.T) {
	if err := tmux.EnsureInstalled(); err != nil {
		t.Skip("tmux not installed:", err)
	}

	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	_ = os.MkdirAll(binDir, 0755)
	binPath := filepath.Join(binDir, "ntm")

	dataDir := filepath.Join(tempDir, "data")
	_ = os.MkdirAll(dataDir, 0755)
	t.Setenv("XDG_DATA_HOME", dataDir)

	// Build ntm binary for testing real process lifecycle
	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/ntm")
	buildCmd.Dir = "../.."
	buildCmd.Env = append(os.Environ(), "GOPATH=/home/gabriel/go", "GOMODCACHE=/home/gabriel/go/pkg/mod", "GOCACHE=/home/gabriel/.cache/go-build")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build ntm binary: %v\noutput: %s", err, string(out))
	}

	session := fmt.Sprintf("ntm_bd_odr_%d", time.Now().UnixNano()%1000000)

	// Clean up any test session or strays on exit
	t.Cleanup(func() {
		_ = tmux.KillSession(session)
		_ = exec.Command("pkill", "-f", resilience.MonitorProcessPatternForExecutable(binPath, session)).Run()
		_ = resilience.DeleteManifest(session)
	})

	t.Setenv("NTM_DISABLE_INTERNAL_MONITOR", "")

	// 1. Create a tmux session with an agent pane
	if err := tmux.CreateSession(session, tempDir); err != nil {
		t.Fatalf("failed to create test tmux session: %v", err)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("failed to get panes for test session: %v", err)
	}

	// Create and save manifest for the session
	manifest := &resilience.SpawnManifest{
		Session:     session,
		ProjectDir:  tempDir,
		AutoRestart: false,
		Agents: []resilience.AgentConfig{
			{
				PaneID:    panes[0].ID,
				PaneIndex: panes[0].Index,
				Type:      "claude",
				Command:   "sleep 100",
			},
		},
	}
	if err := resilience.SaveManifest(manifest); err != nil {
		t.Fatalf("failed to save manifest: %v", err)
	}

	// Start internal-monitor process
	monitorCmd := exec.Command(binPath, "internal-monitor", session)
	monitorLogPath := filepath.Join(tempDir, session+"-monitor.log")
	logFile, err := os.OpenFile(monitorLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("failed to create log file: %v", err)
	}
	monitorCmd.Stdout = logFile
	monitorCmd.Stderr = logFile
	if err := monitorCmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("failed to start internal-monitor: %v", err)
	}
	_ = logFile.Close()

	pattern := resilience.MonitorProcessPatternForExecutable(binPath, session)

	// Positive assertion: assert monitor is running (pgrep finds at least 1 pid)
	getPids := func() []string {
		out, err := exec.Command("pgrep", "-f", pattern).Output()
		if err != nil {
			return nil
		}
		var pids []string
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				pids = append(pids, line)
			}
		}
		return pids
	}

	time.Sleep(200 * time.Millisecond)
	pids := getPids()
	if len(pids) != 1 {
		t.Fatalf("STEP 1 FAILED: expected exactly 1 monitor running after start, got %d (pids: %v)", len(pids), pids)
	}

	// 2. Kill the session via raw tmux command (tmux kill-session -t <session>:)
	if err := exec.Command("tmux", "kill-session", "-t", session+":").Run(); err != nil {
		t.Fatalf("failed to kill session via tmux: %v", err)
	}

	// 3. Assert pgrep finds 0 monitors running within bounded time (<= 5 seconds)
	deadline := time.Now().Add(5 * time.Second)
	var remaining []string
	for time.Now().Before(deadline) {
		remaining = getPids()
		if len(remaining) == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(remaining) != 0 {
		t.Fatalf("STEP 3 FAILED: expected 0 monitors after kill-session, but %d still alive: %v", len(remaining), remaining)
	}

	// 4. Relaunch session and monitor under same session name
	if err := tmux.CreateSession(session, tempDir); err != nil {
		t.Fatalf("failed to recreate test tmux session: %v", err)
	}
	newPanes, err := tmux.GetPanes(session)
	if err != nil || len(newPanes) == 0 {
		t.Fatalf("failed to get panes for recreated session: %v", err)
	}

	manifest.Agents = []resilience.AgentConfig{
		{
			PaneID:    newPanes[0].ID,
			PaneIndex: newPanes[0].Index,
			Type:      "claude",
			Command:   "sleep 100",
		},
	}
	if err := resilience.SaveManifest(manifest); err != nil {
		t.Fatalf("failed to save recreated manifest: %v", err)
	}

	monitorCmd2 := exec.Command(binPath, "internal-monitor", session)
	logFile2, err := os.OpenFile(monitorLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("failed to open log file: %v", err)
	}
	monitorCmd2.Stdout = logFile2
	monitorCmd2.Stderr = logFile2
	if err := monitorCmd2.Start(); err != nil {
		_ = logFile2.Close()
		t.Fatalf("failed to restart internal-monitor: %v", err)
	}
	_ = logFile2.Close()

	time.Sleep(200 * time.Millisecond)
	pids2 := getPids()
	if len(pids2) != 1 {
		t.Fatalf("STEP 4 FAILED: expected exactly 1 monitor running after relaunch, got %d (pids: %v)", len(pids2), pids2)
	}

	// 5. Kill session again
	if err := exec.Command("tmux", "kill-session", "-t", session+":").Run(); err != nil {
		t.Fatalf("failed to kill recreated session: %v", err)
	}

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		remaining = getPids()
		if len(remaining) == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(remaining) != 0 {
		t.Fatalf("STEP 5 FAILED: expected 0 monitors after second kill-session, but %d still alive: %v", len(remaining), remaining)
	}

	// Check log: ensure NO false alarms like "Agent %... crashed: Pane no longer exists"
	logContent, _ := os.ReadFile(monitorLogPath)
	if strings.Contains(string(logContent), "crashed: Pane no longer exists") {
		t.Fatalf("monitor log contains false pane-crash alarm:\n%s", string(logContent))
	}
}

// TestInternalMonitor_VanishedPaneSetExitsCleanly tests that if all monitored panes
// vanish while the session still exists, the monitor exits on its own and does NOT
// spam crash alarms.
func TestInternalMonitor_VanishedPaneSetExitsCleanly(t *testing.T) {
	if err := tmux.EnsureInstalled(); err != nil {
		t.Skip("tmux not installed:", err)
	}

	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	_ = os.MkdirAll(binDir, 0755)
	binPath := filepath.Join(binDir, "ntm")

	dataDir := filepath.Join(tempDir, "data")
	_ = os.MkdirAll(dataDir, 0755)
	t.Setenv("XDG_DATA_HOME", dataDir)

	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/ntm")
	buildCmd.Dir = "../.."
	buildCmd.Env = append(os.Environ(), "GOPATH=/home/gabriel/go", "GOMODCACHE=/home/gabriel/go/pkg/mod", "GOCACHE=/home/gabriel/.cache/go-build")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build ntm binary: %v\noutput: %s", err, string(out))
	}

	session := fmt.Sprintf("ntm_bd_odr_vps_%d", time.Now().UnixNano()%1000000)

	t.Cleanup(func() {
		_ = tmux.KillSession(session)
		_ = exec.Command("pkill", "-f", resilience.MonitorProcessPatternForExecutable(binPath, session)).Run()
		_ = resilience.DeleteManifest(session)
	})

	t.Setenv("NTM_DISABLE_INTERNAL_MONITOR", "")

	if err := tmux.CreateSession(session, tempDir); err != nil {
		t.Fatalf("failed to create test tmux session: %v", err)
	}

	// Register a pane ID that does NOT exist in this session (%999999)
	manifest := &resilience.SpawnManifest{
		Session:     session,
		ProjectDir:  tempDir,
		AutoRestart: false,
		Agents: []resilience.AgentConfig{
			{
				PaneID:    "%999999",
				PaneIndex: 1,
				Type:      "claude",
				Command:   "sleep 100",
			},
		},
	}
	if err := resilience.SaveManifest(manifest); err != nil {
		t.Fatalf("failed to save manifest: %v", err)
	}

	monitorCmd := exec.Command(binPath, "internal-monitor", session)
	monitorLogPath := filepath.Join(tempDir, session+"-monitor.log")
	logFile, err := os.OpenFile(monitorLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("failed to create log file: %v", err)
	}
	monitorCmd.Stdout = logFile
	monitorCmd.Stderr = logFile
	if err := monitorCmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("failed to start internal-monitor: %v", err)
	}
	_ = logFile.Close()

	pattern := resilience.MonitorProcessPatternForExecutable(binPath, session)

	getPids := func() []string {
		out, err := exec.Command("pgrep", "-f", pattern).Output()
		if err != nil {
			return nil
		}
		var pids []string
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				pids = append(pids, line)
			}
		}
		return pids
	}

	// Initially monitor is started
	time.Sleep(200 * time.Millisecond)
	initialPids := getPids()
	if len(initialPids) != 1 {
		t.Fatalf("expected 1 monitor running initially, got %d", len(initialPids))
	}

	// Wait up to 6 seconds for monitor to detect vanished pane set and exit
	deadline := time.Now().Add(6 * time.Second)
	var remaining []string
	for time.Now().Before(deadline) {
		remaining = getPids()
		if len(remaining) == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected monitor to exit when pane set vanished, but %d still alive: %v", len(remaining), remaining)
	}

	// Check log: ensure NO false alarms
	logContent, _ := os.ReadFile(monitorLogPath)
	if strings.Contains(string(logContent), "crashed: Pane no longer exists") {
		t.Fatalf("monitor log contains false pane-crash alarm on vanished pane set:\n%s", string(logContent))
	}
}

// TestInternalMonitor_SpawnReplacesExistingMonitor tests Criterion 2:
// Starting a monitor for a session that already has a running monitor stops the
// existing one first and leaves exactly 1 monitor running.
func TestInternalMonitor_SpawnReplacesExistingMonitor(t *testing.T) {
	if err := tmux.EnsureInstalled(); err != nil {
		t.Skip("tmux not installed:", err)
	}

	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	_ = os.MkdirAll(binDir, 0755)
	binPath := filepath.Join(binDir, "ntm")

	dataDir := filepath.Join(tempDir, "data")
	_ = os.MkdirAll(dataDir, 0755)
	t.Setenv("XDG_DATA_HOME", dataDir)

	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/ntm")
	buildCmd.Dir = "../.."
	buildCmd.Env = append(os.Environ(), "GOPATH=/home/gabriel/go", "GOMODCACHE=/home/gabriel/go/pkg/mod", "GOCACHE=/home/gabriel/.cache/go-build")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build ntm binary: %v\noutput: %s", err, string(out))
	}

	session := fmt.Sprintf("ntm_bd_odr_repl_%d", time.Now().UnixNano()%1000000)

	t.Cleanup(func() {
		_ = tmux.KillSession(session)
		_ = exec.Command("pkill", "-f", resilience.MonitorProcessPatternForExecutable(binPath, session)).Run()
		_ = resilience.DeleteManifest(session)
	})

	t.Setenv("NTM_DISABLE_INTERNAL_MONITOR", "")

	if err := tmux.CreateSession(session, tempDir); err != nil {
		t.Fatalf("failed to create test tmux session: %v", err)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("failed to get panes: %v", err)
	}

	manifest := &resilience.SpawnManifest{
		Session:     session,
		ProjectDir:  tempDir,
		AutoRestart: false,
		Agents: []resilience.AgentConfig{
			{
				PaneID:    panes[0].ID,
				PaneIndex: panes[0].Index,
				Type:      "claude",
				Command:   "sleep 100",
			},
		},
	}
	if err := resilience.SaveManifest(manifest); err != nil {
		t.Fatalf("failed to save manifest: %v", err)
	}

	pattern := resilience.MonitorProcessPatternForExecutable(binPath, session)
	getPids := func() []string {
		out, err := exec.Command("pgrep", "-f", pattern).Output()
		if err != nil {
			return nil
		}
		var pids []string
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				pids = append(pids, line)
			}
		}
		return pids
	}

	// 1. Start elder monitor
	monitorCmd1 := exec.Command(binPath, "internal-monitor", session)
	if err := monitorCmd1.Start(); err != nil {
		t.Fatalf("failed to start monitor 1: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	pids1 := getPids()
	if len(pids1) != 1 {
		t.Fatalf("expected 1 monitor initially, got %d (pids: %v)", len(pids1), pids1)
	}
	elderPID := pids1[0]

	// 2. Launch a second monitor via the CLI replacement path (StartSessionMonitor logic)
	// Kill existing using KillExistingMonitorProcess or StartSessionMonitor
	if err := resilience.KillExistingMonitorProcess(session); err != nil {
		t.Fatalf("KillExistingMonitorProcess failed: %v", err)
	}

	// Start newer monitor
	monitorCmd2 := exec.Command(binPath, "internal-monitor", session)
	if err := monitorCmd2.Start(); err != nil {
		t.Fatalf("failed to start monitor 2: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	pids2 := getPids()
	if len(pids2) != 1 {
		t.Fatalf("expected exactly 1 monitor after replacement, got %d (pids: %v)", len(pids2), pids2)
	}
	if pids2[0] == elderPID {
		t.Fatalf("expected elder monitor %s to be replaced, but it is still running", elderPID)
	}
}
