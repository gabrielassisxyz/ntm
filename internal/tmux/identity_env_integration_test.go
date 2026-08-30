package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/tests/testutil/tmuxenv"
)

// startBareTmuxServer starts a private tmux server on its own socket with a
// deliberately minimal environment that carries neither GIT_IDENTITY_ENABLED
// nor AGENT_NAME — the "server predates this ntm invocation" case bd-fug
// exists for. It never touches TMUX_TMPDIR or the shared server TestMain
// owns. Returns the socket path; the caller is responsible for killing it.
func startBareTmuxServer(t *testing.T) string {
	t.Helper()
	binary := findInstalledTmuxBinaryPath()
	if binary == "" {
		t.Skip("tmux binary not found")
	}
	root, err := tmuxenv.CreateShortTmuxTempDir()
	if err != nil {
		t.Fatalf("create isolated tmux root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socketPath := tmuxenv.DefaultSocketPath(root)
	// tmux does not create the socket's parent directory itself when an
	// explicit -S names one that does not exist yet (unlike its own
	// TMUX_TMPDIR-only resolution, which does) — confirmed empirically
	// against tmux 3.7b: start-server reports "error creating <path>" and
	// exits 0 with no server listening. Pre-create it so -S behaves the
	// same way TMUX_TMPDIR-implicit resolution would.
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatalf("create tmux socket directory: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	// "; set-option -s exit-empty off" matches this package's own TestMain:
	// without it, a freshly started server with zero sessions can exit on
	// its own before the next command reaches it.
	cmd := exec.CommandContext(ctx, binary, "-S", socketPath, "start-server", ";", "set-option", "-s", "exit-empty", "off")
	// Deliberately bare: no GIT_IDENTITY_ENABLED, no AGENT_NAME, not even
	// this test process's own copies of them (set by other tests in this
	// package via t.Setenv would otherwise leak in and hide the bug this
	// guards against).
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("start bare tmux server: %v: %s", err, strings.TrimSpace(string(output)))
	}
	t.Cleanup(func() {
		killCtx, killCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer killCancel()
		killCmd := exec.CommandContext(killCtx, binary, "-S", socketPath, "kill-server")
		killCmd.Env = cmd.Env
		_ = killCmd.Run() // best-effort; "no server running" is not a failure here
	})
	return socketPath
}

// TestCreateSessionWithEnvContext_SurvivesPreexistingBareServer is bd-fug's
// integration case: a real tmux server started before this call, with an
// environment holding neither guard variable, must still end up with a
// session whose OWN environment carries GIT_IDENTITY_ENABLED=1 — proving
// the propagation does not depend on the server (or this process) already
// having it, only on the -e flags and set-environment call this code makes.
func TestCreateSessionWithEnvContext_SurvivesPreexistingBareServer(t *testing.T) {
	if !IsInstalled() {
		t.Skip("tmux not installed")
	}
	socketPath := startBareTmuxServer(t)
	client := NewClientWithSocket(socketPath)

	sessionName := "ntm_test_bare_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	sessionEnv := map[string]string{"GIT_IDENTITY_ENABLED": "1"}
	paneEnv := map[string]string{"AGENT_NAME": sessionName + "-p1"}
	if err := client.CreateSessionWithEnvContext(context.Background(), sessionName, os.TempDir(), 0, sessionEnv, paneEnv); err != nil {
		t.Fatalf("CreateSessionWithEnvContext against bare server: %v", err)
	}
	t.Cleanup(func() { _ = client.KillSession(sessionName) })

	ok, err := client.SessionEnvironmentHasContext(context.Background(), sessionName, "GIT_IDENTITY_ENABLED", "1")
	if err != nil {
		t.Fatalf("SessionEnvironmentHasContext: %v", err)
	}
	if !ok {
		t.Fatal("session created on a bare pre-existing server does not carry GIT_IDENTITY_ENABLED=1")
	}
}

// TestSwarmIdentityEnv_DistinctAgentNamePerPaneViaProcEnviron creates a
// two-pane session and reads each pane's AGENT_NAME back from its own
// process environment (/proc/<pid>/environ), not from tmux's session
// table — show-environment cannot see a pane-scoped variable at all (the
// same limitation documented for CODEX_HOME in codex_home.go). Skipped
// with a stated precondition where /proc is unavailable.
func TestSwarmIdentityEnv_DistinctAgentNamePerPaneViaProcEnviron(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("precondition unmet: /proc/<pid>/environ is Linux-specific")
	}
	if !IsInstalled() {
		t.Skip("tmux not installed")
	}
	acquireGlobalTmuxTestLock(t)

	sessionName := "ntm_test_penv_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	client := NewClient("") // isolated via TMUX_TMPDIR set by this package's TestMain
	sessionEnv := map[string]string{"GIT_IDENTITY_ENABLED": "1"}
	pane1Name := sessionName + "-p1"
	pane2Name := sessionName + "-p2"
	if err := client.CreateSessionWithEnvContext(context.Background(), sessionName, os.TempDir(), 0, sessionEnv, map[string]string{"AGENT_NAME": pane1Name}); err != nil {
		t.Fatalf("CreateSessionWithEnvContext: %v", err)
	}
	t.Cleanup(func() { _ = client.KillSession(sessionName) })

	if _, err := client.SplitWindowWithEnvContext(context.Background(), sessionName, os.TempDir(), map[string]string{"AGENT_NAME": pane2Name}); err != nil {
		t.Fatalf("SplitWindowWithEnvContext: %v", err)
	}

	panes, err := client.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("GetPanes: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("GetPanes returned %d panes, want 2", len(panes))
	}

	got := make(map[string]string, 2) // pane id -> AGENT_NAME read from /proc
	for _, pane := range panes {
		pid, err := client.RunContext(context.Background(), "display-message", "-p", "-t", pane.ID, "-F", "#{pane_pid}")
		if err != nil {
			t.Fatalf("resolve pane pid for %s: %v", pane.ID, err)
		}
		name, err := agentNameFromProcEnviron(strings.TrimSpace(pid))
		if err != nil {
			t.Fatalf("read /proc/%s/environ: %v", pid, err)
		}
		got[pane.ID] = name
	}

	names := make(map[string]bool, len(got))
	for paneID, name := range got {
		if name == "" {
			t.Errorf("pane %s has no AGENT_NAME in its process environment", paneID)
			continue
		}
		if names[name] {
			t.Errorf("AGENT_NAME %q was seen on more than one pane: %v", name, got)
		}
		names[name] = true
	}
	if !names[pane1Name] || !names[pane2Name] {
		t.Errorf("expected AGENT_NAME values %q and %q, got %v", pane1Name, pane2Name, got)
	}
}

// agentNameFromProcEnviron reads AGENT_NAME from a process's environment via
// /proc/<pid>/environ (NUL-separated KEY=VALUE entries).
func agentNameFromProcEnviron(pid string) (string, error) {
	data, err := os.ReadFile("/proc/" + pid + "/environ")
	if err != nil {
		return "", err
	}
	for _, entry := range strings.Split(string(data), "\x00") {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "AGENT_NAME" {
			return value, nil
		}
	}
	return "", errors.New("AGENT_NAME not present")
}
