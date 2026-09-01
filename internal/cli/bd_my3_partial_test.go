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

// agyReadyFooterCapture is the shakedown capture of an Antigravity pane
// at its prompt with the memory/model footer pushing the chevron past
// the parser's 5-line idle window. bd-my3 imports it from the bd-q2a
// reproduction file. It is the canonical "this pane is healthy and
// ready for prompt delivery" capture.
const agyReadyFooterCapture = agyIdleFooterCapture

// newMixedReadinessObserver builds a SessionObserver over two panes: a
// healthy agy pane at its prompt (the shakedown capture) and an
// unhealthy pane stuck mid-boot. The brief's reproduction case 2 is
// exactly this shape — one ambiguous pane, one healthy pane, the
// session sitting in the same tmux session — and the test exercises
// the bd-my3 partial-success contract on it: the readiness gate is
// per-pane, the healthy pane gets its prompt, the unhealthy pane is
// reported in SpawnPromptDeliveryStatus.PaneErrors, and the spawn
// response carries the per-pane outcome rather than a single hard
// error.
func newMixedReadinessObserver(observedAt time.Time, readyCapture, unreadyCapture string) *statuspkg.SessionObserver {
	detector := statuspkg.NewDetector()
	return statuspkg.NewSessionObserverWithDependencies(
		detector,
		statuspkg.DefaultSessionObserverConfig(detector.Config()),
		statuspkg.SessionObserverDependencies{
			ListPanes: func(context.Context, string) ([]tmux.PaneActivity, error) {
				return []tmux.PaneActivity{
					{
						Pane:         tmux.Pane{ID: "%92", Index: 92, Title: "demo__agy_92", Type: tmux.AgentAntigravity},
						LastActivity: observedAt.Add(-time.Minute),
					},
					{
						Pane:         tmux.Pane{ID: "%646", Index: 646, Title: "demo__cod_646", Type: tmux.AgentCodex},
						LastActivity: observedAt.Add(-time.Minute),
					},
				}, nil
			},
			CapturePane: func(_ context.Context, target string, _ int) (string, error) {
				switch target {
				case "%92":
					return readyCapture, nil
				case "%646":
					return unreadyCapture, nil
				default:
					return "", nil
				}
			},
			Now: func() time.Time { return observedAt },
		},
	)
}

// codexLowConfidenceCapture is a codex pane stuck mid-boot with no
// prompt visible. The exact failing signal in the shakedown was
// `failing signal: confidence=0.50 (want >= 0.75)` (bd-my3
// reproduction case 2); the test only needs the timeout to fire with
// a named-signal error so the partial-success contract has something
// to surface.
const codexLowConfidenceCapture = `loading models
compacting context
waiting for upstream
`

// TestWaitForSpawnPaneReadyPerPanePartialContract exercises the bd-my3
// per-pane readiness split: a session with one ready pane and one
// ambiguous pane must let the ready pane through waitForSpawnPaneReady
// while the ambiguous pane's wait times out with a named failing-signal
// error. The aggregation in buildSpawnPromptDeliveryStatus then turns
// the per-pane outcomes into a single status the spawn response can
// carry, instead of a single hard error that fails the whole spawn.
func TestWaitForSpawnPaneReadyPerPanePartialContract(t *testing.T) {
	observedAt := time.Now().UTC()
	observer := newMixedReadinessObserver(observedAt, agyReadyFooterCapture, codexLowConfidenceCapture)

	// The healthy pane: waitForSpawnPaneReady must return nil before
	// the deadline. This is the bd-my3 reproduction case 1 in
	// miniature — an agy pane at its prompt, no --force required.
	readyErr := waitForSpawnPaneReady(t.Context(), "demo", "%92", 5*time.Second, time.Millisecond, observer)
	if readyErr != nil {
		t.Fatalf("waitForSpawnPaneReady(%%92) = %v, want nil for a ready agy pane", readyErr)
	}

	// The ambiguous pane: waitForSpawnPaneReady must time out with a
	// named failing-signal error (bd-q2a), not a bare "timeout" or a
	// false telemetry reading. The brief's case-2 reproduction.
	unreadyErr := waitForSpawnPaneReady(t.Context(), "demo", "%646", 50*time.Millisecond, time.Millisecond, observer)
	if unreadyErr == nil {
		t.Fatal("waitForSpawnPaneReady(%646) = nil, want timeout for the ambiguous pane")
	}
	if !strings.Contains(unreadyErr.Error(), "failing signal:") {
		t.Fatalf("unready error = %v, want the named-signal timeout from bd-q2a", unreadyErr)
	}

	// The aggregation the spawn response carries: the ready pane is
	// counted in Delivered, the unready pane is in PaneErrors with
	// the verbatim readiness message so the operator can read the
	// failing signal without re-polling the pane.
	status := buildSpawnPromptDeliveryStatus(2, []output.SpawnPromptDeliveryError{{
		PaneID:  "%646",
		Message: unreadyErr.Error(),
	}})
	if status == nil {
		t.Fatal("buildSpawnPromptDeliveryStatus(2, [%%646 err]) = nil, want populated partial-failure status")
	}
	if status.Total != 2 {
		t.Errorf("Total = %d, want 2", status.Total)
	}
	if status.Delivered != 1 {
		t.Errorf("Delivered = %d, want 1 (the agy pane cleared its readiness gate)", status.Delivered)
	}
	if status.Failed != 1 {
		t.Errorf("Failed = %d, want 1 (the codex pane timed out)", status.Failed)
	}
	if len(status.PaneErrors) != 1 || status.PaneErrors[0].PaneID != "%646" {
		t.Fatalf("PaneErrors = %+v, want one entry for %%646", status.PaneErrors)
	}
	if !strings.Contains(status.PaneErrors[0].Message, "failing signal:") {
		t.Errorf("PaneErrors[0].Message = %q, want the readiness signal verbatim", status.PaneErrors[0].Message)
	}
}
