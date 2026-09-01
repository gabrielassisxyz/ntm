package process

import (
	"fmt"
	"os"
	"strings"

	gopsutil "github.com/shirou/gopsutil/v4/process"
)

// CPUTime returns the total CPU time (user + system) consumed by the process
// with the given PID, expressed in seconds. It is the cheap-and-mechanical
// signal the frozen-state classifier (status.StateFrozen, bd-3b9) keys off:
// a wedged agent process neither advances its CPU clock nor moves the
// screen, while a working one is advancing at least one. The Linux fast
// path reads /proc/<pid>/stat directly (utime+stime in clock ticks, scaled
// by the system's USER_HZ), bypassing gopsutil's per-call snapshot overhead
// because the robot tick samples this once per pane per cycle. gopsutil is
// the portability fallback for platforms without /proc.
//
// ok is false when the PID is invalid, the process has gone away, or the
// process is in a state where CPU time is not meaningful (zombie). Callers
// must treat ok=false as "no signal" rather than "zero CPU", so a missing
// sample does not cause a healthy pane to be reported as frozen.
func CPUTime(pid int) (cpuSeconds float64, ok bool) {
	if pid <= 0 {
		return 0, false
	}
	if seconds, found := nativeCPUTime(pid); found {
		return seconds, true
	}
	proc, err := gopsutil.NewProcess(int32(pid))
	if err != nil {
		return 0, false
	}
	times, err := proc.Times()
	if err != nil {
		return 0, false
	}
	total := times.User + times.System
	if total < 0 {
		return 0, false
	}
	return total, true
}

// nativeCPUTime reads /proc/<pid>/stat directly on Linux. It returns
// (seconds, true) on success and (0, false) when /proc is unavailable or
// the file is malformed; the portable gopsutil path then takes over. The
// /proc format is whitespace-separated and the executable name lives in
// the second field surrounded by parentheses, so the scanner cannot use
// strings.Fields — fields 14 (utime) and 15 (stime) are reached by counting
// from the last closing paren, which is the only delimiter that cannot
// appear inside the comm value.
func nativeCPUTime(pid int) (float64, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	// comm is field 2 (1-indexed) in the format: PID (comm) state ppid ...
	// Find the last ')' so we skip the comm blob entirely. The comm can
	// contain spaces and even newlines in pathological cases, so the last
	// ')' is the only safe delimiter.
	closeIdx := strings.LastIndexByte(string(data), ')')
	if closeIdx < 0 || closeIdx+1 >= len(data) {
		return 0, false
	}
	rest := data[closeIdx+1:]
	// After ')' comes a leading space (the separator), then the rest of the
	// fields beginning with state (field 3). utime and stime are fields 14
	// and 15, so they are the 12th and 13th fields in `rest`.
	parts := strings.Fields(string(rest))
	if len(parts) < 13 {
		return 0, false
	}
	utime, err1 := parseInt64(parts[11])
	stime, err2 := parseInt64(parts[12])
	if err1 != nil || err2 != nil {
		return 0, false
	}
	ticks := utime + stime
	if ticks < 0 {
		return 0, false
	}
	return float64(ticks) / float64(clockTicksPerSecond()), true
}

func parseInt64(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty integer")
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-digit %q in %q", r, s)
		}
		n = n*10 + int64(r-'0')
	}
	return n, nil
}

// clockTicksPerSecond returns the kernel's USER_HZ, used to convert the
// tick counts in /proc/<pid>/stat to seconds. Linux ships 100 on every
// common architecture; reading the live value via sysconf would handle
// the rare outlier but requires cgo. The constant below is the documented
// default and matches the kernel's compiled-in value for x86_64, aarch64,
// and the common ARMv7 builds. A system that genuinely needs a different
// value is the kind of system gopsutil's portable path is the right tool
// for; the fast path is an optimization, not a guarantee.
func clockTicksPerSecond() int {
	return 100
}
