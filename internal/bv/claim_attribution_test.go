package bv

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	assignmentstore "github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/sqliteutil"
)

// claimEventAttribution is what a test reads back from one events row.
type claimEventAttribution struct {
	agentName sql.NullString
	harness   sql.NullString
	model     sql.NullString
}

func readLatestClaimEventAttribution(t *testing.T, databasePath, beadID string) claimEventAttribution {
	t.Helper()
	database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
	if err != nil {
		t.Fatalf("open Beads database: %v", err)
	}
	defer database.Close()
	var got claimEventAttribution
	err = database.QueryRow(
		`SELECT agent_name, harness, model FROM events WHERE issue_id = ? ORDER BY rowid DESC LIMIT 1`,
		beadID,
	).Scan(&got.agentName, &got.harness, &got.model)
	if err != nil {
		t.Fatalf("read latest claim event for %s: %v", beadID, err)
	}
	return got
}

// TestInsertGuardedClaimEventAttributionFallsBackPerField proves each of the
// three attribution columns is resolved independently: an item that carries
// only some of the three fields must not lose the ones the environment still
// has, and a test that only exercised the item-present path would pass even
// if that per-field fallback were deleted.
func TestInsertGuardedClaimEventAttributionFallsBackPerField(t *testing.T) {
	requireRealBR(t)
	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	beadID := createRealBRBead(t, dir, "attribution fallback target")
	databasePath := realBRDatabasePath(t, dir)

	t.Setenv("BR_AGENT_NAME", "env-agent-name")
	t.Setenv("BR_HARNESS", "env-harness")
	t.Setenv("BR_MODEL", "env-model")

	// The item carries agent_name and model but leaves harness blank, so
	// harness alone must fall back to the environment while the other two
	// keep the item's own value.
	ctx := assignmentstore.WithClaimAttribution(t.Context(), assignmentstore.ClaimAttribution{
		AgentName: "item-agent-name",
		Harness:   "",
		Model:     "item-model",
	})

	database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
	if err != nil {
		t.Fatalf("open Beads database: %v", err)
	}
	defer database.Close()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := insertGuardedClaimEvent(ctx, tx, beadID, "status_changed", "TestActor", "open", "in_progress", time.Now().UTC()); err != nil {
		t.Fatalf("insertGuardedClaimEvent: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	got := readLatestClaimEventAttribution(t, databasePath, beadID)
	if !got.agentName.Valid || got.agentName.String != "item-agent-name" {
		t.Fatalf("agent_name = %+v, want item value %q", got.agentName, "item-agent-name")
	}
	if !got.harness.Valid || got.harness.String != "env-harness" {
		t.Fatalf("harness = %+v, want environment fallback %q", got.harness, "env-harness")
	}
	if !got.model.Valid || got.model.String != "item-model" {
		t.Fatalf("model = %+v, want item value %q", got.model, "item-model")
	}
}

// TestInsertGuardedClaimEventAttributionFallsBackToEnvironmentWithNoItem
// covers the opposite composition from the test above: no attribution on ctx
// at all (the shape every caller outside the Beads assignment claim uses
// today) must reproduce exactly the pre-existing, environment-only recording.
func TestInsertGuardedClaimEventAttributionFallsBackToEnvironmentWithNoItem(t *testing.T) {
	requireRealBR(t)
	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	beadID := createRealBRBead(t, dir, "no attribution target")
	databasePath := realBRDatabasePath(t, dir)

	t.Setenv("BR_AGENT_NAME", "env-only-agent")
	t.Setenv("BR_HARNESS", "env-only-harness")
	t.Setenv("BR_MODEL", "env-only-model")

	ctx := t.Context()
	database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
	if err != nil {
		t.Fatalf("open Beads database: %v", err)
	}
	defer database.Close()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := insertGuardedClaimEvent(ctx, tx, beadID, "status_changed", "TestActor", "open", "in_progress", time.Now().UTC()); err != nil {
		t.Fatalf("insertGuardedClaimEvent: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	got := readLatestClaimEventAttribution(t, databasePath, beadID)
	if got.agentName.String != "env-only-agent" || got.harness.String != "env-only-harness" || got.model.String != "env-only-model" {
		t.Fatalf("attribution = %+v, want every field from the environment", got)
	}
}

// TestInsertGuardedClaimEventEmptyModelRecordsNullNotEmptyString proves an
// item that explicitly carries no model (and an environment with none
// either) records SQL NULL, not the empty string a naive implementation
// would bind. A consumer needs to tell "not known" from "known to be blank".
func TestInsertGuardedClaimEventEmptyModelRecordsNullNotEmptyString(t *testing.T) {
	requireRealBR(t)
	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	beadID := createRealBRBead(t, dir, "null model target")
	databasePath := realBRDatabasePath(t, dir)

	// Explicitly blank, not merely unset by the test process's own
	// environment: guards against inherited BR_MODEL from the shell.
	t.Setenv("BR_MODEL", "")

	ctx := assignmentstore.WithClaimAttribution(t.Context(), assignmentstore.ClaimAttribution{
		AgentName: "agent",
		Harness:   "cc",
		Model:     "",
	})

	database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
	if err != nil {
		t.Fatalf("open Beads database: %v", err)
	}
	defer database.Close()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := insertGuardedClaimEvent(ctx, tx, beadID, "status_changed", "TestActor", "open", "in_progress", time.Now().UTC()); err != nil {
		t.Fatalf("insertGuardedClaimEvent: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	got := readLatestClaimEventAttribution(t, databasePath, beadID)
	if got.model.Valid {
		t.Fatalf("model = %q (valid=%v), want SQL NULL for an empty model", got.model.String, got.model.Valid)
	}
}

// TestClaimAttributionDiffersPerPaneThroughAtomicCoordinatorExecute is the
// integration test bd-4gw calls out as the one that fails today: two items
// of different agent types claimed through the SAME AtomicCoordinator (the
// exact abstraction ntm assign uses, ClaimBeadForAssignmentWithOperatorGatedLabels
// wired in as the ClaimPort) must record two claim events whose agent_name
// and harness differ, proving attribution is per-request rather than a
// process-global value one item could leak into another's event.
func TestClaimAttributionDiffersPerPaneThroughAtomicCoordinatorExecute(t *testing.T) {
	requireRealBR(t)
	// AssignmentStore persists under $HOME/.ntm/sessions/<session>; sandbox it
	// so this test cannot touch the real home directory (see
	// full-test-suite-is-a-machine-mutation).
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	beadCC := createRealBRBead(t, dir, "attribution pane cc")
	beadCod := createRealBRBead(t, dir, "attribution pane cod")
	databasePath := realBRDatabasePath(t, dir)

	store := assignmentstore.NewStore("bd-4gw-attribution-test")
	claimPort := assignmentstore.ClaimFunc(func(ctx context.Context, beadID, actor string) (assignmentstore.ClaimReceipt, error) {
		claim, err := ClaimBeadForAssignmentWithOperatorGatedLabels(ctx, dir, beadID, actor, OperatorGatedLabels())
		if err != nil {
			return assignmentstore.ClaimReceipt{}, err
		}
		return assignmentstore.ClaimReceipt{
			BeadID: claim.ID, Actor: claim.Actor, Status: claim.Status, ClaimedAt: claim.ClaimedAt,
		}, nil
	})
	reservationPort := assignmentstore.ReservationFunc(func(_ context.Context, req assignmentstore.ReservationRequest) (assignmentstore.LeaseReceipt, error) {
		return assignmentstore.LeaseReceipt{AgentName: req.AgentName, Target: req.Target}, nil
	})
	dispatchPort := assignmentstore.DispatchFunc(func(_ context.Context, req assignmentstore.DispatchRequest) (assignmentstore.DispatchReceipt, error) {
		return assignmentstore.DispatchReceipt{DeliveryID: "noop-delivery-" + req.BeadID}, nil
	})
	coordinator := assignmentstore.NewAtomicCoordinator(store, claimPort, reservationPort, dispatchPort)

	ctx := t.Context()
	items := []struct {
		beadID    string
		agentType string
		agentName string
	}{
		{beadCC, "cc", "cc-agent-one"},
		{beadCod, "cod", "cod-agent-two"},
	}
	for i, item := range items {
		key, err := assignmentstore.NewAssignmentIdempotencyKey()
		if err != nil {
			t.Fatalf("idempotency key: %v", err)
		}
		occupancy := fmt.Sprintf("%%%d", i+1)
		_, err = coordinator.Execute(ctx, assignmentstore.AtomicRequest{
			BeadID:         item.beadID,
			BeadTitle:      "attribution test",
			Target:         occupancy,
			OccupancyKey:   occupancy,
			Pane:           i + 1,
			AgentType:      item.agentType,
			AgentName:      item.agentName,
			Actor:          item.agentName,
			Prompt:         "noop prompt",
			IdempotencyKey: key,
		})
		if err != nil {
			t.Fatalf("Execute(%s): %v", item.beadID, err)
		}
	}

	gotCC := readLatestClaimEventAttribution(t, databasePath, beadCC)
	gotCod := readLatestClaimEventAttribution(t, databasePath, beadCod)

	if gotCC.harness.String != "cc" {
		t.Fatalf("cc pane harness = %+v, want %q", gotCC.harness, "cc")
	}
	if gotCod.harness.String != "cod" {
		t.Fatalf("cod pane harness = %+v, want %q", gotCod.harness, "cod")
	}
	if gotCC.agentName.String != "cc-agent-one" {
		t.Fatalf("cc pane agent_name = %+v, want %q", gotCC.agentName, "cc-agent-one")
	}
	if gotCod.agentName.String != "cod-agent-two" {
		t.Fatalf("cod pane agent_name = %+v, want %q", gotCod.agentName, "cod-agent-two")
	}
	if gotCC.harness.String == gotCod.harness.String || gotCC.agentName.String == gotCod.agentName.String {
		t.Fatalf("attribution did not differ per pane: cc=%+v cod=%+v", gotCC, gotCod)
	}
}
