package resilience

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestStartSessionMonitor_DisabledUnderGoTest pins the fork-bomb guard: under
// `go test` the shared path must refuse with ErrInternalMonitorDisabled and
// write nothing.
func TestStartSessionMonitor_DisabledUnderGoTest(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)

	result, err := StartSessionMonitor(t.Context(), SpawnMonitorRequest{
		Session:    "guardproj",
		ProjectDir: "/tmp/guardproj",
		Agents:     []AgentConfig{{PaneID: "0.1", PaneIndex: 1, Type: "claude"}},
	})
	if !errors.Is(err, ErrInternalMonitorDisabled) {
		t.Fatalf("err = %v, want ErrInternalMonitorDisabled", err)
	}
	if result != nil {
		t.Fatalf("result = %+v, want nil when disabled", result)
	}
	if _, statErr := os.Stat(filepath.Join(ManifestDir(), "guardproj.json")); !os.IsNotExist(statErr) {
		t.Fatalf("manifest must not be written when the monitor is disabled (stat err = %v)", statErr)
	}
}

// TestBuildSpawnManifest pins single-definition manifest construction: field
// mapping, agent passthrough, and the restart-unsupported (grok) filter.
func TestBuildSpawnManifest(t *testing.T) {
	req := SpawnMonitorRequest{
		Session:     "proj--lane",
		ProjectDir:  "/srv/projects/proj",
		AutoRestart: true,
		Agents: []AgentConfig{
			{PaneID: "0.1", PaneIndex: 1, Type: "claude", Model: "opus", Command: "claude --model opus"},
			{PaneID: "0.2", PaneIndex: 2, Type: "grok", Model: "", Command: "grok"},
			{PaneID: "0.3", PaneIndex: 3, Type: "codex", Model: "", Command: "codex"},
		},
	}
	m := buildSpawnManifest(req)
	if m.Session != "proj--lane" || m.ProjectDir != "/srv/projects/proj" || !m.AutoRestart {
		t.Fatalf("manifest header mismatch: %+v", m)
	}
	if len(m.Agents) != 2 {
		t.Fatalf("agents = %d, want 2 (grok filtered): %+v", len(m.Agents), m.Agents)
	}
	if m.Agents[0].PaneID != "0.1" || m.Agents[0].Type != "claude" || m.Agents[0].Command != "claude --model opus" {
		t.Fatalf("agent 0 mismatch: %+v", m.Agents[0])
	}
	if m.Agents[1].PaneID != "0.3" || m.Agents[1].Type != "codex" {
		t.Fatalf("agent 1 mismatch: %+v", m.Agents[1])
	}

	// Round-trip through the persisted form the monitor loads.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	loaded, err := LoadManifest("proj--lane")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.Session != m.Session || len(loaded.Agents) != len(m.Agents) || loaded.Agents[1].PaneID != "0.3" {
		t.Fatalf("persisted manifest mismatch: %+v", loaded)
	}
}

// TestMonitorProcessPattern_POSIXCompliance tests that the pattern returned by
// MonitorProcessPatternForExecutable is valid POSIX ERE that works with pgrep and pkill.
func TestMonitorProcessPattern_POSIXCompliance(t *testing.T) {
	pattern := MonitorProcessPatternForExecutable("/usr/bin/ntm", "bd-odr-sess")

	// Must not contain non-capturing groups `(?:` which POSIX ERE rejects
	if strings.Contains(pattern, "(?:") {
		t.Fatalf("pattern contains non-capturing group (?: which breaks POSIX pgrep/pkill: %s", pattern)
	}

	// Test with grep -E (standard POSIX ERE engine)
	testCases := []struct {
		input string
		match bool
	}{
		{"ntm internal-monitor bd-odr-sess", true},
		{"/usr/bin/ntm internal-monitor bd-odr-sess", true},
		{"/usr/bin/ntm internal-monitor bd-odr-sess extra", true},
		{"/usr/bin/ntm internal-monitor bd-odr-sess2", false},
		{"/usr/bin/ntm-dev internal-monitor bd-odr-sess", false},
		{"other ntm internal-monitor bd-odr-sess", true},
	}

	for _, tc := range testCases {
		cmd := exec.Command("grep", "-E", pattern)
		cmd.Stdin = strings.NewReader(tc.input)
		err := cmd.Run()
		matched := (err == nil)
		if matched != tc.match {
			t.Errorf("grep -E %q against %q: got matched=%v, want %v", pattern, tc.input, matched, tc.match)
		}
	}
}

// TestFindMonitorPIDs_NoMatchReturnsNil tests that a non-running session returns empty PIDs without error.
func TestFindMonitorPIDs_NoMatchReturnsNil(t *testing.T) {
	pids, err := FindMonitorPIDs("nonexistent-test-session-xyz-12345")
	if err != nil {
		t.Fatalf("FindMonitorPIDs error = %v, want nil", err)
	}
	if len(pids) != 0 {
		t.Fatalf("FindMonitorPIDs got %v, want empty", pids)
	}
}

// TestKillExistingMonitorProcess_SafeWhenNoneRunning tests that KillExistingMonitorProcess
// exits cleanly with no error when no monitor process is running.
func TestKillExistingMonitorProcess_SafeWhenNoneRunning(t *testing.T) {
	if err := KillExistingMonitorProcess("nonexistent-test-session-xyz-12345"); err != nil {
		t.Fatalf("KillExistingMonitorProcess error = %v, want nil", err)
	}
}
