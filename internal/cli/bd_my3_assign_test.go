package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/output"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestAgyIdleAtPromptAcceptsDirectAssignWithoutForce is the bd-my3
// acceptance criterion case 1: an Antigravity pane at its input prompt
// (the shakedown capture) must classify idle, accept dispatch, and the
// direct-assign path must NOT refuse it with "pane 7 is busy (state:
// working), use --force to override" the way it did before the fix.
// The reproduction (orchestrator survey, 2026-08-31) was specifically
// an agy pane at its prompt that the assign path rejected despite the
// operator being able to see it was idle.
func TestAgyIdleAtPromptAcceptsDirectAssignWithoutForce(t *testing.T) {
	observedAt := time.Now().UTC()
	observer := newAgySessionObserver(observedAt, agyReadyFooterCapture)

	observation, err := observer.Observe(t.Context(), "demo")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	pane, ok := observation.PaneByID("%92")
	if !ok {
		t.Fatal("observed pane missing")
	}

	// The unified-detector's verdict: this is the value the assign
	// path's `paneObservation.SafeToDispatch()` reads. A wrong value
	// here is exactly the bug the brief reports.
	if pane.Current.Status.State != statuspkg.StateIdle {
		t.Fatalf("pane state = %s, want idle — the assign path would refuse the assignment as PANE_BUSY", pane.Current.Status.State)
	}
	if pane.Current.Confidence < statuspkg.MinimumDispatchConfidence {
		t.Fatalf("pane confidence = %.2f, want >= %.2f — observationConfidence was downgraded to 0.5 by the bd-3nv-style path",
			pane.Current.Confidence, statuspkg.MinimumDispatchConfidence)
	}
	if !pane.SafeToDispatch() {
		t.Fatalf("pane SafeToDispatch() = false on a visibly-idle agy pane; the assign path would print `pane is busy (state: %s), use --force to override`",
			pane.Current.Status.State)
	}
	if pane.Current.Freshness != statuspkg.FreshnessFresh {
		t.Fatalf("pane freshness = %s, want fresh — stale observations must always refuse dispatch", pane.Current.Freshness)
	}
}

// TestAgyIdleAtPromptClassifierChainIsAuthoritative pins the chain the
// bd-my3 fix restored. classifyState (determineStateAt) must return
// StateIdle, the observationConfidence layer must agree (0.95, not
// 0.5), and the SafeToDispatch short-circuit at SafeToDispatch() must
// pass. The third assertion is the one that surfaces the
// user-visible symptom: a value mismatch between classifyState
// ("idle") and DetectIdleFromOutput ("not idle") drops confidence to
// 0.5 and breaks the gate, which is what the bd-3nv pi fix cured
// for pi and what bd-my3 cures for agy.
func TestAgyIdleAtPromptClassifierChainIsAuthoritative(t *testing.T) {
	observedAt := time.Now().UTC()
	detector := statuspkg.NewDetector()

	res := detector.AnalyzeAt("%92", "demo__agy_92", "agy", agyReadyFooterCapture, observedAt.Add(-time.Minute), observedAt)
	if res.State != statuspkg.StateIdle {
		t.Fatalf("determineStateAt state = %s, want idle — the bd-my3 agy arm must catch the chevron past the parser's 5-line window", res.State)
	}

	if !statuspkg.DetectIdleFromOutput(agyReadyFooterCapture, "agy") {
		t.Fatalf("DetectIdleFromOutput(agy) = false on a shakedown-shaped idle pane; observationConfidence would drop to 0.5 and the assign path would refuse the pane")
	}

	// And the dispatch gate passes: the gate is what reads "is this
	// pane actually ready for prompt delivery" — a state=idle,
	// conf=0.95 observation is the only combination that authorizes
	// dispatch.
	obs, err := newAgySessionObserver(observedAt, agyReadyFooterCapture).Observe(t.Context(), "demo")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	pane, _ := obs.PaneByID("%92")
	if !pane.SafeToDispatch() {
		t.Fatalf("SafeToDispatch = false: state=%s conf=%.2f freshness=%s",
			pane.Current.Status.State, pane.Current.Confidence, pane.Current.Freshness)
	}
}

// TestAgyMidBootStillRefusesDispatch is the negative side of the
// bd-my3 fix: a mid-boot agy pane (no prompt yet, just startup
// chatter) must keep refusing dispatch. The arm in
// DetectIdleFromOutput and determineStateAt is positive-idle only —
// it returns true when the TUI is at a prompt; it does not return
// true on "no prompt visible", which keeps a still-booting pane from
// being mistaken for a healthy one.
//
// The earlier `looksLikeIdle` heuristic can fall through to a
// state=idle verdict on very short output (a pre-existing bd-style
// fallback for known agent types), but the dispatch gate still
// refuses because the gate is the conjunction of state, freshness,
// confidence, and the no-classifier parser double-check. A
// confidence-0.25 mid-boot pane fails the gate at the confidence
// floor regardless of the state field, which is what this test
// pins.
func TestAgyMidBootStillRefusesDispatch(t *testing.T) {
	observedAt := time.Now().UTC()
	observer := newAgySessionObserver(observedAt, agyMidBootCapture)

	observation, err := observer.Observe(t.Context(), "demo")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	pane, ok := observation.PaneByID("%92")
	if !ok {
		t.Fatal("observed pane missing")
	}
	if pane.SafeToDispatch() {
		t.Fatalf("mid-boot agy pane SafeToDispatch = true, want false — the bd-my3 arm must not shortcut a pane that has not drawn its prompt")
	}
	// The dispatch gate fails the pane via confidence or state, not
	// silently. Assert the surfaced values are not dispatchable.
	if pane.Current.Confidence >= statuspkg.MinimumDispatchConfidence &&
		pane.Current.Status.State == statuspkg.StateIdle &&
		pane.Current.Freshness == statuspkg.FreshnessFresh {
		t.Fatalf("mid-boot pane is dispatchable on every axis: state=%s conf=%.2f freshness=%s — the gate refused something here",
			pane.Current.Status.State, pane.Current.Confidence, pane.Current.Freshness)
	}
}

// TestSpawnPromptDeliveryStatusFromAgyAndCodexMixedSession is the
// bd-my3 acceptance criterion case 2: spawn a mixed session where one
// pane is a healthy agy (the shakedown capture) and one pane is an
// ambiguous codex (mid-boot, no prompt). The per-pane readiness gate
// must clear the agy pane, time out the codex pane with a named
// failing-signal error, and the buildSpawnPromptDeliveryStatus
// aggregation must surface the split — Total=2, Delivered=1, Failed=1,
// with the codex pane in PaneErrors and the agy pane in the implicit
// Delivered count.
func TestSpawnPromptDeliveryStatusFromAgyAndCodexMixedSession(t *testing.T) {
	observedAt := time.Now().UTC()
	observer := newMixedReadinessObserver(observedAt, agyReadyFooterCapture, codexLowConfidenceCapture)

	// Run the per-pane readiness gates the way the spawn flow does.
	readyErr := waitForSpawnPaneReady(t.Context(), "demo", "%92", 5*time.Second, time.Millisecond, observer)
	if readyErr != nil {
		t.Fatalf("waitForSpawnPaneReady(%%92) = %v, want nil", readyErr)
	}
	unreadyErr := waitForSpawnPaneReady(t.Context(), "demo", "%646", 50*time.Millisecond, time.Millisecond, observer)
	if unreadyErr == nil {
		t.Fatal("waitForSpawnPaneReady(%646) = nil, want timeout for the ambiguous pane")
	}
	if !strings.Contains(unreadyErr.Error(), "failing signal:") {
		t.Fatalf("unready error = %v, want the bd-q2a named-signal timeout", unreadyErr)
	}

	// Build the spawn response's prompt_delivery status the way the
	// JSON-mode spawn flow does.
	status := buildSpawnPromptDeliveryStatus(2, []output.SpawnPromptDeliveryError{{
		PaneID:  "%646",
		Message: unreadyErr.Error(),
	}})
	if status == nil {
		t.Fatal("buildSpawnPromptDeliveryStatus(2, ...) = nil, want populated")
	}
	if status.Total != 2 {
		t.Errorf("Total = %d, want 2", status.Total)
	}
	if status.Delivered != 1 {
		t.Errorf("Delivered = %d, want 1 (agy pane cleared its gate)", status.Delivered)
	}
	if status.Failed != 1 {
		t.Errorf("Failed = %d, want 1 (codex pane timed out)", status.Failed)
	}
	if len(status.PaneErrors) != 1 || status.PaneErrors[0].PaneID != "%646" {
		t.Fatalf("PaneErrors = %+v, want one entry for %%646", status.PaneErrors)
	}
}

// TestVerifyBootStillHardFailsOnPartialReady guards the bd-my3
// partial contract's only opt-out: --verify-boot. The user
// explicitly asked for strict readiness, so a single ambiguous pane
// still hard-fails the spawn. We assert the opt-out is wired by
// checking that the function that powers the verify-boot path
// (waitForAgentsReadyWithObserver) still returns a non-nil error on
// a partial-ready session. The aggregate-mode error names the
// failing pane and its state/freshness/confidence; the per-pane
// failing-signal error lives in spawnReadinessError (covered by
// TestWaitForSpawnPaneReadyPerPanePartialContract).
func TestVerifyBootStillHardFailsOnPartialReady(t *testing.T) {
	observedAt := time.Now().UTC()
	observer := newMixedReadinessObserver(observedAt, agyReadyFooterCapture, codexLowConfidenceCapture)

	// waitForAgentsReadyWithObserver waits for all agent panes to be
	// ready. With one ready pane and one ambiguous pane it must time
	// out with the readiness error. The spawn flow wraps this in
	// outputError() only when opts.VerifyBoot is set; the default
	// partial-success contract bypasses outputError. This test is the
	// pointer to that wiring — the strict-mode error path is exercised
	// here, and the partial-mode wiring is in the spawn.go change.
	ready, err := waitForAgentsReadyWithObserver(
		t.Context(), "demo", 50*time.Millisecond, time.Millisecond, observer,
	)
	if err == nil {
		t.Fatal("waitForAgentsReadyWithObserver = nil, want timeout for the mixed-ready session")
	}
	if ready != 1 {
		t.Errorf("ready = %d, want 1 (the agy pane cleared its gate)", ready)
	}
	// Sanity: the error names the failing pane so the operator can
	// see which one tripped the strict gate.
	if !strings.Contains(err.Error(), "pane 646") {
		t.Errorf("error = %v, want it to name the failing pane (%%646 or \"pane 646\")", err)
	}
	if !strings.Contains(err.Error(), "is not ready") {
		t.Errorf("error = %v, want the readiness-issue message", err)
	}
}

// agyChevronBeyondFooterCapture is the production-shape capture the
// status-layer agy arm specifically cures: the chevron sits at the TOP
// of the visible scrollback with the memory/model footer and a long
// scrollback of working-keyword noise below it. The status line scan
// walks only the trailing 12 non-empty lines, so without the agy arm
// in DetectIdleFromOutput this fixture classifies as not-idle (the
// chevron is beyond the window, the working-keyword scrollback is in
// it). The arm's full-output multiline match is the only signal that
// catches the case.
const agyChevronBeyondFooterCapture = `>>>
running migration scripts
running tests
running tests
running tests
running tests
running tests
running tests
running tests
running tests
running tests
running tests
running tests
running tests
running tests
running tests
running tests
running tests
running tests
running tests
running tests
Memory: 312.4 MB used / 1.2 GB total
Model: gemini-2.5-pro (medium)
Ready for your next command`

// TestAgyIdleProductionShapeFixturePinsStatusArm pins the bd-my3
// status-layer agy arm against the production-shape fixture (chevron
// beyond the line scan's 12-line window). Without the arm, the line
// scan finds no chevron in the trailing 12 lines, and DetectIdleFromOutput
// returns false; observationConfidence would then drop to 0.5 and the
// dispatch gate would refuse every direct assign to a healthy agy pane.
// The fixture is the brief's reproduction case 1 in miniature: a visible
// prompt an operator can see, but the classifier must be told the arm
// fires regardless of how far the memory/model footer has pushed the
// chevron past the line scan window.
func TestAgyIdleProductionShapeFixturePinsStatusArm(t *testing.T) {
	observedAt := time.Now().UTC()
	detector := statuspkg.NewDetector()

	if !statuspkg.DetectIdleFromOutput(agyChevronBeyondFooterCapture, "agy") {
		t.Fatal("DetectIdleFromOutput(agy, chevron-beyond-window) = false, want true — the bd-my3 status arm must catch the chevron past the line scan's 12-line window")
	}

	res := detector.AnalyzeAt("%92", "demo__agy_92", "agy", agyChevronBeyondFooterCapture, observedAt.Add(-time.Minute), observedAt)
	if res.State != statuspkg.StateIdle {
		t.Fatalf("AnalyzeAt(agy, chevron-beyond-window) state = %s, want idle — the bd-my3 arm must catch the chevron past the line scan's 12-line window", res.State)
	}

	obs := newAgySessionObserver(observedAt, agyChevronBeyondFooterCapture)
	observation, err := obs.Observe(t.Context(), "demo")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	pane, ok := observation.PaneByID("%92")
	if !ok {
		t.Fatal("observed pane missing")
	}
	if pane.Current.Status.State != statuspkg.StateIdle {
		t.Fatalf("pane state = %s, want idle — without the bd-my3 arm, the line scan misses the chevron and observationConfidence drops the pane to 0.5, which the dispatch gate refuses as busy", pane.Current.Status.State)
	}
	if pane.Current.Confidence < statuspkg.MinimumDispatchConfidence {
		t.Fatalf("pane confidence = %.2f, want >= %.2f — without the arm, observationConfidence returns 0.5 for the state=idle-but-DetectIdleFromOutput=false mismatch and the gate refuses the assign", pane.Current.Confidence, statuspkg.MinimumDispatchConfidence)
	}
	if !pane.SafeToDispatch() {
		t.Fatalf("pane SafeToDispatch() = false on a production-shape idle agy pane; the assign path would print `pane is busy (state: %s), use --force to override`", pane.Current.Status.State)
	}
}

// newAgyHighVelocitySessionObserver builds a SessionObserver over a single
// agy pane whose LastActivity equals observedAt (high velocity). The
// default newAgySessionObserver pins LastActivity to one minute before
// observedAt (low velocity) so the fallback `if isAtPrompt &&
// isLowVelocity` chain returns StateIdle for the same fixture. The
// high-velocity variant forces that fallback off, so a StateIdle verdict
// depends entirely on the unified.go agy arm — a regression of the arm
// returns StateWorking instead, which is what the
// TestAgyIdleProductionShapeFixturePinsUnifiedDetectorArm assertion
// catches.
func newAgyHighVelocitySessionObserver(observedAt time.Time, capture string) *statuspkg.SessionObserver {
	detector := statuspkg.NewDetector()
	return statuspkg.NewSessionObserverWithDependencies(
		detector,
		statuspkg.DefaultSessionObserverConfig(detector.Config()),
		statuspkg.SessionObserverDependencies{
			ListPanes: func(context.Context, string) ([]tmux.PaneActivity, error) {
				return []tmux.PaneActivity{{
					Pane:         tmux.Pane{ID: "%92", Index: 92, Title: "demo__agy_92", Type: tmux.AgentAntigravity},
					LastActivity: observedAt,
				}}, nil
			},
			CapturePane: func(_ context.Context, _ string, _ int) (string, error) {
				return capture, nil
			},
			Now: func() time.Time { return observedAt },
		},
	)
}

// TestAgyIdleProductionShapeFixturePinsUnifiedDetectorArm is the bd-my3
// review follow-up: the unified.go agy arm (in determineStateAt) must
// be the only thing that returns StateIdle for a high-velocity
// production-shape fixture. With lastActivity == observedAt, the
// existing fallback `if isAtPrompt && isLowVelocity` does not fire
// (isLowVelocity is false), so a healthy pane the parser reports as
// working (chevron past the parser's 5-line idle window, with "running"
// keywords in scrollback) resolves to StateIdle ONLY when the
// unified.go agy arm fires.
//
// WITHOUT the unified.go arm: state=working (recent-activity
// short-circuit). The dispatch gate then refuses the pane.
//
// WITH the unified.go arm: state=idle. observationConfidence returns
// 0.95 because DetectIdleFromOutput (the patterns.go agy arm, kept) sees
// the chevron via agyTuiPromptShowing.
//
// This test binds specifically to the unified.go arm. The earlier
// TestAgyIdleProductionShapeFixturePinsStatusArm uses low velocity, so
// the fallback chain returns StateIdle there regardless of whether the
// unified.go arm is present.
func TestAgyIdleProductionShapeFixturePinsUnifiedDetectorArm(t *testing.T) {
	observedAt := time.Now().UTC()
	detector := statuspkg.NewDetector()

	// High velocity: lastActivity == observedAt, well within the 5-second
	// ActivityThreshold. Without the unified.go arm, the fallback chain
	// `if isAtPrompt && isLowVelocity` short-circuits at the recent-
	// activity guard and returns StateWorking, regardless of DetectIdle.
	res := detector.AnalyzeAt("%92", "demo__agy_92", "agy", agyChevronBeyondFooterCapture, observedAt, observedAt)
	if res.State != statuspkg.StateIdle {
		t.Fatalf("AnalyzeAt(agy, chevron-beyond-window, high-velocity) state = %s, want idle — without the unified.go agy arm, the recent-activity guard returns StateWorking even though the chevron is in the output", res.State)
	}

	// And the chain's downstream observation: with state=idle and
	// DetectIdleFromOutput=true (the patterns.go arm catches the chevron
	// via agyTuiPromptShowing), observationConfidence returns 0.95. The
	// dispatch gate then authorizes the assign.
	obs := newAgyHighVelocitySessionObserver(observedAt, agyChevronBeyondFooterCapture)
	observation, err := obs.Observe(t.Context(), "demo")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	pane, ok := observation.PaneByID("%92")
	if !ok {
		t.Fatal("observed pane missing")
	}
	if pane.Current.Status.State != statuspkg.StateIdle {
		t.Fatalf("pane state = %s, want idle — without the unified.go arm, a high-velocity production-shape pane the parser reports as working resolves to StateWorking and the gate refuses the assign", pane.Current.Status.State)
	}
	if pane.Current.Confidence < statuspkg.MinimumDispatchConfidence {
		t.Fatalf("pane confidence = %.2f, want >= %.2f — observationConfidence drops to 0.5 when state=idle but the patterns.go arm's chevron scan disagreed (this fixture exercises both arms)", pane.Current.Confidence, statuspkg.MinimumDispatchConfidence)
	}
	if !pane.SafeToDispatch() {
		t.Fatalf("SafeToDispatch = false on a healthy high-velocity agy pane; without the unified.go arm the state would be working and the gate would refuse")
	}
}
