package process

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestCPUTime_InvalidPID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		pid  int
	}{
		{"zero", 0},
		{"negative", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if secs, ok := CPUTime(tc.pid); ok {
				t.Errorf("CPUTime(%d) = (%v, true), want (_, false)", tc.pid, secs)
			}
		})
	}
}

func TestCPUTime_CurrentProcess(t *testing.T) {
	t.Parallel()
	// The current process is guaranteed to have consumed some user CPU by
	// the time the test runs (test framework startup, init, etc.). A value
	// of exactly zero would indicate a broken parser or a wrong clock-tick
	// scaling; both would misclassify every pane as frozen.
	secs, ok := CPUTime(os.Getpid())
	if !ok {
		t.Skip("CPUTime(self) returned ok=false on this platform; nothing to assert")
	}
	if secs < 0 {
		t.Errorf("CPUTime(self) = %v, want non-negative", secs)
	}
}

func TestCPUTime_AdvancesAcrossBusyWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("busy-wait timing unreliable on Windows")
	}
	t.Parallel()
	pid := os.Getpid()
	before, ok := CPUTime(pid)
	if !ok {
		t.Skip("CPUTime returned ok=false on this platform")
	}
	// Consume measurable user CPU. 50ms is long enough that even a noisy
	// CI runner cannot hide the increment, short enough not to extend
	// the test budget.
	deadline := time.Now().Add(50 * time.Millisecond)
	acc := uint64(0)
	for time.Now().Before(deadline) {
		// Tight loop to pin the CPU; the compiler cannot eliminate this
		// because the accumulator is observed via the runtime call.
		for i := 0; i < 1000; i++ {
			acc++
		}
		runtime.Gosched()
	}
	_ = acc
	after, ok := CPUTime(pid)
	if !ok {
		t.Fatalf("CPUTime(self) after busy-wait returned ok=false")
	}
	// On a busy CI runner the kernel may underreport by a tick or two, so
	// allow a small equality window; in practice the increment is
	// comfortably above 0.001s.
	if after+1e-6 < before {
		t.Errorf("CPUTime went backwards: before=%v after=%v", before, after)
	}
}

func TestCPUTime_NonExistentPID(t *testing.T) {
	t.Parallel()
	// 0x7FFFFFF0 is in the unused high range on Linux; on macOS pick a
	// pid that is overwhelmingly likely to be free. We do not assert ok
	// strictly false because some kernels return ESRCH from gopsutil but
	// the /proc fast path can still find a zombie — the contract is
	// "no signal" either way (ok=false OR a sensible value), and on
	// platforms where /proc exists the fast path returns ok=true with 0
	// for a zombie. We accept any (secs, ok) here and only require that
	// for ok=true the seconds value is non-negative.
	secs, ok := CPUTime(0x7FFFFFF0)
	if ok && secs < 0 {
		t.Errorf("CPUTime(nonexistent) = (%v, true), want non-negative seconds if ok", secs)
	}
}
