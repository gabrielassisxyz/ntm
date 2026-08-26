// Package robot provides machine-readable output for AI agents.
// is_working_session_id_test.go covers the AgentSessionID join (bd-4gw): the
// robot surface must report a session identifier that actually resolves to a
// file in that CLI's own session directory, not merely a non-empty string.
package robot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestGetIsWorkingReportsAgentSessionIDMatchingRealTranscriptFile(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// agentsession.Discoverer resolves the home directory via
	// os.UserHomeDir(); sandbox it so this test only ever touches a temp
	// directory, never the real ~/.claude.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	workDir := t.TempDir()
	session := fmt.Sprintf("ntm-session-id-test-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, workDir); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	defer tmux.KillSession(session)

	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("list panes: panes=%v err=%v", panes, err)
	}
	pane := panes[0]

	// Force agent-type classification to "cc" via the pane title (NTM's own
	// naming convention __<type>_<index>) rather than depending on scrollback
	// content, so this test needs no real Claude Code process running.
	if err := tmux.SetPaneTitle(pane.ID, session+"__cc_1"); err != nil {
		t.Fatalf("set pane title: %v", err)
	}

	// Plant a fake Claude Code transcript for this exact working directory —
	// the native-store fallback discoverClaudeContext resolves purely from
	// home + workDir, needing no matching "claude" process in the pane.
	encodedWorkDir := agentsession.ClaudeProjectDir(workDir)
	projectDir := filepath.Join(tmpHome, ".claude", "projects", encodedWorkDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("create fake claude project dir: %v", err)
	}
	const wantSessionID = "fake-session-abc123"
	transcriptPath := filepath.Join(projectDir, wantSessionID+".jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write fake transcript: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := GetIsWorking(ctx, IsWorkingOptions{
		Session:          session,
		LinesCaptured:    20,
		IncludeSessionID: true,
	})
	if err != nil {
		t.Fatalf("GetIsWorking: %v", err)
	}
	if !output.Success {
		t.Fatalf("GetIsWorking reported failure: %+v", output.RobotResponse)
	}
	if len(output.Panes) != 1 {
		t.Fatalf("expected exactly one pane in output, got %d: %+v", len(output.Panes), output.Panes)
	}

	var got PaneWorkStatus
	for _, status := range output.Panes {
		got = status
	}
	if got.AgentType != "cc" {
		t.Fatalf("AgentType = %q, want %q", got.AgentType, "cc")
	}
	if got.AgentSessionID != wantSessionID {
		t.Fatalf("AgentSessionID = %q, want %q", got.AgentSessionID, wantSessionID)
	}

	// The join itself, walked in the direction a consumer walks it: the path
	// is rebuilt from the id the surface REPORTED, so an id no transcript
	// file backs fails here. Statting the planted path instead would hold
	// whatever the surface said, which is not what the comment claims.
	reportedTranscriptPath := filepath.Join(projectDir, got.AgentSessionID+".jsonl")
	if info, statErr := os.Stat(reportedTranscriptPath); statErr != nil || info.IsDir() {
		t.Fatalf("no transcript file backs reported session id %q at %s: %v", got.AgentSessionID, reportedTranscriptPath, statErr)
	}
}

// TestGetIsWorkingReportsPiAndClaudeSessionIDsInOneCall proves two providers
// answer in a single --session-id call: a cc pane and a pi pane in the same
// session each report their own id, and each id resolves to a real transcript
// file in that provider's own session directory.
func TestGetIsWorkingReportsPiAndClaudeSessionIDsInOneCall(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	workDir := t.TempDir()
	session := fmt.Sprintf("ntm-session-id-pi-test-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, workDir); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	defer tmux.KillSession(session)

	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("list panes: panes=%v err=%v", panes, err)
	}
	if err := tmux.SetPaneTitle(panes[0].ID, session+"__cc_1"); err != nil {
		t.Fatalf("set cc pane title: %v", err)
	}
	piPaneID, err := tmux.SplitWindow(session, workDir)
	if err != nil {
		t.Fatalf("split pi pane: %v", err)
	}
	if err := tmux.SetPaneTitle(piPaneID, session+"__pi_2"); err != nil {
		t.Fatalf("set pi pane title: %v", err)
	}

	// Plant a Claude transcript and a pi transcript for the same workDir.
	claudeProjectDir := filepath.Join(tmpHome, ".claude", "projects", agentsession.ClaudeProjectDir(workDir))
	if err := os.MkdirAll(claudeProjectDir, 0o755); err != nil {
		t.Fatalf("create fake claude project dir: %v", err)
	}
	const wantClaudeID = "fake-claude-session"
	if err := os.WriteFile(filepath.Join(claudeProjectDir, wantClaudeID+".jsonl"), []byte(`{"type":"user"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write fake claude transcript: %v", err)
	}

	piSessionsDir := filepath.Join(tmpHome, ".pi", "agent", "sessions", agentsession.PiProjectDir(workDir))
	if err := os.MkdirAll(piSessionsDir, 0o755); err != nil {
		t.Fatalf("create fake pi sessions dir: %v", err)
	}
	const wantPiID = "fake-pi-session"
	piHeader := `{"type":"session","version":3,"id":"` + wantPiID + `","timestamp":"2026-08-26T02:46:53.012Z","cwd":"` + workDir + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(piSessionsDir, "2026-08-26T02-46-53-012Z_"+wantPiID+".jsonl"), []byte(piHeader), 0o644); err != nil {
		t.Fatalf("write fake pi transcript: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := GetIsWorking(ctx, IsWorkingOptions{
		Session:          session,
		LinesCaptured:    20,
		IncludeSessionID: true,
		Panes:            []int{0, 1},
	})
	if err != nil {
		t.Fatalf("GetIsWorking: %v", err)
	}
	if !output.Success {
		t.Fatalf("GetIsWorking reported failure: %+v", output.RobotResponse)
	}
	if len(output.Panes) != 2 {
		t.Fatalf("expected two panes in output, got %d: %+v", len(output.Panes), output.Panes)
	}

	byType := make(map[string]PaneWorkStatus, len(output.Panes))
	for _, status := range output.Panes {
		byType[status.AgentType] = status
	}
	ccStatus, ok := byType["cc"]
	if !ok {
		t.Fatalf("no cc pane in output: %+v", output.Panes)
	}
	if ccStatus.AgentSessionID != wantClaudeID {
		t.Fatalf("cc AgentSessionID = %q, want %q", ccStatus.AgentSessionID, wantClaudeID)
	}
	piStatus, ok := byType["pi"]
	if !ok {
		t.Fatalf("no pi pane in output: %+v", output.Panes)
	}
	if piStatus.AgentSessionID != wantPiID {
		t.Fatalf("pi AgentSessionID = %q, want %q", piStatus.AgentSessionID, wantPiID)
	}

	// The join, walked in the direction a consumer walks it: rebuild the path
	// from the reported id and stat it, rather than statting the planted path.
	matches, err := filepath.Glob(filepath.Join(piSessionsDir, "*_"+piStatus.AgentSessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no pi transcript file backs reported id %q: %v", piStatus.AgentSessionID, err)
	}
}

// TestGetIsWorkingOmitsAgentSessionIDWithoutOptIn proves the field costs
// nothing on the default poll path: with IncludeSessionID left off, no
// discovery runs and the field stays absent even though the same fake
// transcript exists on disk.
func TestGetIsWorkingOmitsAgentSessionIDWithoutOptIn(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	workDir := t.TempDir()
	session := fmt.Sprintf("ntm-session-id-optout-test-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, workDir); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	defer tmux.KillSession(session)

	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("list panes: panes=%v err=%v", panes, err)
	}
	if err := tmux.SetPaneTitle(panes[0].ID, session+"__cc_1"); err != nil {
		t.Fatalf("set cc pane title: %v", err)
	}
	piPaneID, err := tmux.SplitWindow(session, workDir)
	if err != nil {
		t.Fatalf("split pi pane: %v", err)
	}
	if err := tmux.SetPaneTitle(piPaneID, session+"__pi_2"); err != nil {
		t.Fatalf("set pi pane title: %v", err)
	}

	// Plant both a Claude and a pi transcript; neither may be read without
	// the opt-in flag.
	encodedWorkDir := agentsession.ClaudeProjectDir(workDir)
	projectDir := filepath.Join(tmpHome, ".claude", "projects", encodedWorkDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("create fake claude project dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "fake-session-xyz.jsonl"), []byte(`{"type":"user"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write fake claude transcript: %v", err)
	}
	piSessionsDir := filepath.Join(tmpHome, ".pi", "agent", "sessions", agentsession.PiProjectDir(workDir))
	if err := os.MkdirAll(piSessionsDir, 0o755); err != nil {
		t.Fatalf("create fake pi sessions dir: %v", err)
	}
	piHeader := `{"type":"session","version":3,"id":"fake-pi-session","timestamp":"2026-08-26T02:46:53.012Z","cwd":"` + workDir + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(piSessionsDir, "2026-08-26T02-46-53-012Z_fake-pi-session.jsonl"), []byte(piHeader), 0o644); err != nil {
		t.Fatalf("write fake pi transcript: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := GetIsWorking(ctx, IsWorkingOptions{Session: session, LinesCaptured: 20, Panes: []int{0, 1}})
	if err != nil {
		t.Fatalf("GetIsWorking: %v", err)
	}
	for _, status := range output.Panes {
		if status.AgentSessionID != "" {
			t.Fatalf("AgentSessionID = %q, want empty without --session-id", status.AgentSessionID)
		}
	}
}
