package robot

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

// TestGetTerseSessionScope exercises the terse session scope over a stubbed
// tmux inventory: terse output is built from the snapshot, so the scope flows
// through GetSnapshotWithOptions and the terse lines follow the filtered set.
func TestGetTerseSessionScope(t *testing.T) {
	useSessionListTmuxBinary(t, "alpha", "beta")

	// Isolate the best-effort collectors from the repository's own project
	// state.
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

	t.Run("scoped terse returns only that session", func(t *testing.T) {
		output, err := GetTerse(config.Default(), "alpha")
		if err != nil {
			t.Fatalf("GetTerse() error = %v", err)
		}
		if !output.Success {
			t.Fatalf("response = %+v, want success", output.RobotResponse)
		}
		if len(output.States) != 1 || output.States[0].Session != "alpha" {
			t.Fatalf("States = %+v, want exactly [alpha]", output.States)
		}
		if len(output.TerseLines) != 1 {
			t.Fatalf("TerseLines = %+v, want exactly one line", output.TerseLines)
		}
	})

	t.Run("unscoped terse returns both sessions", func(t *testing.T) {
		output, err := GetTerse(config.Default(), "")
		if err != nil {
			t.Fatalf("GetTerse() error = %v", err)
		}
		if !output.Success {
			t.Fatalf("response = %+v, want success", output.RobotResponse)
		}
		if len(output.States) != 2 {
			t.Fatalf("States = %+v, want both [alpha beta]", output.States)
		}
		if len(output.TerseLines) != 2 {
			t.Fatalf("TerseLines = %+v, want two lines", output.TerseLines)
		}
	})

	t.Run("unknown session is SESSION_NOT_FOUND", func(t *testing.T) {
		output, err := GetTerse(config.Default(), "gamma")
		if err != nil {
			t.Fatalf("GetTerse() error = %v", err)
		}
		if output.Success || output.ErrorCode != ErrCodeSessionNotFound {
			t.Fatalf("response = %+v, want SESSION_NOT_FOUND failure", output.RobotResponse)
		}
		if len(output.TerseLines) != 0 {
			t.Fatalf("TerseLines = %+v, want none alongside the error", output.TerseLines)
		}
	})
}

// TestTerseSessionScopeEnvelope guards the envelope contract for the scoped
// and the failing terse responses: each serialises a complete RobotResponse,
// with no partial payload alongside an error.
func TestTerseSessionScopeEnvelope(t *testing.T) {
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
		output, err := GetTerse(config.Default(), "alpha")
		if err != nil {
			t.Fatalf("GetTerse() error = %v", err)
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
		states, ok := parsed["states"].([]interface{})
		if !ok || len(states) != 1 {
			t.Fatalf("states = %#v, want exactly one entry", parsed["states"])
		}
		if session := states[0].(map[string]interface{})["session"]; session != "alpha" {
			t.Errorf("states[0].session = %v, want alpha", session)
		}
	})

	t.Run("failing response serialises an error envelope with no partial payload", func(t *testing.T) {
		output, err := GetTerse(config.Default(), "gamma")
		if err != nil {
			t.Fatalf("GetTerse() error = %v", err)
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
		states, ok := parsed["states"].([]interface{})
		if !ok || states == nil || len(states) != 0 {
			t.Errorf("states = %#v, want empty array (no partial payload)", parsed["states"])
		}
	})
}
