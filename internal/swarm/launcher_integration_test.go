package swarm

// Integration test for bd-yf3: derive a pane's AGENT_NAME via
// swarm.DerivePaneAgentName, then feed it into tmux.SplitWindowWithEnvContext
// and assert the tmux argv carries the identity in the form swarm-tick
// already parses. Lives here (not in internal/tmux) because tmux imports
// swarm, so a swarm-side test of swarm.DerivePaneAgentName → tmux call is
// the natural seam. Without the package-level SplitWindowWithEnvContext
// wrapper the CLI uses, this would be a method on tmux.Client; with it,
// the seam is the same code path spawn.go and add.go take at runtime.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// writeLaunchFakeTmux installs a fake tmux binary that logs every
// invocation's argv (space-joined, one per line) to logPath, and answers
// the show-environment / split-window calls the launcher needs.
func writeLaunchFakeTmux(t *testing.T, logPath, showEnvOutput string) string {
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
  list-panes)
    printf '%%0_NTM_SEP_0_NTM_SEP_title_NTM_SEP_bash_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_1234_NTM_SEP_0\n'
    ;;
  split-window)
    echo "%$FAKE_TMUX_PANE_SEQ"
    ;;
  has-session)
    echo "can't find session" >&2
    exit 1
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

func readLaunchLog(t *testing.T, logPath string) []string {
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

// TestDerivePaneAgentNameThenSplitWindowAppliesDerivedName is the bd-yf3
// launch-path integration proof: DerivePaneAgentName(session, idx) returns
// a session-pane form that swarm-tick already parses, and the launcher
// feeds that exact string into tmux.SplitWindowWithEnvContext so the
// pane's child process inherits it via /proc/<pid>/environ. A mutation
// that breaks either side — derivation collapsing to a constant, or the
// CLI dropping the wrapper — fails one of the assertions here.
func TestDerivePaneAgentNameThenSplitWindowAppliesDerivedName(t *testing.T) {
	for _, tc := range []struct {
		session   string
		paneIndex int
		wantName  string
	}{
		{"cc_agents_1", 1, "cc_agents_1-p1"},
		{"cc_agents_1", 2, "cc_agents_1-p2"},
		{"proj", 7, "proj-p7"},
	} {
		t.Run(tc.wantName, func(t *testing.T) {
			got := DerivePaneAgentName(tc.session, tc.paneIndex)
			if got != tc.wantName {
				t.Fatalf("DerivePaneAgentName(%q, %d) = %q, want %q", tc.session, tc.paneIndex, got, tc.wantName)
			}

			root := t.TempDir()
			logPath := filepath.Join(root, "tmux.log")
			scriptPath := writeLaunchFakeTmux(t, logPath, "")
			t.Setenv("NTM_TMUX_BINARY", scriptPath)
			t.Setenv("FAKE_TMUX_PANE_SEQ", "9")

			if _, err := tmux.SplitWindowWithEnvContext(context.Background(), tc.session, "/tmp", map[string]string{
				AgentNameVar: got,
			}); err != nil {
				t.Fatalf("SplitWindowWithEnvContext error: %v", err)
			}

			lines := readLaunchLog(t, logPath)
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
			wantArgv := "-e " + AgentNameVar + "=" + tc.wantName
			if !strings.Contains(splitCall, wantArgv) {
				t.Errorf("split-window argv = %q, want it to contain %q", splitCall, wantArgv)
			}
			if strings.Count(splitCall, "-e "+AgentNameVar+"=") != 1 {
				t.Errorf("split-window argv = %q, want exactly one -e %s= occurrence", splitCall, AgentNameVar)
			}
		})
	}
}

// TestDerivePaneAgentNameReturnsDistinctPerPane is the criterion-4 stand-in:
// two panes of one session, derived in order, must produce distinct
// names — otherwise the tracker could not tell one pane's claim from
// another's and the brief's "two pane-mappable assignees" outcome fails
// at the naming step before any registration happens.
func TestDerivePaneAgentNameReturnsDistinctPerPane(t *testing.T) {
	first := DerivePaneAgentName("cc_agents_1", 1)
	second := DerivePaneAgentName("cc_agents_1", 2)
	if first == "" || second == "" {
		t.Fatalf("DerivePaneAgentName returned empty: first=%q second=%q", first, second)
	}
	if first == second {
		t.Fatalf("two panes of one session share identity %q", first)
	}
	if !strings.HasPrefix(first, "cc_agents_1-p") || !strings.HasPrefix(second, "cc_agents_1-p") {
		t.Errorf("identity names %q and %q do not carry the session-pane form", first, second)
	}
}
