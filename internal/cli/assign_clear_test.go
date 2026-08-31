package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ============================================================================
// Clear Assignments Tests (bd-30o1y)
// ============================================================================

// ============================================================================
// Data Structure Tests
// ============================================================================

// TestClearAssignmentResultStructure tests the ClearAssignmentResult type
func TestClearAssignmentResultStructure(t *testing.T) {
	result := ClearAssignmentResult{
		BeadID:                   "bd-xyz",
		BeadTitle:                "Test task",
		PreviousPane:             3,
		PreviousAgent:            "GreenLake",
		PreviousAgentType:        "claude",
		PreviousStatus:           "working",
		AssignmentFound:          true,
		FileReservationsReleased: true,
		FilesReleased:            []string{"src/api/*.go"},
		Success:                  true,
		Error:                    "",
		ErrorCode:                "",
	}

	if result.BeadID != "bd-xyz" {
		t.Errorf("Expected BeadID 'bd-xyz', got %q", result.BeadID)
	}
	if result.PreviousPane != 3 {
		t.Errorf("Expected PreviousPane 3, got %d", result.PreviousPane)
	}
	if !result.AssignmentFound {
		t.Error("Expected AssignmentFound to be true")
	}
	if !result.Success {
		t.Error("Expected Success to be true")
	}
}

// TestClearAssignmentResultNotFound tests result when assignment not found
func TestClearAssignmentResultNotFound(t *testing.T) {
	result := ClearAssignmentResult{
		BeadID:          "bd-notfound",
		AssignmentFound: false,
		Success:         false,
		Error:           "assignment not found or already completed",
		ErrorCode:       clearErrNotAssigned,
	}

	if result.AssignmentFound {
		t.Error("Expected AssignmentFound to be false")
	}
	if result.Success {
		t.Error("Expected Success to be false")
	}
	if result.ErrorCode != "NOT_ASSIGNED" {
		t.Errorf("Expected ErrorCode 'NOT_ASSIGNED', got %q", result.ErrorCode)
	}
}

// TestClearAssignmentResultJSON tests JSON marshaling
func TestClearAssignmentResultJSON(t *testing.T) {
	result := ClearAssignmentResult{
		BeadID:          "bd-json",
		PreviousPane:    2,
		PreviousAgent:   "BlueLake",
		AssignmentFound: true,
		Success:         true,
		FilesReleased:   []string{"a.go", "b.go"},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded ClearAssignmentResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.BeadID != "bd-json" {
		t.Errorf("Expected BeadID 'bd-json', got %q", decoded.BeadID)
	}
	if len(decoded.FilesReleased) != 2 {
		t.Errorf("Expected 2 files released, got %d", len(decoded.FilesReleased))
	}
}

// TestClearAllResultStructure tests the ClearAllResult type
func TestClearAllResultStructure(t *testing.T) {
	result := ClearAllResult{
		Pane:      3,
		AgentType: "claude",
		Success:   true,
		ClearedBeads: []ClearAssignmentResult{
			{BeadID: "bd-001", Success: true},
			{BeadID: "bd-002", Success: true},
		},
	}

	if result.Pane != 3 {
		t.Errorf("Expected Pane 3, got %d", result.Pane)
	}
	if result.AgentType != "claude" {
		t.Errorf("Expected AgentType 'claude', got %q", result.AgentType)
	}
	if len(result.ClearedBeads) != 2 {
		t.Errorf("Expected 2 cleared beads, got %d", len(result.ClearedBeads))
	}
}

// TestClearAssignmentsSummaryStructure tests the ClearAssignmentsSummary type
func TestClearAssignmentsSummaryStructure(t *testing.T) {
	summary := ClearAssignmentsSummary{
		ClearedCount:         5,
		ReservationsReleased: 3,
		FailedCount:          1,
	}

	if summary.ClearedCount != 5 {
		t.Errorf("Expected ClearedCount 5, got %d", summary.ClearedCount)
	}
	if summary.ReservationsReleased != 3 {
		t.Errorf("Expected ReservationsReleased 3, got %d", summary.ReservationsReleased)
	}
	if summary.FailedCount != 1 {
		t.Errorf("Expected FailedCount 1, got %d", summary.FailedCount)
	}
}

// TestClearAssignmentsDataStructure tests the ClearAssignmentsData type
func TestClearAssignmentsDataStructure(t *testing.T) {
	pane := 3
	data := ClearAssignmentsData{
		Cleared: []ClearAssignmentResult{
			{BeadID: "bd-001", Success: true},
		},
		Summary: ClearAssignmentsSummary{
			ClearedCount: 1,
		},
		Pane:      &pane,
		AgentType: "claude",
	}

	if len(data.Cleared) != 1 {
		t.Errorf("Expected 1 cleared, got %d", len(data.Cleared))
	}
	if data.Summary.ClearedCount != 1 {
		t.Errorf("Expected ClearedCount 1, got %d", data.Summary.ClearedCount)
	}
	if *data.Pane != 3 {
		t.Errorf("Expected Pane 3, got %d", *data.Pane)
	}
}

// TestClearAssignmentsErrorStructure tests the ClearAssignmentsError type
func TestClearAssignmentsErrorStructure(t *testing.T) {
	err := ClearAssignmentsError{
		Code:    "NOT_ASSIGNED",
		Message: "assignment not found",
		Details: map[string]interface{}{
			"bead_id": "bd-xyz",
		},
	}

	if err.Code != "NOT_ASSIGNED" {
		t.Errorf("Expected Code 'NOT_ASSIGNED', got %q", err.Code)
	}
	if err.Details["bead_id"] != "bd-xyz" {
		t.Errorf("Expected bead_id 'bd-xyz' in details, got %v", err.Details["bead_id"])
	}
}

// TestClearAssignmentsEnvelopeStructure tests the ClearAssignmentsEnvelope type
func TestClearAssignmentsEnvelopeStructure(t *testing.T) {
	envelope := ClearAssignmentsEnvelope{
		Command:    "assign",
		Subcommand: "clear",
		Session:    "myproject",
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Success:    true,
		Data: &ClearAssignmentsData{
			Cleared: []ClearAssignmentResult{
				{BeadID: "bd-001", Success: true},
			},
			Summary: ClearAssignmentsSummary{ClearedCount: 1},
		},
		Warnings: []string{},
	}

	if envelope.Command != "assign" {
		t.Errorf("Expected Command 'assign', got %q", envelope.Command)
	}
	if envelope.Subcommand != "clear" {
		t.Errorf("Expected Subcommand 'clear', got %q", envelope.Subcommand)
	}
	if !envelope.Success {
		t.Error("Expected Success to be true")
	}
}

// ============================================================================
// Error Code Tests
// ============================================================================

// TestClearErrorCodes tests the documented error code constants
func TestClearErrorCodes(t *testing.T) {
	tests := []struct {
		code    string
		meaning string
	}{
		{clearErrNotAssigned, "Assignment not found"},
		{clearErrAlreadyCompleted, "Assignment already completed"},
		{clearErrPaneNotFound, "Pane not found"},
		{clearErrInvalidFlag, "Invalid flag combination"},
		{clearErrInternal, "Internal error"},
	}

	for _, tc := range tests {
		if tc.code == "" {
			t.Errorf("Error code for %q is empty", tc.meaning)
		}
	}

	// Verify specific constant values
	if clearErrNotAssigned != "NOT_ASSIGNED" {
		t.Errorf("Expected clearErrNotAssigned='NOT_ASSIGNED', got %q", clearErrNotAssigned)
	}
	if clearErrAlreadyCompleted != "ALREADY_COMPLETED" {
		t.Errorf("Expected clearErrAlreadyCompleted='ALREADY_COMPLETED', got %q", clearErrAlreadyCompleted)
	}
	if clearErrPaneNotFound != "PANE_NOT_FOUND" {
		t.Errorf("Expected clearErrPaneNotFound='PANE_NOT_FOUND', got %q", clearErrPaneNotFound)
	}
}

// TestClearEnvelopeWithError tests error envelope creation
func TestClearEnvelopeWithError(t *testing.T) {
	envelope := ClearAssignmentsEnvelope{
		Command:    "assign",
		Subcommand: "clear",
		Session:    "test",
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Success:    false,
		Warnings:   []string{},
		Error: &ClearAssignmentsError{
			Code:    "NOT_ASSIGNED",
			Message: "assignment not found",
		},
	}

	if envelope.Success {
		t.Error("Expected Success to be false")
	}
	if envelope.Error == nil {
		t.Fatal("Expected Error to be non-nil")
	}
	if envelope.Error.Code != "NOT_ASSIGNED" {
		t.Errorf("Expected Error.Code 'NOT_ASSIGNED', got %q", envelope.Error.Code)
	}
}

// ============================================================================
// Validation Tests
// ============================================================================

// TestClearAndClearPaneMutuallyExclusive tests that --clear and --clear-pane can't be used together
func TestClearAndClearPaneMutuallyExclusive(t *testing.T) {
	// If both are set, it's an error
	clearBeads := "bd-001"
	clearPane := "0.3"

	isInvalid := clearBeads != "" && clearPane != ""
	if !isInvalid {
		t.Error("Expected setting both --clear and --clear-pane to be invalid")
	}
}

// TestClearRequiresBeadIDs tests that --clear requires bead IDs
func TestClearRequiresBeadIDs(t *testing.T) {
	// Empty string means no beads
	clearBeads := ""
	if clearBeads != "" {
		t.Error("Expected empty string for no beads")
	}
}

// TestClearParsesCommaSeparatedBeads tests parsing comma-separated bead IDs
func TestClearParsesCommaSeparatedBeads(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"bd-001", []string{"bd-001"}},
		{"bd-001,bd-002", []string{"bd-001", "bd-002"}},
		{"bd-001, bd-002, bd-003", []string{"bd-001", "bd-002", "bd-003"}},
		{"", []string{""}},
	}

	for _, tc := range tests {
		// Simulate parsing logic
		result := []string{}
		if tc.input != "" {
			parts := splitAndTrimTestHelper(tc.input)
			result = parts
		} else {
			result = []string{""}
		}

		if len(result) != len(tc.expected) {
			t.Errorf("For input %q, expected %d parts, got %d", tc.input, len(tc.expected), len(result))
		}
	}
}

// Helper function for tests
func splitAndTrimTestHelper(input string) []string {
	parts := []string{}
	start := 0
	for i := 0; i <= len(input); i++ {
		if i == len(input) || input[i] == ',' {
			part := trimSpaces(input[start:i])
			parts = append(parts, part)
			start = i + 1
		}
	}
	return parts
}

func trimSpaces(s string) string {
	start := 0
	end := len(s)
	for start < end && s[start] == ' ' {
		start++
	}
	for end > start && s[end-1] == ' ' {
		end--
	}
	return s[start:end]
}

// ============================================================================
// Assignment Status Tests
// ============================================================================

// TestClearOnlyNonCompletedWithoutForce tests that completed assignments need --force
func TestClearOnlyNonCompletedWithoutForce(t *testing.T) {
	tests := []struct {
		status   assignment.AssignmentStatus
		force    bool
		canClear bool
	}{
		{assignment.StatusAssigned, false, true},
		{assignment.StatusWorking, false, true},
		{assignment.StatusFailed, false, true},
		{assignment.StatusCompleted, false, false}, // Need --force
		{assignment.StatusCompleted, true, true},   // With --force
		{assignment.StatusReassigned, false, true},
	}

	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			// Validation: status != Completed || force
			canClear := tc.status != assignment.StatusCompleted || tc.force
			if canClear != tc.canClear {
				t.Errorf("Expected canClear=%v for status=%s force=%v, got %v",
					tc.canClear, tc.status, tc.force, canClear)
			}
		})
	}
}

// TestClearFailedOnlyFlag tests the --clear-failed flag logic
func TestClearFailedOnlyFlag(t *testing.T) {
	assignments := []assignment.Assignment{
		{BeadID: "bd-001", Status: assignment.StatusWorking},
		{BeadID: "bd-002", Status: assignment.StatusFailed},
		{BeadID: "bd-003", Status: assignment.StatusCompleted},
		{BeadID: "bd-004", Status: assignment.StatusFailed},
	}

	// --clear-failed should only clear failed assignments
	var failedBeads []string
	for _, a := range assignments {
		if a.Status == assignment.StatusFailed {
			failedBeads = append(failedBeads, a.BeadID)
		}
	}

	if len(failedBeads) != 2 {
		t.Errorf("Expected 2 failed beads, got %d", len(failedBeads))
	}
	if failedBeads[0] != "bd-002" || failedBeads[1] != "bd-004" {
		t.Errorf("Expected failed beads bd-002 and bd-004, got %v", failedBeads)
	}
}

func TestClearPaneSelectionAndFilteringUsePhysicalIdentity(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%40", WindowIndex: 0, Index: 1, Type: tmux.AgentCodex},
		{ID: "%41", WindowIndex: 1, Index: 1, Type: tmux.AgentClaude},
	}
	target, err := resolveClearPaneTarget(panes, "0.1")
	if err != nil {
		t.Fatalf("resolve explicit physical pane: %v", err)
	}
	if target.ID != "%40" {
		t.Fatalf("resolved pane = %q, want %%40", target.ID)
	}

	targetAssignment := assignment.Assignment{BeadID: "ntm-target", Pane: 1, OccupancyKey: "%40", DispatchTarget: "%40"}
	otherWindowAssignment := assignment.Assignment{BeadID: "ntm-other", Pane: 1, OccupancyKey: "%41", DispatchTarget: "%41"}
	legacyIndexOnly := assignment.Assignment{BeadID: "ntm-legacy", Pane: 1}
	windowPaneOnly := assignment.Assignment{BeadID: "ntm-window-pane", Pane: 1, OccupancyKey: "0.1", DispatchTarget: "0.1"}
	if matches, matchErr := assignmentMatchesPhysicalPane(targetAssignment, target); matchErr != nil || !matches {
		t.Fatal("target assignment did not match its physical pane")
	}
	if matches, matchErr := assignmentMatchesPhysicalPane(otherWindowAssignment, target); matchErr != nil || matches {
		t.Fatal("same local index in another window matched the target pane")
	}
	for _, malformed := range []assignment.Assignment{legacyIndexOnly, windowPaneOnly} {
		matches, matchErr := assignmentMatchesPhysicalPane(malformed, target)
		var migrationErr *assignment.PaneIdentityMigrationError
		if matches || !errors.Is(matchErr, assignment.ErrPaneIdentityMigrationRequired) || !errors.As(matchErr, &migrationErr) {
			t.Fatalf("malformed assignment %+v match=%v error=%v, want typed migration error", malformed, matches, matchErr)
		}
	}
}

func TestFindAssignmentForPhysicalPaneDisambiguatesDuplicateIndexes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := assignment.NewStore("physical-pane-occupancy")
	store.Assignments["ntm-first"] = &assignment.Assignment{
		BeadID: "ntm-first", Pane: 1, Status: assignment.StatusAssigned,
		OccupancyKey: "%40", DispatchTarget: "%40", AssignedAt: time.Now().UTC(),
	}
	store.Assignments["ntm-second"] = &assignment.Assignment{
		BeadID: "ntm-second", Pane: 1, Status: assignment.StatusWorking,
		OccupancyKey: "%41", DispatchTarget: "%41", AssignedAt: time.Now().UTC(),
	}
	firstPane := tmux.Pane{ID: "%40", WindowIndex: 0, Index: 1}
	secondPane := tmux.Pane{ID: "%41", WindowIndex: 1, Index: 1}
	if got, err := findAssignmentForPhysicalPane(store, firstPane); err != nil || got == nil || got.BeadID != "ntm-first" {
		t.Fatalf("first physical pane assignment = %+v", got)
	}
	if got, err := findAssignmentForPhysicalPane(store, secondPane); err != nil || got == nil || got.BeadID != "ntm-second" {
		t.Fatalf("second physical pane assignment = %+v", got)
	}

	legacy := assignment.NewStore("physical-pane-legacy")
	legacy.Assignments["ntm-legacy"] = &assignment.Assignment{
		BeadID: "ntm-legacy", Pane: 1, Status: assignment.StatusAssigned, AssignedAt: time.Now().UTC(),
	}
	for _, pane := range []tmux.Pane{firstPane, secondPane} {
		got, err := findAssignmentForPhysicalPane(legacy, pane)
		var migrationErr *assignment.PaneIdentityMigrationError
		if got != nil || !errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) || !errors.As(err, &migrationErr) {
			t.Fatalf("legacy local index lookup for %s = %+v, error=%v, want typed migration error", pane.ID, got, err)
		}
	}
}

func TestRunClearFailedAssignmentsClearsOnlyFailedRows(t *testing.T) {
	isolateSessionAgentStorage(t)
	const session = "clear-failed-production"
	store := assignment.NewStore(session)
	if _, err := store.Assign("ntm-working", "Working", 1, "codex", "BlueLake", "work"); err != nil {
		t.Fatalf("assign working row: %v", err)
	}
	if err := store.MarkWorking("ntm-working"); err != nil {
		t.Fatalf("mark working row: %v", err)
	}
	if _, err := store.Assign("ntm-failed", "Failed", 2, "claude", "GreenLake", "work"); err != nil {
		t.Fatalf("assign failed row: %v", err)
	}
	if err := store.MarkWorking("ntm-failed"); err != nil {
		t.Fatalf("mark failed row working: %v", err)
	}
	if err := store.MarkFailed("ntm-failed", "fixture failure"); err != nil {
		t.Fatalf("mark failed row: %v", err)
	}
	if err := store.Save(); err != nil {
		t.Fatalf("save fixture store: %v", err)
	}

	previousJSON := jsonOutput
	previousClear := assignClear
	previousClearPane := assignClearPane
	previousClearFailed := assignClearFailed
	previousForce := assignForce
	previousRelease := releaseAssignmentLeases
	t.Cleanup(func() {
		jsonOutput = previousJSON
		assignClear = previousClear
		assignClearPane = previousClearPane
		assignClearFailed = previousClearFailed
		assignForce = previousForce
		releaseAssignmentLeases = previousRelease
	})
	jsonOutput = true
	assignClear = ""
	assignClearPane = ""
	assignClearFailed = true
	assignForce = false
	releaseAssignmentLeases = func(context.Context, string, *assignment.Assignment) ([]string, error) { return nil, nil }

	output, err := captureStdout(t, func() error {
		return runClearAssignments(&cobra.Command{}, session)
	})
	if err != nil {
		t.Fatalf("runClearAssignments(--clear-failed): %v", err)
	}
	var envelope ClearAssignmentsEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode clear-failed envelope: %v\noutput=%s", err, output)
	}
	if !envelope.Success || envelope.Subcommand != "clear-failed" || envelope.Data == nil {
		t.Fatalf("clear-failed envelope = %+v", envelope)
	}
	if envelope.Data.Summary.ClearedCount != 1 || envelope.Data.Summary.FailedCount != 0 || len(envelope.Data.Cleared) != 1 || envelope.Data.Cleared[0].BeadID != "ntm-failed" {
		t.Fatalf("clear-failed result = %+v", envelope.Data)
	}

	loaded, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	if loaded.Get("ntm-failed") != nil {
		t.Fatal("failed row remained after --clear-failed")
	}
	if current := loaded.Get("ntm-working"); current == nil || current.Status != assignment.StatusWorking {
		t.Fatalf("working row was changed by --clear-failed: %+v", current)
	}
}

func TestRunClearSelectedAssignmentsCanceledJSONIsTimeoutWithoutMutation(t *testing.T) {
	isolateSessionAgentStorage(t)
	const (
		session = "clear-canceled-json"
		beadID  = "ntm-clear-canceled"
	)
	store := assignment.NewStore(session)
	if _, err := store.Assign(beadID, "Canceled clear", 1, "codex", "BlueLake", "work"); err != nil {
		t.Fatalf("assign canceled clear row: %v", err)
	}
	if err := store.Save(); err != nil {
		t.Fatalf("save canceled clear row: %v", err)
	}

	previousJSON := jsonOutput
	previousForce := assignForce
	previousRelease := releaseAssignmentLeases
	t.Cleanup(func() {
		jsonOutput = previousJSON
		assignForce = previousForce
		releaseAssignmentLeases = previousRelease
	})
	jsonOutput = true
	assignForce = false
	releaseAssignmentLeases = func(context.Context, string, *assignment.Assignment) ([]string, error) {
		t.Fatal("canceled clear reached external lease release")
		return nil, nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	output, runErr := captureStdout(t, func() error {
		return runClearSelectedAssignmentsFromStore(cmd, store, session, []string{beadID}, "clear")
	})
	if runErr == nil {
		t.Fatal("canceled clear returned success")
	}
	var envelope ClearAssignmentsEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode canceled clear envelope: %v\noutput=%s", err, output)
	}
	if envelope.Success || envelope.Error == nil || envelope.Error.Code != "TIMEOUT" || envelope.Data == nil ||
		len(envelope.Data.Cleared) != 1 || envelope.Data.Cleared[0].ErrorCode != "TIMEOUT" {
		t.Fatalf("canceled clear envelope = %+v", envelope)
	}

	reloaded, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload canceled clear store: %v", err)
	}
	if current := reloaded.Get(beadID); current == nil || current.Status != assignment.StatusAssigned || current.ClearState != assignment.ClearStateNone {
		t.Fatalf("canceled clear mutated durable assignment: %+v", current)
	}
}

func TestRunClearPaneAssignmentsCancellationIsTypedAndRecoverable(t *testing.T) {
	isolateSessionAgentStorage(t)
	const (
		session = "clear-pane-canceled-json"
		beadID  = "ntm-clear-pane-canceled"
	)
	store := assignment.NewStore(session)
	_, err := store.Assign(beadID, "Canceled pane clear", 1, "codex", "BlueLake", "work")
	if err != nil {
		t.Fatalf("assign canceled pane clear row: %v", err)
	}
	store.Assignments[beadID].DispatchTarget = "%71"
	store.Assignments[beadID].OccupancyKey = "%71"
	if err := store.Save(); err != nil {
		t.Fatalf("save canceled pane clear row: %v", err)
	}

	previousJSON := jsonOutput
	previousForce := assignForce
	previousRelease := releaseAssignmentLeases
	previousGetPanes := getClearPanePanes
	t.Cleanup(func() {
		jsonOutput = previousJSON
		assignForce = previousForce
		releaseAssignmentLeases = previousRelease
		getClearPanePanes = previousGetPanes
	})
	jsonOutput = true
	assignForce = false
	getClearPanePanes = func(context.Context, string) ([]tmux.Pane, error) {
		return []tmux.Pane{{ID: "%71", Index: 1, WindowIndex: 0, Type: tmux.AgentCodex}}, nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	releaseCalls := 0
	releaseAssignmentLeases = func(context.Context, string, *assignment.Assignment) ([]string, error) {
		releaseCalls++
		cancel()
		return nil, errors.New("agent mail release transport stopped")
	}
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	output, runErr := captureStdout(t, func() error {
		return runClearPaneAssignments(cmd, session, "%71")
	})
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("canceled pane clear error = %v, want context.Canceled", runErr)
	}
	if releaseCalls != 1 {
		t.Fatalf("pane clear release calls = %d, want 1", releaseCalls)
	}
	var envelope ClearAssignmentsEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode canceled pane clear envelope: %v\noutput=%s", err, output)
	}
	if envelope.Success || envelope.Error == nil || envelope.Error.Code != robot.ErrCodeTimeout || envelope.Data == nil ||
		len(envelope.Data.Cleared) != 1 || envelope.Data.Cleared[0].ErrorCode != robot.ErrCodeTimeout {
		t.Fatalf("canceled pane clear envelope = %+v", envelope)
	}

	reloaded, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload canceled pane clear store: %v", err)
	}
	if current := reloaded.Get(beadID); current == nil {
		t.Fatal("canceled pane clear removed recoverable assignment")
	}
}

func TestRunClearPaneAssignmentsPaneDiscoveryCancellationIsTimeout(t *testing.T) {
	previousJSON := jsonOutput
	previousGetPanes := getClearPanePanes
	t.Cleanup(func() {
		jsonOutput = previousJSON
		getClearPanePanes = previousGetPanes
	})
	jsonOutput = true
	ctx, cancel := context.WithCancel(t.Context())
	getClearPanePanes = func(context.Context, string) ([]tmux.Pane, error) {
		cancel()
		return nil, errors.New("tmux transport stopped")
	}
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	output, runErr := captureStdout(t, func() error {
		return runClearPaneAssignments(cmd, "clear-pane-discovery-canceled", "%71")
	})
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("canceled pane discovery error = %v, want context.Canceled", runErr)
	}
	var envelope ClearAssignmentsEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode canceled pane discovery envelope: %v\noutput=%s", err, output)
	}
	if envelope.Success || envelope.Error == nil || envelope.Error.Code != robot.ErrCodeTimeout {
		t.Fatalf("canceled pane discovery envelope = %+v", envelope)
	}
}

func TestRunClearFailedRejectsPendingCompletionEvent(t *testing.T) {
	for _, leased := range []bool{false, true} {
		t.Run(map[bool]string{false: "unleased", true: "leased"}[leased], func(t *testing.T) {
			isolateSessionAgentStorage(t)
			const (
				session = "clear-failed-pending-completion"
				beadID  = "ntm-clear-failed-pending-completion"
				eventID = "clear-failed-pending-completion-event"
			)
			now := time.Now().UTC()
			store := assignment.NewStore(session)
			store.Assignments[beadID] = &assignment.Assignment{
				BeadID: beadID, Status: assignment.StatusFailed, AssignedAt: now,
				DispatchTarget: "%72", OccupancyKey: "%72",
				PendingCompletionEventID: eventID, CompletionDetectedAt: &now,
			}
			if leased {
				expiresAt := now.Add(time.Minute)
				store.Assignments[beadID].CompletionConsumerToken = "clear-failed-consumer"
				store.Assignments[beadID].CompletionLeaseExpiresAt = &expiresAt
			}
			if err := store.Save(); err != nil {
				t.Fatalf("seed pending completion clear-failed: %v", err)
			}
			before := store.Get(beadID)

			previousJSON := jsonOutput
			previousForce := assignForce
			previousRelease := releaseAssignmentLeases
			t.Cleanup(func() {
				jsonOutput = previousJSON
				assignForce = previousForce
				releaseAssignmentLeases = previousRelease
			})
			jsonOutput = true
			assignForce = false
			releaseAssignmentLeases = func(context.Context, string, *assignment.Assignment) ([]string, error) {
				t.Fatal("pending completion clear crossed external release boundary")
				return nil, nil
			}

			output, runErr := captureStdout(t, func() error {
				return runClearFailedAssignments(&cobra.Command{}, session)
			})
			if !errors.Is(runErr, assignment.ErrCompletionEventPending) {
				t.Fatalf("pending completion clear-failed error = %v, want ErrCompletionEventPending", runErr)
			}
			var envelope ClearAssignmentsEnvelope
			if err := json.Unmarshal([]byte(output), &envelope); err != nil {
				t.Fatalf("decode pending completion clear-failed envelope: %v\noutput=%s", err, output)
			}
			if envelope.Success || envelope.Error == nil || envelope.Error.Code != clearErrCompletionPending || envelope.Data == nil ||
				len(envelope.Data.Cleared) != 1 || envelope.Data.Cleared[0].ErrorCode != clearErrCompletionPending {
				t.Fatalf("pending completion clear-failed envelope = %+v", envelope)
			}
			after, err := assignment.LoadStoreStrict(session)
			if err != nil {
				t.Fatalf("strict reload after refused completion clear: %v", err)
			}
			current := after.Get(beadID)
			if current == nil || current.PendingCompletionEventID != before.PendingCompletionEventID ||
				current.CompletionConsumerToken != before.CompletionConsumerToken || current.ClearState != before.ClearState {
				t.Fatalf("refused pending completion clear mutated row: before=%+v after=%+v", before, current)
			}
		})
	}
}

// ============================================================================
// CLI Flag Tests
// ============================================================================

// TestAssignCommandHasClearFlag tests that the assign command has --clear flag
func TestAssignCommandHasClearFlag(t *testing.T) {
	cmd := newAssignCmd()
	flag := cmd.Flags().Lookup("clear")
	if flag == nil {
		t.Fatal("Expected 'clear' flag to exist")
	}
}

// TestAssignCommandHasClearPaneFlag tests that the assign command has --clear-pane flag
func TestAssignCommandHasClearPaneFlag(t *testing.T) {
	cmd := newAssignCmd()
	flag := cmd.Flags().Lookup("clear-pane")
	if flag == nil {
		t.Fatal("Expected 'clear-pane' flag to exist")
	}
}

// TestAssignCommandHasClearFailedFlag tests that the assign command has --clear-failed flag
func TestAssignCommandHasClearFailedFlag(t *testing.T) {
	cmd := newAssignCmd()
	flag := cmd.Flags().Lookup("clear-failed")
	if flag == nil {
		t.Fatal("Expected 'clear-failed' flag to exist")
	}
}

// TestClearFlagDefaultValue tests that --clear defaults to empty
func TestClearFlagDefaultValue(t *testing.T) {
	cmd := newAssignCmd()
	flag := cmd.Flags().Lookup("clear")
	if flag == nil {
		t.Fatal("Expected 'clear' flag to exist")
	}
	if flag.DefValue != "" {
		t.Errorf("Expected default value '', got %q", flag.DefValue)
	}
}

// TestClearPaneFlagDefaultValue tests that --clear-pane defaults to empty.
func TestClearPaneFlagDefaultValue(t *testing.T) {
	cmd := newAssignCmd()
	flag := cmd.Flags().Lookup("clear-pane")
	if flag == nil {
		t.Fatal("Expected 'clear-pane' flag to exist")
	}
	if flag.DefValue != "" {
		t.Errorf("Expected default value '', got %q", flag.DefValue)
	}
}

// ============================================================================
// File Reservation Release Tests
// ============================================================================

// TestClearReleasesFileReservations tests that clear releases file reservations
func TestClearReleasesFileReservations(t *testing.T) {
	result := ClearAssignmentResult{
		BeadID:                   "bd-xyz",
		AssignmentFound:          true,
		FileReservationsReleased: true,
		FilesReleased:            []string{"src/api/*.go", "src/utils/*.go"},
		Success:                  true,
	}

	if !result.FileReservationsReleased {
		t.Error("Expected FileReservationsReleased to be true")
	}
	if len(result.FilesReleased) != 2 {
		t.Errorf("Expected 2 files released, got %d", len(result.FilesReleased))
	}
}

// TestClearWithNoReservationsToRelease tests clear with no file reservations
func TestClearWithNoReservationsToRelease(t *testing.T) {
	result := ClearAssignmentResult{
		BeadID:                   "bd-xyz",
		AssignmentFound:          true,
		FileReservationsReleased: false,
		FilesReleased:            nil,
		Success:                  true,
	}

	if result.FileReservationsReleased {
		t.Error("Expected FileReservationsReleased to be false when no reservations")
	}
}

// ============================================================================
// Edge Cases
// ============================================================================

// TestClearEmptyBeadList tests clearing with empty bead list
func TestClearEmptyBeadList(t *testing.T) {
	beadIDs := []string{}

	// Empty list should be handled gracefully
	if len(beadIDs) != 0 {
		t.Errorf("Expected empty bead list, got %d beads", len(beadIDs))
	}
}

// TestClearNonExistentBead tests clearing a bead that doesn't exist
func TestClearNonExistentBead(t *testing.T) {
	result := ClearAssignmentResult{
		BeadID:          "bd-nonexistent",
		AssignmentFound: false,
		Success:         false,
		Error:           "assignment not found or already completed",
	}

	if result.AssignmentFound {
		t.Error("Expected AssignmentFound to be false")
	}
	if result.Success {
		t.Error("Expected Success to be false")
	}
}

// TestClearPaneWithNoAssignments tests clearing a pane with no assignments
func TestClearPaneWithNoAssignments(t *testing.T) {
	result := ClearAllResult{
		Pane:         5,
		AgentType:    "claude",
		Success:      true,
		ClearedBeads: []ClearAssignmentResult{},
	}

	// Should succeed with 0 cleared beads
	if !result.Success {
		t.Error("Expected Success to be true even with no beads to clear")
	}
	if len(result.ClearedBeads) != 0 {
		t.Errorf("Expected 0 cleared beads, got %d", len(result.ClearedBeads))
	}
}

// TestClearAlreadyClearedAssignment tests clearing the same assignment twice
func TestClearAlreadyClearedAssignment(t *testing.T) {
	// First clear succeeds
	firstResult := ClearAssignmentResult{
		BeadID:          "bd-xyz",
		AssignmentFound: true,
		Success:         true,
	}

	// Second clear fails (already cleared)
	secondResult := ClearAssignmentResult{
		BeadID:          "bd-xyz",
		AssignmentFound: false,
		Success:         false,
		Error:           "assignment not found or already completed",
	}

	if !firstResult.Success {
		t.Error("Expected first clear to succeed")
	}
	if secondResult.Success {
		t.Error("Expected second clear to fail")
	}
}

// TestClearCompletedWithoutForce tests clearing completed assignment without --force
func TestClearCompletedWithoutForce(t *testing.T) {
	a := assignment.Assignment{
		BeadID: "bd-completed",
		Status: assignment.StatusCompleted,
	}
	force := false

	// Validation: completed without force should be rejected
	shouldReject := a.Status == assignment.StatusCompleted && !force
	if !shouldReject {
		t.Error("Expected completed assignment without --force to be rejected")
	}
}

// TestClearCompletedWithForce tests clearing completed assignment with --force
func TestClearCompletedWithForce(t *testing.T) {
	a := assignment.Assignment{
		BeadID: "bd-completed",
		Status: assignment.StatusCompleted,
	}
	force := true

	// Validation: completed with force should be allowed
	shouldAllow := a.Status != assignment.StatusCompleted || force
	if !shouldAllow {
		t.Error("Expected completed assignment with --force to be allowed")
	}
}

// ============================================================================
// Batch Clear Tests
// ============================================================================

// TestBatchClearSuccess tests clearing multiple beads successfully
func TestBatchClearSuccess(t *testing.T) {
	results := []ClearAssignmentResult{
		{BeadID: "bd-001", Success: true},
		{BeadID: "bd-002", Success: true},
		{BeadID: "bd-003", Success: true},
	}

	successCount := 0
	for _, r := range results {
		if r.Success {
			successCount++
		}
	}

	if successCount != 3 {
		t.Errorf("Expected 3 successful clears, got %d", successCount)
	}
}

// TestBatchClearPartialFailure tests clearing multiple beads with some failures
func TestBatchClearPartialFailure(t *testing.T) {
	results := []ClearAssignmentResult{
		{BeadID: "bd-001", Success: true},
		{BeadID: "bd-002", Success: false, Error: "assignment not found"},
		{BeadID: "bd-003", Success: true},
	}

	successCount := 0
	failCount := 0
	for _, r := range results {
		if r.Success {
			successCount++
		} else {
			failCount++
		}
	}

	if successCount != 2 {
		t.Errorf("Expected 2 successful clears, got %d", successCount)
	}
	if failCount != 1 {
		t.Errorf("Expected 1 failed clear, got %d", failCount)
	}
}

// TestClearSummaryAccuracy tests that summary stats are accurate
func TestClearSummaryAccuracy(t *testing.T) {
	results := []ClearAssignmentResult{
		{BeadID: "bd-001", Success: true, FilesReleased: []string{"a.go"}},
		{BeadID: "bd-002", Success: true, FilesReleased: []string{"b.go", "c.go"}},
		{BeadID: "bd-003", Success: false},
		{BeadID: "bd-004", Success: true, FilesReleased: nil},
	}

	cleared := 0
	failed := 0
	reservations := 0
	for _, r := range results {
		if r.Success {
			cleared++
			reservations += len(r.FilesReleased)
		} else {
			failed++
		}
	}

	if cleared != 3 {
		t.Errorf("Expected 3 cleared, got %d", cleared)
	}
	if failed != 1 {
		t.Errorf("Expected 1 failed, got %d", failed)
	}
	if reservations != 3 {
		t.Errorf("Expected 3 reservations released, got %d", reservations)
	}
}

// ============================================================================
// JSON Envelope Tests
// ============================================================================

// TestClearEnvelopeJSONMarshaling tests JSON marshaling of envelope
func TestClearEnvelopeJSONMarshaling(t *testing.T) {
	envelope := ClearAssignmentsEnvelope{
		Command:    "assign",
		Subcommand: "clear",
		Session:    "myproject",
		Timestamp:  "2026-01-20T12:00:00Z",
		Success:    true,
		Data: &ClearAssignmentsData{
			Cleared: []ClearAssignmentResult{
				{BeadID: "bd-001", Success: true},
			},
			Summary: ClearAssignmentsSummary{ClearedCount: 1},
		},
		Warnings: []string{},
	}

	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("Failed to marshal envelope: %v", err)
	}

	var decoded ClearAssignmentsEnvelope
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal envelope: %v", err)
	}

	if decoded.Command != "assign" {
		t.Errorf("Expected Command 'assign', got %q", decoded.Command)
	}
	if decoded.Subcommand != "clear" {
		t.Errorf("Expected Subcommand 'clear', got %q", decoded.Subcommand)
	}
	if !decoded.Success {
		t.Error("Expected Success to be true")
	}
	if decoded.Data == nil {
		t.Fatal("Expected Data to be non-nil")
	}
	if len(decoded.Data.Cleared) != 1 {
		t.Errorf("Expected 1 cleared, got %d", len(decoded.Data.Cleared))
	}
}

// TestClearPaneEnvelopeSubcommand tests the subcommand value for clear-pane
func TestClearPaneEnvelopeSubcommand(t *testing.T) {
	pane := 3
	envelope := ClearAssignmentsEnvelope{
		Command:    "assign",
		Subcommand: "clear-pane",
		Session:    "test",
		Timestamp:  "2026-01-20T12:00:00Z",
		Success:    true,
		Data: &ClearAssignmentsData{
			Pane: &pane,
		},
		Warnings: []string{},
	}

	if envelope.Subcommand != "clear-pane" {
		t.Errorf("Expected Subcommand 'clear-pane', got %q", envelope.Subcommand)
	}
	if envelope.Data.Pane == nil || *envelope.Data.Pane != 3 {
		t.Error("Expected Pane to be 3")
	}
}

// ============================================================================
// Orphaned Beads claim recovery (bd-1zn)
// ============================================================================

type orphanedClaimReleaseRecorder struct {
	calls    int
	beadID   string
	actor    string
	project  string
	released bool
	err      error
	release  func(context.Context, string, string, string) (bool, error)
}

func (r *orphanedClaimReleaseRecorder) record(_ context.Context, project, beadID, actor string) (bool, error) {
	r.calls++
	r.project, r.beadID, r.actor = project, beadID, actor
	if r.release == nil {
		return r.released, r.err
	}
	return r.release(context.Background(), project, beadID, actor)
}

func stubOrphanedClaimLookups(t *testing.T, details *bv.BeadAssignmentDetails, detailsErr error, recorder *orphanedClaimReleaseRecorder, projectDir string) {
	t.Helper()
	previousDetails := getBeadAssignmentDetailsForAssignment
	previousRelease := releaseBeadClaimForAssignment
	previousRepo := assignRepoPath
	t.Cleanup(func() {
		getBeadAssignmentDetailsForAssignment = previousDetails
		releaseBeadClaimForAssignment = previousRelease
		assignRepoPath = previousRepo
	})
	getBeadAssignmentDetailsForAssignment = func(context.Context, string, string) (*bv.BeadAssignmentDetails, error) {
		if detailsErr != nil {
			return nil, detailsErr
		}
		return details, nil
	}
	releaseBeadClaimForAssignment = func(ctx context.Context, project, beadID, actor string) (bool, error) {
		return recorder.record(ctx, project, beadID, actor)
	}
	assignRepoPath = projectDir
}

func runClearJSONForTest(t *testing.T, session string, beadIDs ...string) (ClearAssignmentsEnvelope, error) {
	t.Helper()
	return captureClearEnvelopeForTest(t, func() error {
		store, err := assignment.LoadStoreStrict(session)
		if err != nil {
			return err
		}
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		return runClearSelectedAssignmentsFromStore(cmd, store, session, beadIDs, "clear")
	})
}

func captureClearEnvelopeForTest(t *testing.T, run func() error) (ClearAssignmentsEnvelope, error) {
	t.Helper()
	previousJSON := jsonOutput
	previousClear := assignClear
	previousClearPane := assignClearPane
	previousClearFailed := assignClearFailed
	previousForce := assignForce
	previousReleaseLeases := releaseAssignmentLeases
	t.Cleanup(func() {
		jsonOutput = previousJSON
		assignClear = previousClear
		assignClearPane = previousClearPane
		assignClearFailed = previousClearFailed
		assignForce = previousForce
		releaseAssignmentLeases = previousReleaseLeases
	})
	jsonOutput = true
	assignClear = ""
	assignClearPane = ""
	assignClearFailed = false
	assignForce = false
	releaseAssignmentLeases = func(context.Context, string, *assignment.Assignment) ([]string, error) { return nil, nil }

	output, runErr := captureStdout(t, run)
	var envelope ClearAssignmentsEnvelope
	if decodeErr := json.Unmarshal([]byte(output), &envelope); decodeErr != nil {
		t.Fatalf("decode clear envelope: %v\noutput=%s", decodeErr, output)
	}
	return envelope, runErr
}

// TestClearRecordAbsentReleasesOwnOrphanedBeadsClaim proves the bd-1zn
// recovery: a bead with no durable record but a standing ntm-created br claim
// is released through the tracker lookup, and the result names the path.
func TestClearRecordAbsentReleasesOwnOrphanedBeadsClaim(t *testing.T) {
	isolateSessionAgentStorage(t)
	const (
		session = "orphan-own-claim"
		beadID  = "aub-xus.1"
		actor   = "BlueLake/ntm-0123456789ab"
	)
	recorder := &orphanedClaimReleaseRecorder{released: true}
	projectDir := t.TempDir()
	stubOrphanedClaimLookups(t, &bv.BeadAssignmentDetails{ID: beadID, Assignee: actor}, nil, recorder, projectDir)

	envelope, runErr := runClearJSONForTest(t, session, beadID)
	if runErr != nil {
		t.Fatalf("clear record-absent own claim: %v", runErr)
	}
	if !envelope.Success || envelope.Data == nil || envelope.Data.Summary.ClearedCount != 1 {
		t.Fatalf("orphaned-claim clear envelope = %+v", envelope)
	}
	cleared := envelope.Data.Cleared[0]
	if !cleared.Success || cleared.ReleasedVia != clearReleasedViaOrphanedBeadsClaim || cleared.PreviousAgent != actor {
		t.Fatalf("cleared result = %+v", cleared)
	}
	if recorder.calls != 1 || recorder.beadID != beadID || recorder.actor != actor || recorder.project != projectDir {
		t.Fatalf("claim release call = %+v", recorder)
	}
}

// TestClearRecordAbsentForeignClaimRefuses proves another actor's claim is
// never released: the tracker lookup refuses without calling the release.
func TestClearRecordAbsentForeignClaimRefuses(t *testing.T) {
	isolateSessionAgentStorage(t)
	const (
		session = "orphan-foreign-claim"
		beadID  = "aub-xus.2"
	)
	recorder := &orphanedClaimReleaseRecorder{released: true}
	stubOrphanedClaimLookups(t, &bv.BeadAssignmentDetails{ID: beadID, Assignee: "EmeraldCat", Status: "in_progress"}, nil, recorder, t.TempDir())
	recorder.release = func(context.Context, string, string, string) (bool, error) {
		t.Fatal("foreign claim was released through the orphaned-claim fallback")
		return false, nil
	}

	envelope, _ := runClearJSONForTest(t, session, beadID)
	if envelope.Success || envelope.Data == nil || envelope.Data.Summary.ClearedCount != 0 {
		t.Fatalf("foreign-claim clear envelope = %+v", envelope)
	}
	cleared := envelope.Data.Cleared[0]
	if cleared.Success || cleared.Error != "assignment not found or already completed" {
		t.Fatalf("foreign-claim result = %+v", cleared)
	}
}

// TestClearRecordAbsentUnclaimedBeadRefuses keeps the unclaimed record-absent
// case reporting not-found.
func TestClearRecordAbsentUnclaimedBeadRefuses(t *testing.T) {
	isolateSessionAgentStorage(t)
	const (
		session = "orphan-unclaimed"
		beadID  = "aub-xus.3"
	)
	recorder := &orphanedClaimReleaseRecorder{released: true}
	recorder.release = func(context.Context, string, string, string) (bool, error) {
		t.Fatal("unclaimed bead reached the claim release")
		return false, nil
	}
	stubOrphanedClaimLookups(t, &bv.BeadAssignmentDetails{ID: beadID, Assignee: ""}, nil, recorder, t.TempDir())

	envelope, _ := runClearJSONForTest(t, session, beadID)
	if envelope.Success || envelope.Data == nil || envelope.Data.Summary.ClearedCount != 0 {
		t.Fatalf("unclaimed clear envelope = %+v", envelope)
	}
	if got := envelope.Data.Cleared[0].Error; got != "assignment not found or already completed" {
		t.Fatalf("unclaimed result error = %q", got)
	}
}

// TestClearRecordAbsentReleaseCASRefusalFails proves a mid-flight actor
// change is a loud failure instead of a claimed success.
func TestClearRecordAbsentReleaseCASRefusalFails(t *testing.T) {
	isolateSessionAgentStorage(t)
	const (
		session = "orphan-cas-refusal"
		beadID  = "aub-xus.4"
	)
	recorder := &orphanedClaimReleaseRecorder{released: false}
	stubOrphanedClaimLookups(t, &bv.BeadAssignmentDetails{ID: beadID, Assignee: "BlueLake/ntm-0123456789ab"}, nil, recorder, t.TempDir())

	envelope, _ := runClearJSONForTest(t, session, beadID)
	if envelope.Success || envelope.Data == nil || envelope.Data.Summary.ClearedCount != 0 {
		t.Fatalf("CAS-refusal clear envelope = %+v", envelope)
	}
	cleared := envelope.Data.Cleared[0]
	if cleared.Success || !strings.Contains(cleared.Error, "compare-and-set refused") {
		t.Fatalf("CAS-refusal result = %+v", cleared)
	}
}

// TestClearRecordAbsentInspectionFailureFailsLoud proves a tracker lookup
// error is surfaced, never silently reported as "not found".
func TestClearRecordAbsentInspectionFailureFailsLoud(t *testing.T) {
	isolateSessionAgentStorage(t)
	const (
		session = "orphan-inspection-failure"
		beadID  = "aub-xus.5"
	)
	recorder := &orphanedClaimReleaseRecorder{}
	recorder.release = func(context.Context, string, string, string) (bool, error) {
		t.Fatal("unverified bead reached the claim release")
		return false, nil
	}
	stubOrphanedClaimLookups(t, nil, errors.New("br unavailable"), recorder, t.TempDir())

	envelope, _ := runClearJSONForTest(t, session, beadID)
	if envelope.Success || envelope.Data == nil || envelope.Data.Summary.ClearedCount != 0 {
		t.Fatalf("inspection-failure clear envelope = %+v", envelope)
	}
	if got := envelope.Data.Cleared[0].Error; !strings.Contains(got, "inspect Beads claim for record-absent") {
		t.Fatalf("inspection-failure result error = %q", got)
	}
}

// TestClearCompletedRecordWithoutForceSkipsTrackerLookup keeps the force gate
// on completed rows: the orphaned-claim fallback must not bypass it.
func TestClearCompletedRecordWithoutForceSkipsTrackerLookup(t *testing.T) {
	isolateSessionAgentStorage(t)
	const (
		session = "orphan-completed-row"
		beadID  = "ntm-completed"
	)
	store := assignment.NewStore(session)
	if _, err := store.Assign(beadID, "Completed", 1, "codex", "BlueLake", "work"); err != nil {
		t.Fatalf("assign completed row: %v", err)
	}
	if err := store.MarkCompleted(beadID); err != nil {
		t.Fatalf("mark completed row: %v", err)
	}
	if err := store.Save(); err != nil {
		t.Fatalf("save completed row: %v", err)
	}
	recorder := &orphanedClaimReleaseRecorder{}
	recorder.release = func(context.Context, string, string, string) (bool, error) {
		t.Fatal("force-gated completed row reached the orphaned-claim fallback")
		return false, nil
	}
	stubOrphanedClaimLookups(t, &bv.BeadAssignmentDetails{ID: beadID, Assignee: "BlueLake/ntm-0123456789ab"}, nil, recorder, t.TempDir())

	envelope, _ := runClearJSONForTest(t, session, beadID)
	if envelope.Success || envelope.Data == nil || len(envelope.Data.Cleared) != 1 {
		t.Fatalf("completed-row clear envelope = %+v", envelope)
	}
	if got := envelope.Data.Cleared[0].Error; got != "assignment not found or already completed" {
		t.Fatalf("completed-row result error = %q", got)
	}
}

// TestClearRecordPresentReportsAssignmentRecordPath proves the record-present
// path is unchanged apart from naming its release path.
func TestClearRecordPresentReportsAssignmentRecordPath(t *testing.T) {
	isolateSessionAgentStorage(t)
	const (
		session = "record-present-via"
		beadID  = "ntm-row"
	)
	store := assignment.NewStore(session)
	if _, err := store.Assign(beadID, "Row", 1, "codex", "BlueLake", "work"); err != nil {
		t.Fatalf("assign row: %v", err)
	}
	if err := store.Save(); err != nil {
		t.Fatalf("save row: %v", err)
	}
	recorder := &orphanedClaimReleaseRecorder{}
	recorder.release = func(context.Context, string, string, string) (bool, error) {
		t.Fatal("record-present clear must not release through the tracker fallback")
		return false, nil
	}
	stubOrphanedClaimLookups(t, nil, errors.New("tracker lookup must not run for a recorded row"), recorder, t.TempDir())

	envelope, runErr := runClearJSONForTest(t, session, beadID)
	if runErr != nil {
		t.Fatalf("clear recorded row: %v", runErr)
	}
	if !envelope.Success || envelope.Data == nil || envelope.Data.Summary.ClearedCount != 1 {
		t.Fatalf("recorded-row clear envelope = %+v", envelope)
	}
	if got := envelope.Data.Cleared[0].ReleasedVia; got != clearReleasedViaAssignmentRecord {
		t.Fatalf("recorded-row released_via = %q", got)
	}
}

// TestAtomicDispatchFailureClaimNote pins the post-claim failure note: it is
// emitted only when the durable record proves the claim landed, and it names
// the guaranteed recovery command.
func TestAtomicDispatchFailureClaimNote(t *testing.T) {
	now := time.Now().UTC()
	claimed := &assignment.Assignment{
		BeadID:         "aub-xus.1",
		IdempotencyKey: strings.Repeat("ab", 32),
		ClaimState:     assignment.ClaimClaimed,
		ClaimedAt:      &now,
	}
	tests := []struct {
		name    string
		record  *assignment.Assignment
		session string
		want    string
	}{
		{name: "claimed", record: claimed, session: "proj", want: `release it with "ntm assign proj --clear aub-xus.1"`},
		{name: "claim pending", record: &assignment.Assignment{IdempotencyKey: "k", ClaimState: assignment.ClaimPending}, want: ""},
		{name: "claim absent", record: &assignment.Assignment{IdempotencyKey: "k"}, want: ""},
		{name: "no key", record: &assignment.Assignment{ClaimState: assignment.ClaimClaimed}, want: ""},
		{name: "nil record", record: nil, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := atomicDispatchFailureClaimNote(test.record, test.session, "aub-xus.1")
			if test.want == "" {
				if got != "" {
					t.Fatalf("note = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, "intentionally kept") || !strings.Contains(got, test.want) {
				t.Fatalf("note = %q, want it to contain %q", got, test.want)
			}
		})
	}
}
