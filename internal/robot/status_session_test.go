package robot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// openSessionStore opens a fresh runtime projection store seeded with the
// given session names, so the projection-backed status path can be exercised
// without a live tmux server.
func openSessionStore(t *testing.T, names ...string) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	staleAfter := now.Add(time.Hour)
	for _, name := range names {
		if err := store.UpsertRuntimeSession(&state.RuntimeSession{
			Name:        name,
			Attached:    name == names[0],
			CollectedAt: now,
			StaleAfter:  staleAfter,
		}); err != nil {
			t.Fatalf("UpsertRuntimeSession(%s): %v", name, err)
		}
	}
	return store
}

// useProjectionStore installs the given store as the runtime projection store
// for the duration of the test.
func useProjectionStore(t *testing.T, store *state.Store) {
	t.Helper()
	oldStore := currentProjectionStore()
	SetProjectionStore(store)
	t.Cleanup(func() { SetProjectionStore(oldStore) })
}

// TestGetStatusWithOptionsSessionScope exercises the projection-backed status
// path: sessions come from the runtime store, so the scope must be applied
// there, and pagination must page the filtered set rather than the fleet.
func TestGetStatusWithOptionsSessionScope(t *testing.T) {
	useProjectionStore(t, openSessionStore(t, "alpha", "beta"))

	t.Run("scoped status returns only that session", func(t *testing.T) {
		output, err := GetStatusWithOptions(PaginationOptions{Session: "alpha"})
		if err != nil {
			t.Fatalf("GetStatusWithOptions() error = %v", err)
		}
		if !output.Success {
			t.Fatalf("response = %+v, want success", output.RobotResponse)
		}
		if len(output.Sessions) != 1 || output.Sessions[0].Name != "alpha" {
			t.Fatalf("Sessions = %+v, want exactly [alpha]", output.Sessions)
		}
		if output.Summary.TotalSessions != 1 {
			t.Fatalf("Summary.TotalSessions = %d, want 1 (summary scoped with sessions)", output.Summary.TotalSessions)
		}
	})

	t.Run("unscoped status returns both sessions", func(t *testing.T) {
		output, err := GetStatusWithOptions(PaginationOptions{})
		if err != nil {
			t.Fatalf("GetStatusWithOptions() error = %v", err)
		}
		if !output.Success {
			t.Fatalf("response = %+v, want success", output.RobotResponse)
		}
		if len(output.Sessions) != 2 {
			t.Fatalf("Sessions = %+v, want both [alpha beta]", output.Sessions)
		}
		if output.Summary.TotalSessions != 2 {
			t.Fatalf("Summary.TotalSessions = %d, want 2", output.Summary.TotalSessions)
		}
	})

	t.Run("unknown session is SESSION_NOT_FOUND", func(t *testing.T) {
		output, err := GetStatusWithOptions(PaginationOptions{Session: "gamma"})
		if err != nil {
			t.Fatalf("GetStatusWithOptions() error = %v", err)
		}
		if output.Success || output.ErrorCode != ErrCodeSessionNotFound {
			t.Fatalf("response = %+v, want SESSION_NOT_FOUND failure", output.RobotResponse)
		}
	})

	t.Run("pagination pages the filtered set, not the fleet", func(t *testing.T) {
		// Against the fleet of two, offset 1 with limit 1 yields the second
		// session. Against the filtered set of one, the same page is empty.
		unscoped, err := GetStatusWithOptions(PaginationOptions{Limit: 1, Offset: 1})
		if err != nil {
			t.Fatalf("GetStatusWithOptions() error = %v", err)
		}
		if len(unscoped.Sessions) != 1 || unscoped.Sessions[0].Name != "beta" {
			t.Fatalf("unscoped page = %+v, want [beta]", unscoped.Sessions)
		}

		scoped, err := GetStatusWithOptions(PaginationOptions{Limit: 1, Offset: 1, Session: "alpha"})
		if err != nil {
			t.Fatalf("GetStatusWithOptions() error = %v", err)
		}
		if len(scoped.Sessions) != 0 {
			t.Fatalf("scoped page = %+v, want empty (filtered set has one session)", scoped.Sessions)
		}
		if scoped.Pagination == nil {
			t.Fatal("Pagination = nil, want page metadata for the filtered set")
		}
		if scoped.Pagination.Total != 1 {
			t.Fatalf("Pagination.Total = %d, want 1 (the filtered set, not the fleet of 2)", scoped.Pagination.Total)
		}

		first, err := GetStatusWithOptions(PaginationOptions{Limit: 1, Offset: 0, Session: "alpha"})
		if err != nil {
			t.Fatalf("GetStatusWithOptions() error = %v", err)
		}
		if len(first.Sessions) != 1 || first.Sessions[0].Name != "alpha" {
			t.Fatalf("scoped first page = %+v, want [alpha]", first.Sessions)
		}
	})
}

// TestGetStatusWithOptionsSessionScopeLive guards the live path: sessions come
// from the tmux projection rather than the runtime store, so the scope must be
// applied there too, and agents (a flat list across sessions) must be scoped
// alongside so the summary does not count other sessions' agents.
func TestGetStatusWithOptionsSessionScopeLive(t *testing.T) {
	useSessionListTmuxBinary(t, "alpha", "beta")
	useProjectionStore(t, nil)

	// Isolate the live collectors from the repository's own project state.
	origDir, _ := os.Getwd()
	projectDir := t.TempDir()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir project dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	output, err := GetStatusWithOptions(PaginationOptions{Session: "alpha"})
	if err != nil {
		t.Fatalf("GetStatusWithOptions() error = %v", err)
	}
	if !output.Success {
		t.Fatalf("response = %+v, want success", output.RobotResponse)
	}
	if len(output.Sessions) != 1 || output.Sessions[0].Name != "alpha" {
		t.Fatalf("Sessions = %+v, want exactly [alpha]", output.Sessions)
	}
	if output.Summary.TotalSessions != 1 {
		t.Fatalf("Summary.TotalSessions = %d, want 1", output.Summary.TotalSessions)
	}
}

// TestStatusSessionScopeEnvelope guards the envelope contract for the scoped
// and the failing status responses: each serialises a complete RobotResponse,
// with no partial payload alongside an error.
func TestStatusSessionScopeEnvelope(t *testing.T) {
	useProjectionStore(t, openSessionStore(t, "alpha", "beta"))

	t.Run("scoped response serialises a complete success envelope", func(t *testing.T) {
		output, err := GetStatusWithOptions(PaginationOptions{Session: "alpha"})
		if err != nil {
			t.Fatalf("GetStatusWithOptions() error = %v", err)
		}
		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("Marshal failed: %v", err)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}
		if parsed["success"] != true {
			t.Errorf("success = %v, want true", parsed["success"])
		}
		sessions, ok := parsed["sessions"].([]interface{})
		if !ok || len(sessions) != 1 {
			t.Fatalf("sessions = %#v, want exactly one entry", parsed["sessions"])
		}
		if name := sessions[0].(map[string]interface{})["name"]; name != "alpha" {
			t.Errorf("sessions[0].name = %v, want alpha", name)
		}
	})

	t.Run("failing response serialises an error envelope with no partial payload", func(t *testing.T) {
		output, err := GetStatusWithOptions(PaginationOptions{Session: "gamma"})
		if err != nil {
			t.Fatalf("GetStatusWithOptions() error = %v", err)
		}
		data, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("Marshal failed: %v", err)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}
		if parsed["success"] != false {
			t.Errorf("success = %v, want false", parsed["success"])
		}
		if parsed["error_code"] != ErrCodeSessionNotFound {
			t.Errorf("error_code = %v, want %s", parsed["error_code"], ErrCodeSessionNotFound)
		}
		sessions, ok := parsed["sessions"].([]interface{})
		if !ok || sessions == nil || len(sessions) != 0 {
			t.Errorf("sessions = %#v, want empty array (no partial payload)", parsed["sessions"])
		}
	})
}
