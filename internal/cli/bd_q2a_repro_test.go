package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// agyIdleFooterCapture models an Antigravity pane at its prompt (bd-q2a): the
// ">" prompt line sits several lines up from the bottom while the TUI draws a
// footer (memory display etc.) below it, and the scrollback retains
// working-ish words like "running" within the parser's 20-line working
// window. The state telemetry's 12-line scan with the activeWorkBelow guard
// calls this idle; the parser's 5-line idle window misses the prompt and its
// working scan fires on "running migration scripts". Before bd-q2a the
// readiness gate waited on the parser verdict, so it timed out on a pane its
// own telemetry called ready.
const agyIdleFooterCapture = `gemini-2.5-pro /model | 396.8 MB

Antigravity connected

Task completed successfully.

running migration scripts
analyzing schema

What would you like next?

>

Memory: 312.4 MB used / 1.2 GB total
Model: gemini-2.5-pro (medium)
Ready for your next command`

// agyMidBootCapture models an Antigravity pane still coming up: boot chatter,
// no prompt yet. No prompt line is visible anywhere in the window, so the
// telemetry must refuse it.
const agyMidBootCapture = `Starting Antigravity CLI...

Loading workspace configuration
Connecting to Google AI Studio
Authenticating...
`

// newAgySessionObserver builds a SessionObserver pinned to a single agy pane,
// so the full chain — title-derived Pane.Type, determineStateAt's agy arm,
// observationConfidence, and the readiness gate — is exercised over a real
// capture rather than a hand-built PaneObservation.
func newAgySessionObserver(observedAt time.Time, capture string) *statuspkg.SessionObserver {
	detector := statuspkg.NewDetector()
	return statuspkg.NewSessionObserverWithDependencies(
		detector,
		statuspkg.DefaultSessionObserverConfig(detector.Config()),
		statuspkg.SessionObserverDependencies{
			ListPanes: func(context.Context, string) ([]tmux.PaneActivity, error) {
				return []tmux.PaneActivity{{
					Pane:         tmux.Pane{ID: "%92", Index: 92, Title: "demo__agy_2", Type: tmux.AgentAntigravity},
					LastActivity: observedAt.Add(-time.Minute),
				}}, nil
			},
			CapturePane: func(_ context.Context, _ string, _ int) (string, error) {
				return capture, nil
			},
			Now: func() time.Time { return observedAt },
		},
	)
}

// TestSpawnPaneObservationSafeToDispatchDecisionTable is the gate's decision
// table over (state, freshness, confidence, classifier-present), including the
// no-classifier row (bd-q2a): a no-classifier pane whose own telemetry says
// idle/fresh/confidence >= 0.75 is dispatchable by the gate even when the
// parser's narrower classifier disagrees, while every genuinely unready row
// still refuses.
func TestSpawnPaneObservationSafeToDispatchDecisionTable(t *testing.T) {
	now := time.Now().UTC()
	agyPane := tmux.Pane{ID: "%92", WindowIndex: 0, Index: 92, Type: tmux.AgentAntigravity, Title: "demo__agy_92"}
	ccPane := tmux.Pane{ID: "%1", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude, Title: "demo__cc_1"}

	cases := []struct {
		name   string
		pane   tmux.Pane
		state  statuspkg.AgentState
		raw    string // overrides the helper default
		mutate func(p *statuspkg.PaneObservation)
		want   bool
	}{
		{
			name:  "no-classifier idle/fresh/0.95 with parser-disagreeing output is dispatchable",
			pane:  agyPane,
			state: statuspkg.StateIdle,
			raw:   agyIdleFooterCapture, // parser says idle=false working=true; the telemetry says ready
			want:  true,
		},
		{
			name:  "no-classifier mid-boot unknown state is refused",
			pane:  agyPane,
			state: statuspkg.StateUnknown,
			raw:   agyMidBootCapture,
			want:  false,
		},
		{
			name:  "no-classifier stale freshness is refused",
			pane:  agyPane,
			state: statuspkg.StateIdle,
			raw:   agyIdleFooterCapture,
			mutate: func(p *statuspkg.PaneObservation) {
				p.Current.Freshness = statuspkg.FreshnessStale
			},
			want: false,
		},
		{
			name:  "no-classifier confidence below the dispatch floor is refused",
			pane:  agyPane,
			state: statuspkg.StateIdle,
			raw:   agyIdleFooterCapture,
			mutate: func(p *statuspkg.PaneObservation) {
				p.Current.Confidence = 0.5
			},
			want: false,
		},
		{
			name:  "no-classifier capture failure is refused",
			pane:  agyPane,
			state: statuspkg.StateIdle,
			raw:   "",
			mutate: func(p *statuspkg.PaneObservation) {
				p.Current.Error = "capture pipe closed"
			},
			want: false,
		},
		{
			name:  "classifier type (cc) idle with agreeing parser verdict is dispatchable",
			pane:  ccPane,
			state: statuspkg.StateIdle,
			raw:   "Claude Code v0.0.0\n❯ ",
			want:  true,
		},
		{
			// Mirror of TestSpawnPaneObservationSafeToDispatchPi's negative
			// direction: a classifier type keeps the parser double-check, so a
			// telemetry-green pane the parser calls working is still refused.
			name:  "classifier type (pi) idle telemetry but parser-call working is refused",
			pane:  tmux.Pane{ID: "%5", WindowIndex: 0, Index: 5, Type: tmux.AgentPi, Title: "demo__pi_5"},
			state: statuspkg.StateIdle,
			raw:   piWorkingCapture,
			want:  false,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			pane := testSpawnPaneObservation(now, tt.pane, tt.state)
			if tt.raw != "" {
				pane.RawOutput = tt.raw
			}
			if tt.mutate != nil {
				tt.mutate(&pane)
			}
			if got := spawnPaneObservationSafeToDispatch(pane); got != tt.want {
				t.Fatalf("spawnPaneObservationSafeToDispatch = %v, want %v (state=%s freshness=%s confidence=%.2f)",
					got, tt.want, pane.Current.Status.State, pane.Current.Freshness, pane.Current.Confidence)
			}
		})
	}
}

// TestWaitForSpawnPaneReadyAgyDelivers is the "clean spawn with an agy lane,
// delivery asserted" integration criterion driven through the same seams the
// pi readiness tests use: a real observer over a real capture, the readiness
// poll, and the spawn prompt sequence. The idle agy pane must become
// dispatchable before the deadline and the prompt must reach it.
func TestWaitForSpawnPaneReadyAgyDelivers(t *testing.T) {
	observedAt := time.Now().UTC()
	observer := newAgySessionObserver(observedAt, agyIdleFooterCapture)

	observation, err := observer.Observe(context.Background(), "demo")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	pane, ok := observation.PaneByID("%92")
	if !ok {
		t.Fatal("observed pane missing")
	}
	// The telemetry the spawn timeout error prints for this pane.
	if pane.Current.Status.State != statuspkg.StateIdle || pane.Current.Confidence < statuspkg.MinimumDispatchConfidence {
		t.Fatalf("fixture telemetry = state=%s confidence=%.2f, want idle >= %.2f",
			pane.Current.Status.State, pane.Current.Confidence, statuspkg.MinimumDispatchConfidence)
	}

	start := time.Now()
	err = waitForSpawnPaneReady(t.Context(), "demo", "%92", 5*time.Second, time.Millisecond, observer)
	if err != nil {
		t.Fatalf("waitForSpawnPaneReady = %v, want nil for a pane its own telemetry calls ready", err)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("waitForSpawnPaneReady took %v, want return before deadline", elapsed)
	}

	dispatcher := &recordingSpawnDispatcher{}
	steps := []spawnPromptStep{
		{Kind: "recovery_context", Message: "recover this session"},
		{Kind: "user_prompt", Message: "do the work"},
	}
	receipts, err := dispatchSpawnPromptSequence(
		t.Context(), "demo", "%92", steps, observer, dispatcher, 5*time.Second, time.Millisecond,
	)
	if err != nil {
		t.Fatalf("dispatchSpawnPromptSequence = %v, want prompt delivered to the agy pane", err)
	}
	if len(receipts) != len(steps) {
		t.Fatalf("receipts = %d, want %d", len(receipts), len(steps))
	}
	for i, receipt := range receipts {
		if receipt.Status != dispatchsvc.ReceiptDelivered || receipt.Target.Ref.StableKey() != "%92" {
			t.Fatalf("receipt[%d] = %+v, want delivered to pane %%92", i, receipt)
		}
	}
	if got := strings.Join(dispatcher.messages, ","); got != "recover this session,do the work" {
		t.Fatalf("delivered messages = %q, want both steps in order", got)
	}
}

// TestWaitForSpawnPaneReadyUnreadyAgyTimesOutWithNamedSignal is the forced
// unready direction of bd-q2a: a mid-boot agy pane (no prompt yet) must still
// time out, and the timeout must name the failing signal with its value and
// threshold instead of a bare "timeout waiting for pane".
func TestWaitForSpawnPaneReadyUnreadyAgyTimesOutWithNamedSignal(t *testing.T) {
	observedAt := time.Now().UTC()
	observer := newAgySessionObserver(observedAt, agyMidBootCapture)

	err := waitForSpawnPaneReady(t.Context(), "demo", "%92", 10*time.Millisecond, time.Millisecond, observer)
	if err == nil {
		t.Fatal("waitForSpawnPaneReady = nil, want timeout for a mid-boot pane")
	}
	if !strings.Contains(err.Error(), "timeout waiting for pane") {
		t.Fatalf("error = %v, want readiness timeout", err)
	}
	// The mid-boot pane must be refused — the classifier may name state or
	// confidence depending on its heuristics, but either way the error must
	// say which signal failed and the threshold it missed, never a bare
	// timeout or a telemetry that claims the pane was ready.
	if !strings.Contains(err.Error(), "failing signal:") || !strings.Contains(err.Error(), "(want") {
		t.Fatalf("error = %v, want named failing signal with its threshold", err)
	}
	if strings.Contains(err.Error(), "state=idle freshness=fresh confidence=1") ||
		(strings.Contains(err.Error(), "state=idle") && strings.Contains(err.Error(), "confidence=0.95")) {
		t.Fatalf("error = %v, must not claim the pane was ready", err)
	}
}

// TestSpawnReadinessErrorNamesFailingSignal asserts the timeout error carries
// which signal refused delivery, its observed value, and its threshold, for
// each refusal vector the gate can hit.
func TestSpawnReadinessErrorNamesFailingSignal(t *testing.T) {
	now := time.Now().UTC()
	pane := tmux.Pane{ID: "%7", WindowIndex: 0, Index: 7, Type: tmux.AgentAntigravity, Title: "demo__agy_7"}
	base := testSpawnPaneObservation(now, pane, statuspkg.StateIdle)
	base.RawOutput = agyIdleFooterCapture

	cases := []struct {
		name   string
		mutate func(observation *statuspkg.SessionObservation)
		want   string
	}{
		{
			name: "mid-boot state names state and the idle threshold",
			mutate: func(o *statuspkg.SessionObservation) {
				p, _ := o.PaneByID("%7")
				p.Current.Status.State = statuspkg.StateUnknown
				p.Current.Confidence = 0.25
				o.Panes[0] = p
			},
			want: "failing signal: state=unknown (want idle)",
		},
		{
			name: "stale freshness names freshness",
			mutate: func(o *statuspkg.SessionObservation) {
				p, _ := o.PaneByID("%7")
				p.Current.Freshness = statuspkg.FreshnessStale
				o.Panes[0] = p
			},
			want: "failing signal: freshness=stale (want fresh)",
		},
		{
			name: "weak confidence names value and threshold",
			mutate: func(o *statuspkg.SessionObservation) {
				p, _ := o.PaneByID("%7")
				p.Current.Confidence = 0.5
				o.Panes[0] = p
			},
			want: fmt.Sprintf("failing signal: confidence=0.50 (want >= %.2f)", statuspkg.MinimumDispatchConfidence),
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			observation := testSpawnSessionObservation(now, base)
			tt.mutate(&observation)
			err := spawnReadinessError("timeout waiting for pane %7 to become ready", observation, nil, "%7")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("spawnReadinessError = %v, want %q in error", err, tt.want)
			}
		})
	}
}
