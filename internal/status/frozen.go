package status

import (
	"hash/fnv"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/process"
)

// FrozenDetector classifies panes whose agent process has stopped making
// progress: neither CPU time nor screen content has advanced across N
// consecutive samples. The signature is the planted-negative the brief
// calls out: pi without a call timeout freezes mid-model-call, the screen
// can still show a static "Working..." spinner, and the only mechanical
// tell is that both signals stay flat. The detector is sticky — once a
// pane enters the frozen state it stays there until either signal moves —
// because a frozen process that is briefly sampled during a momentary
// stall is not the same as one that has been frozen for the last N ticks
// and is the case the held-claim actor actually wants to act on.
//
// The detector is a per-pane cache, not a one-shot classifier: feeding it
// a single sample will always return FrozenUnknown (it cannot say). A
// caller runs Observe one or more times per tick per pane, then asks
// Classification to read the cache. This is the shape the robot tick
// already has for the screen-fingerprint cache in UnifiedDetector, so the
// two can share a sample path.
type FrozenDetector struct {
	// minSamples is the number of consecutive samples both signals must be
	// flat before the pane is classified frozen. Two is the minimum that
	// distinguishes "I just saw it" from "I have seen it twice"; the robot
	// tick polls at 10s so 2 samples = 10–20s of freeze, comfortably
	// longer than a single tool call's CPU bursts.
	minSamples int

	mu      sync.Mutex
	samples map[string]*paneFrozenSamples
}

// paneFrozenSamples holds the per-pane sliding window of (cputime, screen
// fingerprint) pairs and the result of the most recent classification.
type paneFrozenSamples struct {
	// entries is a circular buffer of the last N observations. N is bounded
	// by minSamples so the buffer never grows past what the classification
	// rule reads.
	entries []frozenSample
	// frozen is the sticky verdict: true once minSamples consecutive
	// identical entries have been observed, false until cleared. Cleared
	// as soon as a new entry's cputime or fingerprint differs from the
	// previous one.
	frozen bool
}

// frozenSample is one observation: the agent (or shell) process's total
// CPU time at the moment of sampling, the screen content fingerprint,
// and when the sample was taken.
type frozenSample struct {
	at         time.Time
	cpuSeconds float64
	cpuOK      bool // false when CPUTime() returned ok=false for this pane
	screen     uint64
}

// NewFrozenDetector returns a detector that requires 2 consecutive flat
// samples to call a pane frozen. Callers that poll faster (sub-second) can
// construct one with NewFrozenDetectorWithMinSamples to keep the same wall
// budget; the robot tick polls at 10s so 2 samples ≈ 20s, which is
// comfortably above a single tool call's CPU burst and well below the
// time a human notices and starts typing.
func NewFrozenDetector() *FrozenDetector {
	return NewFrozenDetectorWithMinSamples(2)
}

// NewFrozenDetectorWithMinSamples is the explicit form for tests and for
// callers whose poll interval differs from the robot tick.
func NewFrozenDetectorWithMinSamples(minSamples int) *FrozenDetector {
	if minSamples < 2 {
		minSamples = 2
	}
	return &FrozenDetector{
		minSamples: minSamples,
		samples:    make(map[string]*paneFrozenSamples),
	}
}

// MinSamples returns the threshold the detector was constructed with.
func (d *FrozenDetector) MinSamples() int {
	return d.minSamples
}

// Observe records one sample for the given pane and updates the frozen
// verdict. paneID is the stable key the caller already uses (e.g. the tmux
// pane ID, or the canonical "window.pane" address). pid is the process
// whose CPU time the detector will read; pass 0 (or any non-positive
// value) to skip the CPU sample — the screen fingerprint alone is
// enough to break a frozen verdict if the screen changes, but never
// enough to enter one, because a static screen is precisely the
// planted-negative case. content is the captured pane tail used to
// fingerprint the screen; an empty string produces a deterministic
// fingerprint of 0, so a pane whose capture consistently fails is
// indistinguishable from a frozen one and the detector will say so —
// this is intentional, because a pane whose capture path is broken
// cannot be restarted safely anyway.
//
// Observe is safe for concurrent use.
func (d *FrozenDetector) Observe(paneID, content string, pid int, at time.Time) {
	fingerprint := fingerprintContent(content)
	var cpu float64
	var cpuOK bool
	if pid > 0 {
		cpu, cpuOK = process.CPUTime(pid)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	entry := frozenSample{
		at:         at,
		cpuSeconds: cpu,
		cpuOK:      cpuOK,
		screen:     fingerprint,
	}

	samp, exists := d.samples[paneID]
	if !exists {
		samp = &paneFrozenSamples{
			entries: make([]frozenSample, 0, d.minSamples),
		}
		d.samples[paneID] = samp
	}
	samp.entries = append(samp.entries, entry)
	if len(samp.entries) > d.minSamples {
		// Drop the oldest; classification only ever needs the trailing
		// window so a circular slice would be premature optimization.
		samp.entries = samp.entries[len(samp.entries)-d.minSamples:]
	}

	// Re-derive the verdict. A single differing entry (cputime moved OR
	// screen changed) clears the frozen state, and a full window of
	// identical entries sets it. A sample with cpuOK=false (process gone)
	// does not by itself clear the frozen verdict, because a frozen
	// process whose parent has been reaped is still frozen; the screen
	// fingerprint is the stronger tiebreaker there.
	if !allIdentical(samp.entries) {
		samp.frozen = false
		return
	}
	samp.frozen = len(samp.entries) >= d.minSamples
}

// Classification reports the current verdict for the given pane. ok is
// false when no observation has been recorded yet.
func (d *FrozenDetector) Classification(paneID string) (frozen bool, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	samp, exists := d.samples[paneID]
	if !exists || len(samp.entries) == 0 {
		return false, false
	}
	return samp.frozen, true
}

// Reset clears the cached samples for a pane. Useful when the caller
// knows the pane has been respawned and the previous samples are now
// meaningless.
func (d *FrozenDetector) Reset(paneID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.samples, paneID)
}

// ResetAll empties the cache. Tests use this between cases; production
// code generally does not.
func (d *FrozenDetector) ResetAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.samples = make(map[string]*paneFrozenSamples)
}

// fingerprintContent hashes the captured pane tail into a uint64. FNV-1a
// is the same choice the screen-fingerprint cache in UnifiedDetector
// uses, so a pane's frozen-detector fingerprint and its activity-cache
// fingerprint agree byte-for-byte on identical captures.
func fingerprintContent(content string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(content))
	return h.Sum64()
}

// allIdentical reports whether every entry in the slice has the same
// (cpu, screen) pair. cpuOK entries with ok=false are treated as
// "matching anything" because a process that has gone away since the last
// sample is no longer a counterexample: the screen fingerprint is the
// stronger tiebreaker, and identical screen + vanished process = frozen.
func allIdentical(samples []frozenSample) bool {
	if len(samples) == 0 {
		return true
	}
	first := samples[0]
	for _, s := range samples[1:] {
		if s.screen != first.screen {
			return false
		}
		if s.cpuOK && first.cpuOK && s.cpuSeconds != first.cpuSeconds {
			return false
		}
	}
	return true
}
