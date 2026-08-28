package tmuxenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// OwnerFile names the marker written inside every isolated tmux test root,
// holding the pid of the test process that created it.
//
// The teardown that kills an isolated server runs from TestMain and t.Cleanup,
// and neither runs when the test binary is killed — a `go test` timeout, a
// Ctrl+C, a panic that takes the process down. The tmux server it started is
// its own process and survives all three, holding a socket in a directory that
// is then removed, so it can never be reached by name again. Eight such servers
// were found alive on one machine, up to 27 hours old, each with a deleted
// working directory.
//
// They are not inert. tmux-continuum refuses to arm its save hook when it
// counts more tmux processes than the current server has clients, so a handful
// of leaked test servers silently switches off session saving for the developer
// — a failure that reports nothing and is only noticed when a restore comes
// back with the wrong day's layout.
//
// Nothing inside a process can guarantee its own cleanup, so this sweeps at
// START instead: a later run reaps what an earlier one could not. Ownership is
// a pid rather than an age, because an age threshold has to choose between
// reaping a long e2e run's live root and leaving yesterday's orphan behind.
const OwnerFile = "owner.pid"

// WriteOwner records the calling process as the owner of an isolated root.
func WriteOwner(root string) error {
	pid := []byte(strconv.Itoa(os.Getpid()))
	return os.WriteFile(filepath.Join(root, OwnerFile), pid, 0o600)
}

// ownerAlive reports whether the root's recorded owner is still running.
//
// A root with no marker is treated as abandoned: markers are written at
// creation, so the only roots without one predate this sweep or belong to a
// creation that died between MkdirTemp and WriteOwner.
func ownerAlive(root string) bool {
	raw, err := os.ReadFile(filepath.Join(root, OwnerFile))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false
	}
	// Signal 0 performs the permission and existence checks without delivering
	// anything. EPERM means the pid is live and owned by somebody else, which
	// is still live.
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// SweepStaleRoots kills the tmux server of every isolated test root whose owner
// has exited, and removes the root. It reports how many it reaped.
//
// Errors are returned rather than swallowed, but one unreachable root never
// stops the others: a sweep that gives up on the first failure would leave the
// rest of the leak in place, which is the situation it exists to end.
func SweepStaleRoots(tmuxBinary string) (int, error) {
	bases := make([]string, 0, len(Candidates()))
	for _, candidate := range Candidates() {
		bases = append(bases, candidate.Path)
	}
	return SweepStaleRootsIn(bases, tmuxBinary)
}

// SweepStaleRootsIn is SweepStaleRoots over an explicit set of base
// directories.
//
// It exists so the sweep's own tests can be scoped to a temp directory. A test
// that called SweepStaleRoots would reap this machine's real /tmp and /var/tmp
// as a side effect — which is how the first run of these tests removed 1528
// leaked roots and reported "want 3, got 1528".
func SweepStaleRootsIn(bases []string, tmuxBinary string) (int, error) {
	var problems []error
	reaped := 0

	for _, base := range bases {
		if base == "" {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(base, Pattern))
		if err != nil {
			problems = append(problems, fmt.Errorf("glob %q: %w", base, err))
			continue
		}
		for _, root := range matches {
			info, err := os.Stat(root)
			if err != nil || !info.IsDir() || ownerAlive(root) {
				continue
			}
			if err := reapRoot(tmuxBinary, root); err != nil {
				problems = append(problems, err)
				continue
			}
			reaped++
		}
	}

	return reaped, errors.Join(problems...)
}

// reapRoot stops the server holding the root's socket, then removes the root.
//
// The kill is attempted before the removal and its failure is not fatal: a root
// whose server already exited has nothing to kill, and that is the common case.
// Removing the directory regardless is what stops the same root being retried
// on every future run.
func reapRoot(tmuxBinary, root string) error {
	if tmuxBinary != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		cmd := exec.CommandContext(ctx, tmuxBinary, "kill-server")
		cmd.Env = append(os.Environ(), "TMUX=", "TMUX_PANE=", "TMUX_TMPDIR="+root)
		_ = cmd.Run()
		cancel()
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove stale tmux test root %q: %w", root, err)
	}
	return nil
}
