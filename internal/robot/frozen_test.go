package robot

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// frozenSleepHelper is the sentinel environment variable that, when set,
// turns the test binary into a long-sleeping helper process. The frozen
// tests use this to anchor the cputime-anchored half of the verdict
// against a process whose CPU clock is essentially frozen by design — a
// sleeping helper does not consume user CPU, so two CPUTime reads taken
// microseconds apart return the same value. Reading CPUTime on the test
// process itself is not a substitute, because the Go runtime / scheduler
// accounts user CPU between the two calls, which would mean even a
// well-behaved test process could spuriously register a cputime delta
// large enough to clear the frozen verdict.
const frozenSleepHelperEnv = "NTM_ROBOT_FROZEN_HELPER"

func init() {
	if os.Getenv(frozenSleepHelperEnv) == "1" {
		time.Sleep(120 * time.Second)
		os.Exit(0)
	}
}

// spawnFrozenHelper launches a child test binary in the long-sleep
// helper role and returns its PID. The child is killed when the test
// exits so the helper never leaks. On platforms where the helper
// cannot be spawned (e.g. a cross-compiled test binary that is not
// executable in the test environment) the test should be skipped at a
// higher level; the helper is the source of determinism the assertion
// depends on, so a fall-back to a flakier signal is not acceptable.
func spawnFrozenHelper(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), frozenSleepHelperEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn frozen helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid
}

// testProcessPIDForFrozen is captured at package init so the helper
// smoke-checks have a known-good PID. The cputime-anchored assertions
// do NOT use this PID — they use the sleeping helper above, which is
// the only way to guarantee a stable CPU clock across the two
// samples.
var testProcessPIDForFrozen = os.Getpid()

// TestApplyFrozenVerdict_SurfaceFlipsToFrozen is the robot-surface
// observation-path assertion (bd-3b9 criterion 3): the frozen state is
// reachable from a PaneWorkStatus through the apply-frozen step the tick
// calls. A pane whose cputime and screen fingerprint are both flat across
// the detector's window must report IsFrozen=true, with neither IsWorking
// nor IsIdle set, and the recommendation pivoted to FROZEN.
func TestApplyFrozenVerdict_SurfaceFlipsToFrozen(t *testing.T) {
	detector := statuspkg.NewFrozenDetectorWithMinSamples(2)
	const frozenContent = "⠹ Working... (this spinner is wedged, do not move)"

	// Anchor the cputime-anchored half of the verdict against a
	// long-sleeping helper process. The Go test runner itself
	// accumulates non-trivial user CPU between two consecutive calls,
	// so using the test process as the CPU source would make the test
	// depend on whatever the scheduler happens to do during the gap;
	// the sleeping helper, by design, has a near-zero CPU clock that
	// does not move between two reads.
	helperPID := spawnFrozenHelper(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	observation := statuspkg.PaneObservation{
		Pane: tmux.PaneRef{ID: "%42", WindowIndex: 0, PaneIndex: 0},
		Metadata: tmux.Pane{
			ID:      "%42",
			Index:   0,
			PID:     helperPID,
			Command: "claude",
		},
	}
	workStatus := PaneWorkStatus{
		AgentType:      "claude",
		IsWorking:      false,
		IsIdle:         true,
		Recommendation: "SAFE_TO_RESTART",
		IndicatorBasis: "idle_prompt",
	}
	applyFrozenVerdict(&workStatus, detector, observation, frozenContent, now)
	applyFrozenVerdict(&workStatus, detector, observation, frozenContent, now.Add(10*time.Second))

	if !workStatus.IsFrozen {
		t.Fatalf("IsFrozen = false after 2 flat samples; want true. workStatus = %+v", workStatus)
	}
	if workStatus.IsWorking || workStatus.IsIdle {
		t.Fatalf("frozen pane must not be working or idle; got is_working=%v is_idle=%v", workStatus.IsWorking, workStatus.IsIdle)
	}
	if workStatus.Recommendation != string(agent.RecommendFrozen) {
		t.Errorf("Recommendation = %q, want %q (FROZEN)", workStatus.Recommendation, agent.RecommendFrozen)
	}
	if workStatus.IndicatorBasis != "frozen_pane" {
		t.Errorf("IndicatorBasis = %q, want %q", workStatus.IndicatorBasis, "frozen_pane")
	}
}

// TestApplyFrozenVerdict_CPUAdvancingWithStaticScreen_NotFrozen is the
// planted-negative test on the robot surface: a pane whose CPU is
// advancing (the agent is doing real work) but whose screen is static
// (a long generation, no new lines yet) must NOT classify frozen —
// killing it would be destructive, exactly the wrong call. The surface
// test guarantees the field is_frozen stays false in that case.
func TestApplyFrozenVerdict_CPUAdvancingWithStaticScreen_NotFrozen(t *testing.T) {
	detector := statuspkg.NewFrozenDetectorWithMinSamples(2)
	const staticContent = "⠹ Working... (long generation, no new lines yet)"

	now := time.Unix(1_700_000_000, 0).UTC()
	// The CPU-advancing arm anchors against the test process itself,
	// which is guaranteed to consume user CPU during the busy-wait
	// gaps. A sleeping helper would not advance and would therefore
	// flip the verdict, exactly the wrong way for the planted
	// negative. The test process's CPU clock therefore MUST advance
	// measurably between the first sample and each subsequent
	// sample; burnCPUFor below guarantees that.
	pid := int(testProcessPIDForFrozen)
	observation := statuspkg.PaneObservation{
		Pane: tmux.PaneRef{ID: "%43", WindowIndex: 0, PaneIndex: 1},
		Metadata: tmux.Pane{
			ID:      "%43",
			Index:   1,
			PID:     pid,
			Command: "codex",
		},
	}
	workStatus := PaneWorkStatus{
		AgentType:      "codex",
		IsWorking:      true,
		IsIdle:         false,
		Recommendation: "DO_NOT_INTERRUPT",
		IndicatorBasis: "codex_live_working_indicator",
	}

	// Burn real CPU between samples so CPUTime(self) advances measurably.
	// 80ms is well above the kernel's per-tick resolution but well below
	// the test budget.
	applyFrozenVerdict(&workStatus, detector, observation, staticContent, now)
	burnCPUFor(80 * time.Millisecond)
	applyFrozenVerdict(&workStatus, detector, observation, staticContent, now.Add(10*time.Second))
	burnCPUFor(80 * time.Millisecond)
	applyFrozenVerdict(&workStatus, detector, observation, staticContent, now.Add(20*time.Second))

	if workStatus.IsFrozen {
		t.Fatalf("IsFrozen = true for a pane whose CPU advanced and screen was static; this is the planted negative. workStatus = %+v", workStatus)
	}
	if !workStatus.IsWorking {
		t.Errorf("IsWorking = false after advanced-CPU + static-screen samples; want true (the surface must preserve the parser's working verdict)")
	}
}

// TestApplyFrozenVerdict_NilDetectorIsNoOp guards the contract: when the
// caller has not supplied a detector, applyFrozenVerdict must not touch
// the workStatus. The default --robot-is-working path passes nil and
// must produce a byte-identical PaneWorkStatus to the pre-feature build.
func TestApplyFrozenVerdict_NilDetectorIsNoOp(t *testing.T) {
	workStatus := PaneWorkStatus{
		AgentType:      "claude",
		IsWorking:      true,
		IsIdle:         false,
		Recommendation: "DO_NOT_INTERRUPT",
		IndicatorBasis: "claude_live_spinner",
	}
	observation := statuspkg.PaneObservation{
		Pane:     tmux.PaneRef{ID: "%99", WindowIndex: 0, PaneIndex: 0},
		Metadata: tmux.Pane{ID: "%99", Index: 0, PID: 0},
	}
	applyFrozenVerdict(&workStatus, nil, observation, "anything", time.Now())
	if workStatus.IsFrozen {
		t.Fatalf("IsFrozen = true after applyFrozenVerdict(nil detector); must remain false")
	}
	if !workStatus.IsWorking {
		t.Fatalf("IsWorking flipped to false with nil detector; the no-op must preserve the workStatus")
	}
}

// burnCPUFor spins the calling goroutine on a tight loop for the
// requested duration so CPUTime(self) advances by an amount the
// frozen-state sampler's equality test can observe. The accumulator
// stays live across iterations so the Go compiler cannot elide the
// loop, and a runtime.Gosched() between loops lets other goroutines
// run so this test never wedges the rest of the suite.
func burnCPUFor(d time.Duration) {
	deadline := time.Now().Add(d)
	acc := uint64(0)
	for time.Now().Before(deadline) {
		for i := 0; i < 1000; i++ {
			acc++
		}
		runtime.Gosched()
	}
	_ = acc
}
