package robot

import (
	"encoding/json"
	"os"
	"testing"
)

// TestGetDashboardSessionScope exercises the dashboard session scope over a
// stubbed tmux inventory: sessions come from tmux.ListSessions, so the scope
// is applied to that list before any pane work happens.
func TestGetDashboardSessionScope(t *testing.T) {
	useSessionListTmuxBinary(t, "alpha", "beta")

	// Isolate the best-effort collectors (beads, alerts, mail) from the
	// repository's own project state.
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

	t.Run("scoped dashboard returns only that session", func(t *testing.T) {
		output, err := GetDashboard("alpha")
		if err != nil {
			t.Fatalf("GetDashboard() error = %v", err)
		}
		if !output.Success {
			t.Fatalf("response = %+v, want success", output.RobotResponse)
		}
		if len(output.Agents) != 1 || output.Agents[0].Name != "alpha" {
			t.Fatalf("Agents = %+v, want exactly [alpha]", output.Agents)
		}
		if output.Summary.TotalSessions != 1 {
			t.Fatalf("Summary.TotalSessions = %d, want 1", output.Summary.TotalSessions)
		}
	})

	t.Run("unscoped dashboard returns both sessions", func(t *testing.T) {
		output, err := GetDashboard("")
		if err != nil {
			t.Fatalf("GetDashboard() error = %v", err)
		}
		if !output.Success {
			t.Fatalf("response = %+v, want success", output.RobotResponse)
		}
		if len(output.Agents) != 2 {
			t.Fatalf("Agents = %+v, want both [alpha beta]", output.Agents)
		}
	})

	t.Run("unknown session is SESSION_NOT_FOUND", func(t *testing.T) {
		output, err := GetDashboard("gamma")
		if err != nil {
			t.Fatalf("GetDashboard() error = %v", err)
		}
		if output.Success || output.ErrorCode != ErrCodeSessionNotFound {
			t.Fatalf("response = %+v, want SESSION_NOT_FOUND failure", output.RobotResponse)
		}
	})
}

// TestDashboardSessionScopeEnvelope guards the envelope contract for the
// scoped and the failing dashboard responses: each serialises a complete
// RobotResponse, with no partial payload alongside an error.
func TestDashboardSessionScopeEnvelope(t *testing.T) {
	useSessionListTmuxBinary(t, "alpha", "beta")

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

	t.Run("scoped response serialises a complete success envelope", func(t *testing.T) {
		output, err := GetDashboard("alpha")
		if err != nil {
			t.Fatalf("GetDashboard() error = %v", err)
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
		agents, ok := parsed["agents"].([]interface{})
		if !ok || len(agents) != 1 {
			t.Fatalf("agents = %#v, want exactly one entry", parsed["agents"])
		}
		if name := agents[0].(map[string]interface{})["name"]; name != "alpha" {
			t.Errorf("agents[0].name = %v, want alpha", name)
		}
	})

	t.Run("failing response serialises an error envelope with no partial payload", func(t *testing.T) {
		output, err := GetDashboard("gamma")
		if err != nil {
			t.Fatalf("GetDashboard() error = %v", err)
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
		agents, ok := parsed["agents"].([]interface{})
		if !ok || agents == nil || len(agents) != 0 {
			t.Errorf("agents = %#v, want empty array (no partial payload)", parsed["agents"])
		}
	})
}
