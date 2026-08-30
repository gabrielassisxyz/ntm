package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/swarm"
)

// planWithPaneIndices builds a minimal SwarmPlan whose only shape that
// matters to validateSwarmIdentityEnv is session names and pane indices.
func planWithPaneIndices(sessions map[string][]int) *swarm.SwarmPlan {
	plan := &swarm.SwarmPlan{}
	for name, indices := range sessions {
		spec := swarm.SessionSpec{Name: name}
		for _, idx := range indices {
			spec.Panes = append(spec.Panes, swarm.PaneSpec{Index: idx})
		}
		plan.Sessions = append(plan.Sessions, spec)
	}
	return plan
}

func TestValidateSwarmIdentityEnv(t *testing.T) {
	tests := []struct {
		name    string
		plan    *swarm.SwarmPlan
		optOut  bool
		wantErr bool
	}{
		{
			name:    "duplicate derived name across two panes",
			plan:    planWithPaneIndices(map[string][]int{"cc_agents_1": {1, 1}}),
			wantErr: true,
		},
		{
			name:    "pane whose derived name would be empty",
			plan:    planWithPaneIndices(map[string][]int{"cc_agents_1": {0}}),
			wantErr: true,
		},
		{
			name:    "opt-out set skips validation even with a real conflict",
			plan:    planWithPaneIndices(map[string][]int{"cc_agents_1": {1, 1}}),
			optOut:  true,
			wantErr: false,
		},
		{
			// The planted negative: every pane derives a distinct, non-empty
			// name, so a validator that refuses everything would still pass
			// the three cases above and only be caught here.
			name: "all correct across two sessions",
			plan: planWithPaneIndices(map[string][]int{
				"cc_agents_1":  {1, 2, 3},
				"cod_agents_1": {1, 2},
			}),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSwarmIdentityEnv(tt.plan, tt.optOut)
			if tt.wantErr && err == nil {
				t.Fatalf("validateSwarmIdentityEnv() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateSwarmIdentityEnv() = %v, want nil", err)
			}
		})
	}
}

func TestSwarmIdentityEnvOptOut(t *testing.T) {
	t.Run("flag alone opts out", func(t *testing.T) {
		t.Setenv("NTM_SWARM_IDENTITY_ENV", "")
		if !swarmIdentityEnvOptOut(true) {
			t.Fatal("expected opt-out with flag=true")
		}
	})

	t.Run("env var 0 opts out without the flag", func(t *testing.T) {
		t.Setenv("NTM_SWARM_IDENTITY_ENV", "0")
		if !swarmIdentityEnvOptOut(false) {
			t.Fatal("expected opt-out with NTM_SWARM_IDENTITY_ENV=0")
		}
	})

	t.Run("unset env and no flag leaves the default enabled", func(t *testing.T) {
		os.Unsetenv("NTM_SWARM_IDENTITY_ENV")
		if swarmIdentityEnvOptOut(false) {
			t.Fatal("expected identity env enabled by default (opt-out, not opt-in)")
		}
	})

	t.Run("unrelated env value does not opt out", func(t *testing.T) {
		t.Setenv("NTM_SWARM_IDENTITY_ENV", "1")
		if swarmIdentityEnvOptOut(false) {
			t.Fatal("expected NTM_SWARM_IDENTITY_ENV=1 to leave the layer enabled")
		}
	})
}

// TestSwarmIdentityWarnings_OptOutSaysSoOutLoud is the bd-fug requirement
// that a swarm disabling its own exclusion layer must say so in the
// output rather than going quiet about it. swarmIdentityWarnings is the
// single source both printSwarmPlan (plain text) and printSwarmJSON
// (out.Warnings, encoded as-is) read from, so proving it here proves both.
func TestSwarmIdentityWarnings_OptOutSaysSoOutLoud(t *testing.T) {
	got := swarmIdentityWarnings(true)
	if len(got) != 1 {
		t.Fatalf("swarmIdentityWarnings(true) = %v, want exactly one entry", got)
	}
	if !strings.Contains(got[0], "no-identity-env") || !strings.Contains(got[0], "NTM_SWARM_IDENTITY_ENV") {
		t.Errorf("warning = %q, want it to name both the flag and the env var", got[0])
	}
}

func TestSwarmIdentityWarnings_EnabledIsSilent(t *testing.T) {
	if got := swarmIdentityWarnings(false); got != nil {
		t.Fatalf("swarmIdentityWarnings(false) = %v, want nil (no warning when the layer is on)", got)
	}
}

// TestPrintSwarmPlan_CarriesIdentityOptOutWarning is the plain-mode half
// of "the opt-out path launches, sets nothing, and says so in the
// output": printSwarmPlan is what runSwarm calls for a non-JSON launch,
// and it must print every entry in out.Warnings.
func TestPrintSwarmPlan_CarriesIdentityOptOutWarning(t *testing.T) {
	out := buildSwarmPlanOutput(&swarm.SwarmPlan{ScanDir: "/tmp"}, false)
	out.Warnings = swarmIdentityWarnings(true)

	stdout, err := captureStdout(t, func() error {
		printSwarmPlan(out)
		return nil
	})
	if err != nil {
		t.Fatalf("captureStdout: %v", err)
	}
	if !strings.Contains(stdout, swarmIdentityOptOutMessage) {
		t.Fatalf("plain-mode output = %q, want it to contain the opt-out warning", stdout)
	}
}

// TestSwarmPlanOutput_JSONCarriesIdentityOptOutWarning is the robot-mode
// counterpart: the JSON envelope printSwarmJSON encodes must carry the
// same statement plain mode prints, not a different or absent one.
func TestSwarmPlanOutput_JSONCarriesIdentityOptOutWarning(t *testing.T) {
	out := buildSwarmPlanOutput(&swarm.SwarmPlan{ScanDir: "/tmp"}, false)
	out.Warnings = swarmIdentityWarnings(true)

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var decoded struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(decoded.Warnings) != 1 || decoded.Warnings[0] != swarmIdentityOptOutMessage {
		t.Fatalf("decoded warnings = %v, want [%q]", decoded.Warnings, swarmIdentityOptOutMessage)
	}
}

// TestSwarmPlanOutput_JSONOmitsWarningsWhenEnabled guards against the
// warnings field leaking a stale or empty-but-present entry into every
// ordinary (non-opt-out) launch's JSON output.
func TestSwarmPlanOutput_JSONOmitsWarningsWhenEnabled(t *testing.T) {
	out := buildSwarmPlanOutput(&swarm.SwarmPlan{ScanDir: "/tmp"}, false)
	out.Warnings = swarmIdentityWarnings(false)

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(data), "warnings") {
		t.Fatalf("JSON output = %s, want no \"warnings\" key when the identity layer is enabled", data)
	}
}

// TestSwarmIdentityConflictOutput_RobotModeShape is bd-fug's robot-mode
// acceptance criterion: a structured error with its own error_code and a
// hint, built via robot.NewErrorResponse. plain mode returns the same
// underlying error from validateSwarmIdentityEnv via fmt.Errorf — the two
// therefore agree on exactly when they fire because both are driven by the
// same validateSwarmIdentityEnv call in runSwarm.
func TestSwarmIdentityConflictOutput_RobotModeShape(t *testing.T) {
	plan := planWithPaneIndices(map[string][]int{"cc_agents_1": {1, 1}})
	plainErr := validateSwarmIdentityEnv(plan, false)
	if plainErr == nil {
		t.Fatal("validateSwarmIdentityEnv returned nil, want the duplicate-name conflict")
	}

	out := swarmIdentityConflictOutput(swarmOptions{ScanDir: "/dp", DryRun: true}, plainErr)

	if out.Success {
		t.Error("Success = true, want false for a conflict envelope")
	}
	if out.ErrorCode != swarmIdentityConflictErrorCode {
		t.Errorf("ErrorCode = %q, want %q", out.ErrorCode, swarmIdentityConflictErrorCode)
	}
	if out.Hint == "" {
		t.Error("Hint is empty, want actionable guidance")
	}
	if out.Error != plainErr.Error() {
		t.Errorf("Error = %q, want the plain-mode error text %q — the two paths must agree on what failed", out.Error, plainErr.Error())
	}
	if out.Allocations == nil || out.Sessions == nil {
		t.Error("Allocations/Sessions must be present-but-empty arrays, not absent, per the JSON field semantics convention")
	}
}

// TestApplySwarmIdentityEnv_RecordsTheLayerOnThePlan is the direction the
// other tests in this file do not cover: they all prove the opt-out path
// stays quiet and the conflict path refuses, and every one of them passes
// with the layer switched off entirely. The plan's IdentityEnvEnabled is
// what the orchestrator reads to decide whether a pane gets equipped at
// all, so a launch that never sets it equips nothing while every other
// assertion here stays green.
func TestApplySwarmIdentityEnv_RecordsTheLayerOnThePlan(t *testing.T) {
	tests := []struct {
		name   string
		optOut bool
		want   bool
	}{
		{name: "default launch equips its panes", optOut: false, want: true},
		{name: "opt-out launch equips nothing", optOut: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planWithPaneIndices(map[string][]int{"cc_agents_1": {1, 2}})
			if err := applySwarmIdentityEnv(plan, tt.optOut); err != nil {
				t.Fatalf("applySwarmIdentityEnv returned %v, want nil for a valid plan", err)
			}
			if plan.IdentityEnvEnabled != tt.want {
				t.Errorf("plan.IdentityEnvEnabled = %v, want %v", plan.IdentityEnvEnabled, tt.want)
			}
		})
	}
}

// TestApplySwarmIdentityEnv_LeavesThePlanUnequippedOnConflict pins the
// order of the two steps: a plan that fails validation must not be marked
// as equipped on its way out, or a caller that ignored the error would
// launch believing the layer is on.
func TestApplySwarmIdentityEnv_LeavesThePlanUnequippedOnConflict(t *testing.T) {
	plan := planWithPaneIndices(map[string][]int{"cc_agents_1": {1, 1}})
	if err := applySwarmIdentityEnv(plan, false); err == nil {
		t.Fatal("applySwarmIdentityEnv returned nil, want the duplicate-name conflict")
	}
	if plan.IdentityEnvEnabled {
		t.Error("plan.IdentityEnvEnabled = true after a refused validation, want false")
	}
}
