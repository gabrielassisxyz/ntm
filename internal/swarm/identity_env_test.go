package swarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestDerivePaneAgentName(t *testing.T) {
	tests := []struct {
		name        string
		sessionName string
		paneIndex   int
		want        string
	}{
		{name: "normal pane 1", sessionName: "cc_agents_1", paneIndex: 1, want: "cc_agents_1-p1"},
		{name: "normal pane 2, distinct from pane 1", sessionName: "cc_agents_1", paneIndex: 2, want: "cc_agents_1-p2"},
		{name: "empty session name yields empty", sessionName: "", paneIndex: 1, want: ""},
		{name: "zero index yields empty", sessionName: "cc_agents_1", paneIndex: 0, want: ""},
		{name: "negative index yields empty", sessionName: "cc_agents_1", paneIndex: -1, want: ""},
		{name: "blank session name yields empty", sessionName: "   ", paneIndex: 1, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DerivePaneAgentName(tt.sessionName, tt.paneIndex)
			if got != tt.want {
				t.Errorf("DerivePaneAgentName(%q, %d) = %q, want %q", tt.sessionName, tt.paneIndex, got, tt.want)
			}
		})
	}
}

// writeFakeTmuxForOrchestrator installs a fake tmux binary that logs every
// invocation's argv (space-joined, one per line) to logPath, and answers
// show-environment with showEnvOutput. Every other command that needs
// stdout to make its caller happy (list-windows, list-panes,
// display-message, split-window) returns a fixed, valid value.
func writeFakeTmuxForOrchestrator(t *testing.T, logPath, showEnvOutput string) string {
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
    # Matches internal/tmux's GetPanes format: id, index, title, command,
    # width, height, active, pid, window_index, joined by FieldSeparator.
    printf '%%0_NTM_SEP_1_NTM_SEP_title_NTM_SEP_bash_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_1234_NTM_SEP_0\n'
    ;;
  split-window)
    echo "%1"
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

func readOrchestratorLog(t *testing.T, logPath string) []string {
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

// TestCreateSession_IdentityEnvEnabled_EquipsSessionAndVerifies proves
// SessionOrchestrator.createSession, when told identityEnvEnabled=true,
// both equips the new-session call with GIT_IDENTITY_ENABLED/AGENT_NAME
// and verifies GIT_IDENTITY_ENABLED afterward via show-environment.
func TestCreateSession_IdentityEnvEnabled_EquipsSessionAndVerifies(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "tmux.log")
	scriptPath := writeFakeTmuxForOrchestrator(t, logPath, "GIT_IDENTITY_ENABLED=1\n")
	t.Setenv("NTM_TMUX_BINARY", scriptPath)

	orch := NewSessionOrchestrator()
	client := tmux.NewClient("")
	spec := SessionSpec{
		Name: "cc_agents_1",
		Panes: []PaneSpec{
			{Index: 1, AgentType: "cc"},
		},
	}

	result := orch.createSession(client, spec, true)
	if result.Error != nil {
		t.Fatalf("createSession error: %v", result.Error)
	}

	lines := readOrchestratorLog(t, logPath)
	var newSessionCall string
	for _, line := range lines {
		if strings.HasPrefix(line, "new-session ") {
			newSessionCall = line
			break
		}
	}
	if newSessionCall == "" {
		t.Fatalf("no new-session call found in %v", lines)
	}
	if !strings.Contains(newSessionCall, "-e "+GitIdentityEnabledVar+"=1") {
		t.Errorf("new-session call = %q, want -e %s=1", newSessionCall, GitIdentityEnabledVar)
	}
	if !strings.Contains(newSessionCall, "-e "+AgentNameVar+"=cc_agents_1-p1") {
		t.Errorf("new-session call = %q, want -e %s=cc_agents_1-p1", newSessionCall, AgentNameVar)
	}
	// WORKTREES_ENABLED declares a different topology (one worktree per
	// agent) that a shared-checkout swarm does not have; bd-fug is
	// deliberate about never setting it.
	if strings.Contains(newSessionCall, "WORKTREES_ENABLED") {
		t.Errorf("new-session call = %q, must never set WORKTREES_ENABLED", newSessionCall)
	}

	var sawShowEnvironment bool
	for _, line := range lines {
		if strings.HasPrefix(line, "show-environment ") {
			sawShowEnvironment = true
		}
	}
	if !sawShowEnvironment {
		t.Errorf("no show-environment verification call found in %v", lines)
	}
}

// TestCreateSession_IdentityEnvEnabled_RefusesAndTearsDownOnVerifyFailure
// covers the runtime refusal: when show-environment does not carry
// GIT_IDENTITY_ENABLED, createSession must return an error naming the
// session, kill the session it just created, and never reach GetPanes,
// pane titling, or split-window for a later pane.
func TestCreateSession_IdentityEnvEnabled_RefusesAndTearsDownOnVerifyFailure(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "tmux.log")
	// A session table that does NOT carry GIT_IDENTITY_ENABLED — the case a
	// tmux server that predates this launch produces.
	scriptPath := writeFakeTmuxForOrchestrator(t, logPath, "-GIT_IDENTITY_ENABLED\n")
	t.Setenv("NTM_TMUX_BINARY", scriptPath)

	orch := NewSessionOrchestrator()
	client := tmux.NewClient("")
	spec := SessionSpec{
		Name: "cc_agents_1",
		Panes: []PaneSpec{
			{Index: 1, AgentType: "cc"},
			{Index: 2, AgentType: "cc"},
		},
	}

	result := orch.createSession(client, spec, true)
	if result.Error == nil {
		t.Fatal("expected createSession to refuse when GIT_IDENTITY_ENABLED does not verify")
	}
	if !strings.Contains(result.Error.Error(), spec.Name) || !strings.Contains(result.Error.Error(), GitIdentityEnabledVar) {
		t.Errorf("error = %v, want it to name the session and the variable", result.Error)
	}
	if len(result.PaneIDs) != 0 {
		t.Errorf("PaneIDs = %v, want none — no pane may be considered launched", result.PaneIDs)
	}

	lines := readOrchestratorLog(t, logPath)
	var sawKillSession, sawSplitWindow, sawListPanes bool
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "kill-session "):
			sawKillSession = true
		case strings.HasPrefix(line, "split-window "):
			sawSplitWindow = true
		case strings.HasPrefix(line, "list-panes "):
			sawListPanes = true
		}
	}
	if !sawKillSession {
		t.Errorf("no kill-session call found in %v — a failed-verification session must be torn down", lines)
	}
	if sawSplitWindow {
		t.Errorf("split-window was called in %v — no pane may be created after a failed verification", lines)
	}
	if sawListPanes {
		t.Errorf("list-panes was called in %v — createSession must refuse before reading panes back", lines)
	}
}

// TestCreateSession_IdentityEnvDisabled_MatchesPreExistingBehavior proves
// identityEnvEnabled=false takes exactly the pre-bd-fug path: no -e flags,
// no show-environment verification call, reproducing "a launch where
// everything is already correct behaves exactly as today" for the case
// where the layer is off altogether.
func TestCreateSession_IdentityEnvDisabled_MatchesPreExistingBehavior(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "tmux.log")
	scriptPath := writeFakeTmuxForOrchestrator(t, logPath, "GIT_IDENTITY_ENABLED=1\n")
	t.Setenv("NTM_TMUX_BINARY", scriptPath)

	orch := NewSessionOrchestrator()
	client := tmux.NewClient("")
	spec := SessionSpec{
		Name:  "cc_agents_1",
		Panes: []PaneSpec{{Index: 1, AgentType: "cc"}},
	}

	result := orch.createSession(client, spec, false)
	if result.Error != nil {
		t.Fatalf("createSession error: %v", result.Error)
	}

	lines := readOrchestratorLog(t, logPath)
	for _, line := range lines {
		if strings.Contains(line, "-e ") {
			t.Errorf("identityEnvEnabled=false must not add any -e flag, found in %q (full log: %v)", line, lines)
		}
		if strings.HasPrefix(line, "show-environment ") {
			t.Errorf("identityEnvEnabled=false must not verify the session environment, found in %v", lines)
		}
	}
}

// TestCreateSession_IdentityEnvEnabled_EquipsPiPanes proves a pi session —
// the lane bd-ut0 adds to the swarm allocation — flows through the same
// identity-env path as cc/cod/gmi: every pi pane's tmux create call carries
// GIT_IDENTITY_ENABLED=1 (session-scoped) and a distinct AGENT_NAME
// (pane-scoped), which is what /proc/<pid>/environ of the pi process then
// shows.
func TestCreateSession_IdentityEnvEnabled_EquipsPiPanes(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "tmux.log")
	scriptPath := writeFakeTmuxForOrchestrator(t, logPath, "GIT_IDENTITY_ENABLED=1\n")
	t.Setenv("NTM_TMUX_BINARY", scriptPath)

	orch := NewSessionOrchestrator()
	orch.StaggerDelay = 0
	client := tmux.NewClient("")
	spec := SessionSpec{
		Name:      "pi_agents_1",
		AgentType: "pi",
		Panes: []PaneSpec{
			{Index: 1, AgentType: "pi", LaunchCmd: "pi"},
			{Index: 2, AgentType: "pi", LaunchCmd: "pi"},
		},
	}

	result := orch.createSession(client, spec, true)
	if result.Error != nil {
		t.Fatalf("createSession error: %v", result.Error)
	}

	lines := readOrchestratorLog(t, logPath)
	var newSessionCall, splitWindowCall string
	for _, line := range lines {
		if strings.HasPrefix(line, "new-session ") {
			newSessionCall = line
		}
		if strings.HasPrefix(line, "split-window ") {
			splitWindowCall = line
		}
	}
	if newSessionCall == "" {
		t.Fatalf("no new-session call for the pi session in %v", lines)
	}
	if !strings.Contains(newSessionCall, "-e "+GitIdentityEnabledVar+"=1") {
		t.Errorf("pi new-session call = %q, want -e %s=1", newSessionCall, GitIdentityEnabledVar)
	}
	if !strings.Contains(newSessionCall, "-e "+AgentNameVar+"=pi_agents_1-p1") {
		t.Errorf("pi new-session call = %q, want -e %s=pi_agents_1-p1", newSessionCall, AgentNameVar)
	}
	if splitWindowCall == "" {
		t.Fatalf("no split-window call for pi pane 2 in %v", lines)
	}
	if !strings.Contains(splitWindowCall, "-e "+AgentNameVar+"=pi_agents_1-p2") {
		t.Errorf("pi split-window call = %q, want a distinct -e %s=pi_agents_1-p2", splitWindowCall, AgentNameVar)
	}
	if strings.Contains(splitWindowCall, "AGENT_NAME=pi_agents_1-p1") {
		t.Errorf("pi pane 2 reused pane 1's AGENT_NAME: %q", splitWindowCall)
	}
}

// TestCreateSessions_DefaultPlanMatchesPreExistingBehavior exercises the
// CreateSessions -> createSession wiring (plan.IdentityEnvEnabled), not
// createSession directly: a SwarmPlan built without setting
// IdentityEnvEnabled — exactly what every pre-bd-fug test and caller
// does — must produce the same tmux commands as before this bead, with
// zero -e flags anywhere.
func TestCreateSessions_DefaultPlanMatchesPreExistingBehavior(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "tmux.log")
	scriptPath := writeFakeTmuxForOrchestrator(t, logPath, "GIT_IDENTITY_ENABLED=1\n")
	t.Setenv("NTM_TMUX_BINARY", scriptPath)

	orch := NewSessionOrchestrator()
	orch.TmuxClient = tmux.NewClient("")
	plan := &SwarmPlan{
		Sessions: []SessionSpec{
			{Name: "cc_agents_1", Panes: []PaneSpec{{Index: 1, AgentType: "cc"}}},
		},
	}

	result, err := orch.CreateSessions(plan)
	if err != nil {
		t.Fatalf("CreateSessions error: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("CreateSessions result.Errors = %v, want none", result.Errors)
	}

	lines := readOrchestratorLog(t, logPath)
	for _, line := range lines {
		if strings.Contains(line, "-e ") {
			t.Errorf("a plan with IdentityEnvEnabled left at its zero value must not add any -e flag, found in %q (full log: %v)", line, lines)
		}
	}
}
