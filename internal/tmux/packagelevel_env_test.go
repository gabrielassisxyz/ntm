package tmux

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestPackageLevelSplitWindowWithEnvContextPropagatesPaneEnv proves the
// package-level SplitWindowWithEnvContext wrapper (the entrypoint the CLI
// uses, not the *Client method) propagates paneEnv into the split-window
// argv via -e flags. Without this guard, a refactor that wires the CLI to
// the package-level wrapper would silently drop the pane identity (bd-yf3).
func TestPackageLevelSplitWindowWithEnvContextPropagatesPaneEnv(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "tmux.log")
	scriptPath := writeFakeTmux(t, logPath, "")
	t.Setenv("NTM_TMUX_BINARY", scriptPath)
	t.Setenv("FAKE_TMUX_PANE_SEQ", "7")

	paneID, err := SplitWindowWithEnvContext(context.Background(), "cc_agents_1", "/tmp", map[string]string{
		"AGENT_NAME":           "cc_agents_1-p2",
		"GIT_IDENTITY_ENABLED": "1",
	})
	if err != nil {
		t.Fatalf("SplitWindowWithEnvContext error: %v", err)
	}
	if paneID != "%7" {
		t.Errorf("paneID = %q, want %%7", paneID)
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
		t.Errorf("split-window argv = %q, want -e AGENT_NAME=cc_agents_1-p2", splitCall)
	}
	if !strings.Contains(splitCall, "-e GIT_IDENTITY_ENABLED=1") {
		t.Errorf("split-window argv = %q, want -e GIT_IDENTITY_ENABLED=1", splitCall)
	}
}

// TestPackageLevelCreateSessionWithEnvContextPropagatesBothEnvs proves the
// package-level CreateSessionWithEnvContext wrapper passes BOTH sessionEnv
// (GIT_IDENTITY_ENABLED) and paneEnv (AGENT_NAME) into the new-session
// argv. A regression here would leave pane 1 unequipped, and the CLI's
// first-pane path would silently lose its identity (bd-yf3).
func TestPackageLevelCreateSessionWithEnvContextPropagatesBothEnvs(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "tmux.log")
	scriptPath := writeFakeTmux(t, logPath, "GIT_IDENTITY_ENABLED=1\n")
	t.Setenv("NTM_TMUX_BINARY", scriptPath)
	t.Setenv("FAKE_TMUX_PANE_SEQ", "1")

	if err := CreateSessionWithEnvContext(context.Background(), "cc_agents_1", "/tmp", 0,
		map[string]string{"GIT_IDENTITY_ENABLED": "1"},
		map[string]string{"AGENT_NAME": "cc_agents_1-p1"},
	); err != nil {
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
		t.Errorf("new-session argv = %q, want -e GIT_IDENTITY_ENABLED=1", newSessionCall)
	}
	if !strings.Contains(newSessionCall, "-e AGENT_NAME=cc_agents_1-p1") {
		t.Errorf("new-session argv = %q, want -e AGENT_NAME=cc_agents_1-p1", newSessionCall)
	}
}
