package status

import (
	"testing"
	"time"
)

// TestFrozenDetector_UnchangedCPUAndScreenAcrossSamples covers the headline
// case: a pane whose process cputime and screen fingerprint are both flat
// across N consecutive samples must classify as frozen. This is the case
// the brief opens with (pi's documented hang class): the screen can show a
// static "Working..." spinner while the model call is wedged.
func TestFrozenDetector_UnchangedCPUAndScreenAcrossSamples(t *testing.T) {
	det := NewFrozenDetectorWithMinSamples(2)
	const content = "Working... (wedged spinner that never moves)"
	const fakeCPUSeconds = 1.234

	// Two samples with identical content and identical CPU. The detector
	// does not call out to the OS; it reads whatever the caller supplies
	// via Observe's fingerprint + the (cputime, cpuOK) pair the call
	// computes. Passing 0 for pid therefore does not work — Observe will
	// skip the CPU sample. We test through the call site the robot tick
	// uses, by writing a tiny harness that bypasses the OS read.
	det.observeForTest("pane-1", content, fakeCPUSeconds, true, time.Unix(1_700_000_000, 0))
	det.observeForTest("pane-1", content, fakeCPUSeconds, true, time.Unix(1_700_000_010, 0))

	frozen, ok := det.Classification("pane-1")
	if !ok {
		t.Fatalf("Classification(pane-1) ok=false, want true after 2 samples")
	}
	if !frozen {
		t.Fatalf("Classification(pane-1) = (false, _), want (true, _) — both CPU and screen flat across N samples must classify frozen")
	}
}

// TestFrozenDetector_CPUAdvancingWithStaticScreen is the planted negative:
// the case that must NEVER classify frozen. A long generation produces
// CPU activity (the model is doing work) but no new screen lines until a
// completion burst, so the screen fingerprint is flat while CPU is
// advancing. Killing this pane would be destructive; the detector must
// keep it out of the frozen bucket.
func TestFrozenDetector_CPUAdvancingWithStaticScreen(t *testing.T) {
	det := NewFrozenDetectorWithMinSamples(2)
	const content = "Working... (long generation, no new lines yet)"

	det.observeForTest("pane-2", content, 1.0, true, time.Unix(1_700_000_000, 0))
	det.observeForTest("pane-2", content, 1.7, true, time.Unix(1_700_000_010, 0)) // +0.7s CPU
	det.observeForTest("pane-2", content, 2.4, true, time.Unix(1_700_000_020, 0)) // +0.7s CPU

	frozen, ok := det.Classification("pane-2")
	if !ok {
		t.Fatalf("Classification(pane-2) ok=false, want true after 3 samples")
	}
	if frozen {
		t.Fatalf("Classification(pane-2) = (true, _), want (false, _) — CPU advancing with static screen is the planted negative and must NEVER classify frozen")
	}
}

// TestFrozenDetector_SingleSampleIsUnknown pins the "no signal" arm: with
// fewer than minSamples entries, the detector must not claim frozen even
// if every signal it has looks flat. The first observation is a snapshot,
// not a comparison.
func TestFrozenDetector_SingleSampleIsUnknown(t *testing.T) {
	det := NewFrozenDetectorWithMinSamples(2)
	det.observeForTest("pane-3", "anything", 0.5, true, time.Unix(1_700_000_000, 0))
	frozen, _ := det.Classification("pane-3")
	if frozen {
		t.Errorf("Classification(pane-3) frozen=true after 1 sample; detector must require minSamples observations before claiming frozen")
	}
}

// TestFrozenDetector_ScreenChangeClearsFrozen proves the verdict is
// correctly reset once the screen moves, even if CPU stays flat. A
// previously frozen pane that suddenly paints a new line is no longer
// frozen.
func TestFrozenDetector_ScreenChangeClearsFrozen(t *testing.T) {
	det := NewFrozenDetectorWithMinSamples(2)
	det.observeForTest("pane-4", "stuck frame", 1.0, true, time.Unix(1_700_000_000, 0))
	det.observeForTest("pane-4", "stuck frame", 1.0, true, time.Unix(1_700_000_010, 0))
	if frozen, _ := det.Classification("pane-4"); !frozen {
		t.Fatalf("precondition: pane-4 should be frozen after 2 flat samples")
	}
	det.observeForTest("pane-4", "stuck frame - and a new line", 1.0, true, time.Unix(1_700_000_020, 0))
	if frozen, _ := det.Classification("pane-4"); frozen {
		t.Fatalf("pane-4 still frozen after screen change; detector must clear on signal change")
	}
}

// TestFrozenDetector_CPUChangeClearsFrozen mirrors the screen-change arm
// for the CPU side: a frozen pane that picks up CPU activity (e.g. the
// hung model call finally returns and starts streaming) is no longer
// frozen.
func TestFrozenDetector_CPUChangeClearsFrozen(t *testing.T) {
	det := NewFrozenDetectorWithMinSamples(2)
	det.observeForTest("pane-5", "wedged", 1.0, true, time.Unix(1_700_000_000, 0))
	det.observeForTest("pane-5", "wedged", 1.0, true, time.Unix(1_700_000_010, 0))
	if frozen, _ := det.Classification("pane-5"); !frozen {
		t.Fatalf("precondition: pane-5 should be frozen after 2 flat samples")
	}
	det.observeForTest("pane-5", "wedged", 1.5, true, time.Unix(1_700_000_020, 0))
	if frozen, _ := det.Classification("pane-5"); frozen {
		t.Fatalf("pane-5 still frozen after CPU advanced; detector must clear on signal change")
	}
}

// observeForTest is a test-only entry point that injects a CPU sample
// without going through process.CPUTime. The production path always
// pairs Observe with a real OS read; tests need a way to specify exact
// CPU values to exercise the "advancing" arm of the frozen rule.
func (d *FrozenDetector) observeForTest(paneID, content string, cpuSeconds float64, cpuOK bool, at time.Time) {
	fingerprint := fingerprintContent(content)
	entry := frozenSample{
		at:         at,
		cpuSeconds: cpuSeconds,
		cpuOK:      cpuOK,
		screen:     fingerprint,
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	samp, exists := d.samples[paneID]
	if !exists {
		samp = &paneFrozenSamples{
			entries: make([]frozenSample, 0, d.minSamples),
		}
		d.samples[paneID] = samp
	}
	samp.entries = append(samp.entries, entry)
	if len(samp.entries) > d.minSamples {
		samp.entries = samp.entries[len(samp.entries)-d.minSamples:]
	}
	if !allIdentical(samp.entries) {
		samp.frozen = false
		return
	}
	samp.frozen = len(samp.entries) >= d.minSamples
}
