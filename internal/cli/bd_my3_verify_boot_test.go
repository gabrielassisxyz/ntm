package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// alwaysBusySpawnObserver is the bd-my3 review-follow-up test fixture:
// a spawnSessionObserver that always reports every pane as StateWorking
// with confidence 0.50 (below the 0.75 dispatch floor). It deliberately
// returns a pane ID that does NOT match the actual tmux pane (the
// dispatcher checks by pane ID via spawnObservationSafeToDispatch ->
// PaneByID), so spawnObservationSafeToDispatch returns false. The
// dispatcher records the readiness refusal in setupErrors, and the
// goroutine records the eventual readiness-timeout error in setupErrors
// too — both surface through the bd-my3 partial-failure branch.
type alwaysBusySpawnObserver struct {
	calls int
}

func (o *alwaysBusySpawnObserver) Observe(ctx context.Context, session string) (statuspkg.SessionObservation, error) {
	o.calls++
	return statuspkg.SessionObservation{
		Session:    session,
		ObservedAt: time.Now().UTC(),
		Complete:   true,
		Panes: []statuspkg.PaneObservation{{
			Pane:      tmux.PaneRef{ID: "%never-matches", WindowIndex: 0, PaneIndex: 999},
			PaneName:  "demo__cc_999",
			AgentType: string(agent.AgentTypeClaudeCode),
			Current: statuspkg.StateObservation{
				Status: statuspkg.AgentStatus{
					PaneID:    "%never-matches",
					PaneName:  "demo__cc_999",
					AgentType: string(agent.AgentTypeClaudeCode),
					State:     statuspkg.StateWorking,
				},
				ObservedAt: time.Now().UTC(),
				Freshness:  statuspkg.FreshnessFresh,
				Confidence: 0.50,
			},
			RawOutput: "thinking",
		}},
	}, nil
}

// TestSpawnVerifyBootHardFailsOnPartialReady drives the bd-my3
// partial-failure contract's verify-boot opt-out end-to-end: a session
// with one user pane + one agent pane whose prompt-delivery observation
// always refuses (so the dispatcher records errors in setupErrors) must
// hard-fail when opts.VerifyBoot is set, and the error must name the
// failing pane in the bd-my3 partial-failure format.
//
// WITHOUT the bd-my3 verify-boot branch in spawn.go, the same input
// returns the later post-launch wait-for-agents-ready error (`0/N agent(s)
// ready within ...`) — a different message format the test rejects.
// The test therefore binds specifically to the bd-my3 branch.
//
// The test uses a real tmux session with isolated TMUX_TMPDIR (per the
// brief's wiring guidance) because spawn's setup phase goes through
// tmux.NewSession/SplitWindow and the readiness gate uses real pane
// topology. Without a real session, setupErrors is empty, the
// partial-failure branch never fires, and the test would degenerate into
// "any non-nil error counts as passing" — which is exactly the failure
// mode the previous run's tests fell into.
func TestSpawnVerifyBootHardFailsOnPartialReady(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Save and restore global state we touch.
	origCfg := cfg
	origJSON := jsonOutput
	origObserverFactory := newSpawnSessionObserver
	t.Cleanup(func() {
		cfg = origCfg
		jsonOutput = origJSON
		newSpawnSessionObserver = origObserverFactory
	})

	// Isolated project dir for the session.
	tmpDir, err := os.MkdirTemp("", "ntm-bd-my3-verify-boot")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	jsonOutput = true
	cfg.Agents.Claude = testAgentCatCommandTemplate

	sessionName := fmt.Sprintf("ntm-bd-my3-verify-boot-%d", time.Now().UnixNano())
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(sessionName) })

	// Override the spawn's session observer with the always-busy fake so
	// the dispatcher's readiness check never resolves.
	fakeObserver := &alwaysBusySpawnObserver{}
	newSpawnSessionObserver = func() spawnSessionObserver { return fakeObserver }

	spawnOpts := SpawnOptions{
		Session:    sessionName,
		Agents:     []FlatAgent{{Type: AgentTypeClaude, Index: 1, Model: "test-model"}},
		CCCount:    1,
		UserPane:   true,
		VerifyBoot: true,
		// Prompt is required so buildSpawnPromptSequenceForAgent returns
		// a user_prompt step (rather than an empty list that short-
		// circuits the dispatcher goroutine and leaves setupErrors empty).
		Prompt: "test prompt to drive prompt-delivery path",
	}

	spawnErr := spawnSessionLogicContext(t.Context(), spawnOpts)
	if spawnErr == nil {
		t.Fatalf("spawnSessionLogicContext returned nil; want the bd-my3 partial-failure error from a fake-busy observer + opts.VerifyBoot=true")
	}
	if !strings.Contains(spawnErr.Error(), "spawn --verify-boot: pane") {
		t.Fatalf("spawn error = %v, want it to start with `spawn --verify-boot: pane` — the bd-my3 partial-failure branch's exact format, distinct from the later post-launch branch which formats as `spawn --verify-boot: 0/N agent(s) ready within ...`", spawnErr)
	}
	if !strings.Contains(spawnErr.Error(), "%1") {
		t.Errorf("spawn error = %v, want it to name the failing pane %%1", spawnErr)
	}
	if fakeObserver.calls == 0 {
		t.Errorf("observer.Observe was never called; the spawn returned without exercising the dispatcher — the verify-boot branch never fired")
	}

	// And: the partial-success contract says the session must remain
	// usable after the verify-boot hard-fail — the operator (or
	// --robot-recover) needs to inspect what happened. Without the bd-my3
	// branch the earlier hard-fail short-circuits the flow, so this is
	// the consumer-visible confirmation the branch's sibling block
	// (the partial-success one below it) is not the test's target.
	if !tmux.SessionExists(sessionName) {
		t.Errorf("session %s was not present after the verify-boot hard-fail; the partial-success contract requires the session to remain usable for the operator (or --robot-recover) to inspect", sessionName)
	}
}