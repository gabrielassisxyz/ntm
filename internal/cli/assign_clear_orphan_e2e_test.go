package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
)

func TestAssignReconcileRetiresClosedTrackerBeadEndToEnd(t *testing.T) {
	isolateSessionAgentStorage(t)
	realBR, err := exec.LookPath("br")
	if err != nil {
		t.Skip("br is required for assignment reconciliation coverage")
	}

	projectDir := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet"}, {"create", "--title", "Reconcile closed work", "--type", "task", "--priority", "2", "--json"}} {
		command := exec.Command(realBR, args...)
		command.Dir = projectDir
		if output, runErr := command.CombinedOutput(); runErr != nil {
			t.Fatalf("br %s: %v\n%s", strings.Join(args, " "), runErr, output)
		} else if len(args) > 0 && args[0] == "create" {
			var created struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(output, &created); err != nil || created.ID == "" {
				t.Fatalf("parse br create output: err=%v output=%s", err, output)
			}
			beadID := created.ID
			claim := exec.Command(realBR, "update", beadID, "--assignee", "CobaltLake", "--status", "in_progress")
			claim.Dir = projectDir
			if claimOutput, claimErr := claim.CombinedOutput(); claimErr != nil {
				t.Fatalf("claim scratch bead: %v\n%s", claimErr, claimOutput)
			}

			const session = "reconcile-closed-e2e"
			store := assignment.NewStore(session)
			if _, err := store.Assign(beadID, "Reconcile closed work", 1, "codex", "CobaltLake", "work"); err != nil {
				t.Fatalf("seed durable assignment: %v", err)
			}
			store.Assignments[beadID].ClaimActor = "CobaltLake"
			if err := store.Save(); err != nil {
				t.Fatalf("save durable assignment: %v", err)
			}

			close := exec.Command(realBR, "close", beadID, "--reason", "completed in scratch session")
			close.Dir = projectDir
			if closeOutput, closeErr := close.CombinedOutput(); closeErr != nil {
				t.Fatalf("close scratch bead: %v\n%s", closeErr, closeOutput)
			}

			var output bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			cmd.SetOut(&output)
			if err := runReconcileAssignments(cmd, session, projectDir); err != nil {
				t.Fatalf("reconcile closed assignment: %v", err)
			}
			if !strings.Contains(output.String(), "retired 1 stale assignment") {
				t.Fatalf("reconcile output=%q", output.String())
			}
			retired, err := assignment.LoadStoreStrict(session)
			if err != nil {
				t.Fatalf("load reconciled assignment store: %v", err)
			}
			row := retired.Get(beadID)
			if row == nil || row.Status != assignment.StatusRetired || row.RetiredAt == nil {
				t.Fatalf("closed tracker row was not durably retired: %+v", row)
			}
		}
	}
}

// TestAssignClearRecoversOrphanedClaimEndToEnd scripts the bd-1zn
// reproduction against a real scratch Beads tracker: ntm's real claim port
// lands the claim, the dispatch then fails the way delivery into a pane still
// in its boot window does (guaranteed-no-actuation after the claim), the
// durable assignment record is dropped, and the documented recovery
// `ntm assign --clear <bead>` alone returns the bead to
// `br ready --unassigned`. The other-actor case is proven untouched in the
// same run. Requires real br; skips otherwise.
func TestAssignClearRecoversOrphanedClaimEndToEnd(t *testing.T) {
	isolateSessionAgentStorage(t)
	realBR, err := exec.LookPath("br")
	if err != nil {
		t.Skip("br is required for orphaned-claim recovery coverage")
	}

	projectDir := t.TempDir()
	orphanBeadID := ""
	foreignBeadID := ""
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"create", "--title", "Orphaned recovery", "--type", "task", "--priority", "2", "--json"},
		{"create", "--title", "Foreign claim", "--type", "task", "--priority", "2", "--json"},
	} {
		cmd := exec.Command(realBR, args...)
		cmd.Dir = projectDir
		output, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("br %s: %v\n%s", strings.Join(args, " "), runErr, output)
		}
		if args[0] == "create" {
			var created struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(output, &created); err != nil || created.ID == "" {
				t.Fatalf("parse br create output: err=%v output=%s", err, output)
			}
			if orphanBeadID == "" {
				orphanBeadID = created.ID
			} else {
				foreignBeadID = created.ID
			}
		}
	}
	foreignClaim := exec.Command(realBR, "update", foreignBeadID, "--assignee", "EmeraldCat", "--status", "in_progress")
	foreignClaim.Dir = projectDir
	if output, runErr := foreignClaim.CombinedOutput(); runErr != nil {
		t.Fatalf("seed foreign claim: %v\n%s", runErr, output)
	}

	previousRepo := assignRepoPath
	t.Cleanup(func() { assignRepoPath = previousRepo })
	assignRepoPath = projectDir

	const session = "orphan-recovery-e2e"
	store := assignment.NewStore(session)
	if err := store.Save(); err != nil {
		t.Fatalf("save empty fixture store: %v", err)
	}

	// The same claim port newCLIAtomicAssignmentCoordinator wires, pointed at
	// the scratch tracker, so the claim in the tracker is ntm-created for real.
	operatorGatedLabels := bv.OperatorGatedLabelsForProject(projectDir)
	claimPort := assignment.ClaimFunc(func(ctx context.Context, claimBeadID, actor string) (assignment.ClaimReceipt, error) {
		claim, err := bv.ClaimBeadForAssignmentWithOperatorGatedLabels(ctx, projectDir, claimBeadID, actor, operatorGatedLabels)
		if err != nil {
			return assignment.ClaimReceipt{}, err
		}
		return assignment.ClaimReceipt{
			BeadID:    claim.ID,
			Actor:     claim.Actor,
			Status:    claim.Status,
			ClaimedAt: claim.ClaimedAt,
		}, nil
	})
	// The boot-window delivery failure from the measured incident: the claim
	// has landed, then the dispatch refuses because the pane is not freshly
	// and confidently idle — ntm can prove nothing reached the pane.
	dispatchPort := assignment.DispatchFunc(func(_ context.Context, req assignment.DispatchRequest) (assignment.DispatchReceipt, error) {
		return assignment.DispatchReceipt{}, assignment.GuaranteeNoActuation(
			fmt.Errorf("pane %s is not freshly and confidently idle at dispatch", req.Target),
		)
	})
	coordinator := assignment.NewAtomicCoordinator(store, claimPort, nil, dispatchPort)

	idempotencyKey, err := assignment.NewAssignmentIdempotencyKey()
	if err != nil {
		t.Fatalf("generate idempotency key: %v", err)
	}
	_, executeErr := coordinator.Execute(context.Background(), assignment.AtomicRequest{
		BeadID:         orphanBeadID,
		BeadTitle:      "Orphaned recovery",
		Target:         "%7",
		OccupancyKey:   "%7",
		Pane:           7,
		AgentType:      "claude",
		AgentName:      "GreenLake",
		Actor:          "GreenLake",
		Prompt:         "Work on bead " + orphanBeadID,
		IdempotencyKey: idempotencyKey,
	})
	if executeErr == nil {
		t.Fatal("dispatch unexpectedly succeeded; the reproduction needs a post-claim delivery failure")
	}
	if !strings.Contains(executeErr.Error(), "dispatch") {
		t.Fatalf("Execute failed before the claim (reproduction not reached): %v", executeErr)
	}

	claimCtx, claimCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer claimCancel()
	details, err := bv.GetBeadAssignmentDetailsContext(claimCtx, projectDir, orphanBeadID)
	if err != nil {
		t.Fatalf("read claimed bead from tracker: %v", err)
	}
	if details.Status != "in_progress" || !assignment.IsStableClaimActor(details.Assignee) {
		t.Fatalf("tracker after failed dispatch does not hold an ntm claim: status=%q assignee=%q", details.Status, details.Assignee)
	}
	durable := store.Get(orphanBeadID)
	if durable == nil || assignment.EffectiveClaimState(durable) != assignment.ClaimClaimed {
		t.Fatalf("failed dispatch did not keep a claimed durable record: %+v", durable)
	}
	if note := atomicDispatchFailureClaimNote(durable, session, orphanBeadID); note == "" {
		t.Fatal("post-claim dispatch failure produced no claim-kept note")
	}
	// "expired or dropped": remove the durable record, leaving only the br claim.
	if err := store.Remove(orphanBeadID); err != nil {
		t.Fatalf("drop durable record: %v", err)
	}

	previousJSON := jsonOutput
	previousLeases := releaseAssignmentLeases
	t.Cleanup(func() {
		jsonOutput = previousJSON
		releaseAssignmentLeases = previousLeases
	})
	jsonOutput = true
	releaseAssignmentLeases = func(context.Context, string, *assignment.Assignment) ([]string, error) { return nil, nil }
	clearOutput, clearErr := captureStdout(t, func() error {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		return runClearSpecificBeads(cmd, session, orphanBeadID)
	})
	var clearEnvelope ClearAssignmentsEnvelope
	if err := json.Unmarshal([]byte(clearOutput), &clearEnvelope); err != nil {
		t.Fatalf("decode clear envelope: %v\noutput=%s", err, clearOutput)
	}
	if clearErr != nil || !clearEnvelope.Success || clearEnvelope.Data == nil || len(clearEnvelope.Data.Cleared) != 1 {
		t.Fatalf("documented recovery failed: err=%v envelope=%+v", clearErr, clearEnvelope)
	}
	cleared := clearEnvelope.Data.Cleared[0]
	if !cleared.Success || cleared.ReleasedVia != clearReleasedViaOrphanedBeadsClaim {
		t.Fatalf("recovery did not report the orphaned-claim path: %+v", cleared)
	}

	details, err = bv.GetBeadAssignmentDetailsContext(claimCtx, projectDir, orphanBeadID)
	if err != nil {
		t.Fatalf("read released bead from tracker: %v", err)
	}
	if details.Status != "open" || details.Assignee != "" {
		t.Fatalf("bead not back to ready/unassigned after --clear: status=%q assignee=%q", details.Status, details.Assignee)
	}
	readyCmd := exec.Command(realBR, "ready", "--unassigned", "--json")
	readyCmd.Dir = projectDir
	readyOutput, err := readyCmd.Output()
	if err != nil {
		t.Fatalf("br ready --unassigned: %v", err)
	}
	if !strings.Contains(string(readyOutput), fmt.Sprintf("%q", orphanBeadID)) {
		t.Fatalf("bead missing from br ready --unassigned: %s", readyOutput)
	}

	foreignClearOutput, foreignClearErr := captureStdout(t, func() error {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		return runClearSpecificBeads(cmd, session, foreignBeadID)
	})
	if foreignClearErr == nil {
		t.Fatal("clear of another actor's claim unexpectedly succeeded")
	}
	var foreignEnvelope ClearAssignmentsEnvelope
	if err := json.Unmarshal([]byte(foreignClearOutput), &foreignEnvelope); err != nil {
		t.Fatalf("decode foreign clear envelope: %v\noutput=%s", err, foreignClearOutput)
	}
	if foreignEnvelope.Success || foreignEnvelope.Data == nil || foreignEnvelope.Data.Summary.ClearedCount != 0 {
		t.Fatalf("foreign-claim clear envelope = %+v", foreignEnvelope)
	}
	if got := foreignEnvelope.Data.Cleared[0].Error; got != "assignment not found or already completed" {
		t.Fatalf("foreign-claim result error = %q", got)
	}
	foreignDetails, err := bv.GetBeadAssignmentDetailsContext(claimCtx, projectDir, foreignBeadID)
	if err != nil {
		t.Fatalf("read foreign bead from tracker: %v", err)
	}
	if foreignDetails.Status != "in_progress" || foreignDetails.Assignee != "EmeraldCat" {
		t.Fatalf("foreign actor's claim was disturbed: status=%q assignee=%q", foreignDetails.Status, foreignDetails.Assignee)
	}
}
