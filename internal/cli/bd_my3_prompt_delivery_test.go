package cli

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/output"
)

// TestBuildSpawnPromptDeliveryStatus covers the per-pane prompt-delivery
// status the spawn response now carries (bd-my3). The brief requires the
// spawn to fail ONLY the ambiguous pane and report the rest in the
// response, so a stable shape that names each failing pane with its
// readiness signal is the contract the rest of the spawn (and consumers
// downstream) must work against.
func TestBuildSpawnPromptDeliveryStatus(t *testing.T) {
	t.Run("no launch no errors returns nil (no field on the wire)", func(t *testing.T) {
		if got := buildSpawnPromptDeliveryStatus(0, nil); got != nil {
			t.Fatalf("buildSpawnPromptDeliveryStatus(0, nil) = %+v, want nil", got)
		}
	})

	t.Run("all panes delivered, no errors", func(t *testing.T) {
		got := buildSpawnPromptDeliveryStatus(3, nil)
		if got == nil {
			t.Fatal("buildSpawnPromptDeliveryStatus(3, nil) = nil, want a populated status")
		}
		if got.Total != 3 || got.Delivered != 3 || got.Failed != 0 {
			t.Fatalf("status = %+v, want Total=3 Delivered=3 Failed=0", got)
		}
		if len(got.PaneErrors) != 0 {
			t.Fatalf("PaneErrors = %+v, want empty", got.PaneErrors)
		}
	})

	t.Run("partial: one of three panes timed out, two delivered", func(t *testing.T) {
		errs := []output.SpawnPromptDeliveryError{{
			PaneID:  "%646",
			Message: "timeout waiting for pane %646 to become ready; failing signal: confidence=0.50 (want >= 0.75)",
		}}
		got := buildSpawnPromptDeliveryStatus(3, errs)
		if got == nil {
			t.Fatal("buildSpawnPromptDeliveryStatus(3, errs) = nil, want a populated status")
		}
		if got.Total != 3 {
			t.Errorf("Total = %d, want 3", got.Total)
		}
		if got.Delivered != 2 {
			t.Errorf("Delivered = %d, want 2", got.Delivered)
		}
		if got.Failed != 1 {
			t.Errorf("Failed = %d, want 1", got.Failed)
		}
		if len(got.PaneErrors) != 1 || got.PaneErrors[0].PaneID != "%646" {
			t.Fatalf("PaneErrors = %+v, want one entry for %%646", got.PaneErrors)
		}
		if !strings.Contains(got.PaneErrors[0].Message, "failing signal: confidence=0.50 (want >= 0.75)") {
			t.Errorf("PaneErrors[0].Message = %q, want the readiness signal verbatim", got.PaneErrors[0].Message)
		}
	})

	t.Run("all panes failed: status still surfaces, brief's contract", func(t *testing.T) {
		// bd-my3 acceptance: "exit code reflecting partial success — never
		// an error exit with all panes alive and untagged". An all-failed
		// status is still partial success: the session is alive, the
		// operator can retry the failing panes. The status is the same
		// shape, with Delivered=0 and Failed=Total.
		errs := []output.SpawnPromptDeliveryError{
			{PaneID: "%1", Message: "timeout"},
			{PaneID: "%2", Message: "timeout"},
		}
		got := buildSpawnPromptDeliveryStatus(2, errs)
		if got == nil || got.Total != 2 || got.Delivered != 0 || got.Failed != 2 {
			t.Fatalf("status = %+v, want Total=2 Delivered=0 Failed=2", got)
		}
	})
}
