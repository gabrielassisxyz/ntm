package tmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeTmux installs a fake tmux binary that appends each invocation's
// argv (space-joined, one line per call) to logPath, and — for
// show-environment — prints showEnvOutput instead of talking to a real
// server. Every other subcommand succeeds immediately, split-window
// printing a fresh pane id so callers relying on `-P -F '#{pane_id}'` get a
// value back.
func writeFakeTmux(t *testing.T, logPath, showEnvOutput string) string {
	t.Helper()
	root := t.TempDir()
	scriptPath := filepath.Join(root, "tmux")
	script := `#!/bin/sh
set -eu
echo "$*" >> '` + logPath + `'
case "$1" in
  show-environment)
    cat '` + filepath.Join(root, "show-env.txt") + `'
    ;;
  list-windows)
    echo "0"
    ;;
  split-window)
    echo "%$FAKE_TMUX_PANE_SEQ"
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "show-env.txt"), []byte(showEnvOutput), 0o600); err != nil {
		t.Fatalf("write fake show-environment output: %v", err)
	}
	return scriptPath
}

// readLog returns the fake tmux invocation log as a slice of lines, one per
// call, in call order.
func readLog(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read fake tmux log: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestCreateSessionWithEnvContext_EquipsFirstPane is the argv-assertion
// test from bd-fug's Tests section: the command recorded for session
// creation must itself carry -e GIT_IDENTITY_ENABLED=1 and
// -e AGENT_NAME=<session>-p1 — proving pane 1 is equipped by new-session
// rather than left for a split-window path that never reaches it.
func TestCreateSessionWithEnvContext_EquipsFirstPane(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "tmux.log")
	scriptPath := writeFakeTmux(t, logPath, "GIT_IDENTITY_ENABLED=1\n")
	t.Setenv("NTM_TMUX_BINARY", scriptPath)
	t.Setenv("FAKE_TMUX_PANE_SEQ", "1")

	client := NewClient("")
	sessionEnv := map[string]string{"GIT_IDENTITY_ENABLED": "1"}
	paneEnv := map[string]string{"AGENT_NAME": "cc_agents_1-p1"}
	if err := client.CreateSessionWithEnvContext(context.Background(), "cc_agents_1", "/tmp", 0, sessionEnv, paneEnv); err != nil {
		t.Fatalf("CreateSessionWithEnvContext error: %v", err)
	}

	lines := readLog(t, logPath)
	if len(lines) == 0 {
		t.Fatal("fake tmux recorded no invocations")
	}
	newSessionCall := lines[0]
	if !strings.HasPrefix(newSessionCall, "new-session ") {
		t.Fatalf("first call = %q, want a new-session invocation", newSessionCall)
	}
	if !strings.Contains(newSessionCall, "-e GIT_IDENTITY_ENABLED=1") {
		t.Errorf("new-session call = %q, want it to contain -e GIT_IDENTITY_ENABLED=1", newSessionCall)
	}
	if !strings.Contains(newSessionCall, "-e AGENT_NAME=cc_agents_1-p1") {
		t.Errorf("new-session call = %q, want it to contain -e AGENT_NAME=cc_agents_1-p1", newSessionCall)
	}

	var sawSetEnvironment bool
	for _, line := range lines {
		if strings.HasPrefix(line, "set-environment ") && strings.Contains(line, "GIT_IDENTITY_ENABLED 1") {
			sawSetEnvironment = true
		}
	}
	if !sawSetEnvironment {
		t.Errorf("no set-environment call persisting GIT_IDENTITY_ENABLED found in %v", lines)
	}
}

// TestSplitWindowWithEnvContext_EquipsSecondPane proves the second pane of
// a session gets its own, distinct AGENT_NAME via split-window's -e, as
// required by bd-fug (a change touching only split-window would leave pane
// 1 unequipped, but this test alone cannot see that mistake — it is paired
// with TestCreateSessionWithEnvContext_EquipsFirstPane for that reason).
func TestSplitWindowWithEnvContext_EquipsSecondPane(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "tmux.log")
	scriptPath := writeFakeTmux(t, logPath, "GIT_IDENTITY_ENABLED=1\n")
	t.Setenv("NTM_TMUX_BINARY", scriptPath)
	t.Setenv("FAKE_TMUX_PANE_SEQ", "2")

	client := NewClient("")
	paneEnv := map[string]string{"AGENT_NAME": "cc_agents_1-p2"}
	if _, err := client.SplitWindowWithEnvContext(context.Background(), "cc_agents_1", "/tmp", paneEnv); err != nil {
		t.Fatalf("SplitWindowWithEnvContext error: %v", err)
	}

	lines := readLog(t, logPath)
	var splitCall string
	for _, line := range lines {
		if strings.HasPrefix(line, "split-window ") {
			splitCall = line
			break
		}
	}
	if splitCall == "" {
		t.Fatalf("no split-window call found in %v", lines)
	}
	if !strings.Contains(splitCall, "-e AGENT_NAME=cc_agents_1-p2") {
		t.Errorf("split-window call = %q, want it to contain -e AGENT_NAME=cc_agents_1-p2", splitCall)
	}
}

// TestSessionEnvironmentHasContext_IgnoresCallerProcessEnv is the bd-fug
// planted negative: the calling process having GIT_IDENTITY_ENABLED and
// AGENT_NAME set in its own os.Getenv must NOT by itself satisfy the
// check. The fake tmux server's show-environment output — the session's
// own table — omits both, and verification must fail regardless of what
// this test process's environment holds.
func TestSessionEnvironmentHasContext_IgnoresCallerProcessEnv(t *testing.T) {
	// The obvious wrong implementation reads os.Getenv("GIT_IDENTITY_ENABLED")
	// instead of asking tmux; setting it here is what would make that
	// implementation pass while the correct one still fails.
	t.Setenv("GIT_IDENTITY_ENABLED", "1")
	t.Setenv("AGENT_NAME", "cc_agents_1-p1")

	root := t.TempDir()
	logPath := filepath.Join(root, "tmux.log")
	// A session environment that does NOT carry GIT_IDENTITY_ENABLED —
	// the case a tmux server that predates this launch produces.
	scriptPath := writeFakeTmux(t, logPath, "-GIT_IDENTITY_ENABLED\nSSH_AUTH_SOCK=/tmp/agent.sock\n")
	t.Setenv("NTM_TMUX_BINARY", scriptPath)

	client := NewClient("")
	ok, err := client.SessionEnvironmentHasContext(context.Background(), "cc_agents_1", "GIT_IDENTITY_ENABLED", "1")
	if err != nil {
		t.Fatalf("SessionEnvironmentHasContext error: %v", err)
	}
	if ok {
		t.Fatal("SessionEnvironmentHasContext = true, want false: the session table has no GIT_IDENTITY_ENABLED, regardless of this process's own environment")
	}
}

// TestSessionEnvironmentHasContext_TrueWhenSessionCarriesIt is the positive
// counterpart: when the session's own table does carry the variable,
// verification must report it, so the negative test above is not passing
// merely because the function always returns false.
func TestSessionEnvironmentHasContext_TrueWhenSessionCarriesIt(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "tmux.log")
	scriptPath := writeFakeTmux(t, logPath, "AGENT_NAME=cc_agents_1-p1\nGIT_IDENTITY_ENABLED=1\n")
	t.Setenv("NTM_TMUX_BINARY", scriptPath)

	client := NewClient("")
	ok, err := client.SessionEnvironmentHasContext(context.Background(), "cc_agents_1", "GIT_IDENTITY_ENABLED", "1")
	if err != nil {
		t.Fatalf("SessionEnvironmentHasContext error: %v", err)
	}
	if !ok {
		t.Fatal("SessionEnvironmentHasContext = false, want true: the fake session table carries GIT_IDENTITY_ENABLED=1")
	}
}
