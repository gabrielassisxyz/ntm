package tmuxenv

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// deadPID returns a pid that is certainly not running.
//
// A process is started and reaped rather than a large constant being assumed
// free: pid_max is configurable, and a hardcoded pid that happens to be live
// would make this test pass for the wrong reason.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exeTrue(t)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start throwaway process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for throwaway process: %v", err)
	}
	return pid
}

func makeRoot(t *testing.T, base, name, owner string) string {
	t.Helper()
	root := filepath.Join(base, name)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	// A file inside proves RemoveAll reached the contents, not just the dir.
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write marker in %s: %v", name, err)
	}
	if owner != "" {
		if err := os.WriteFile(filepath.Join(root, OwnerFile), []byte(owner), 0o600); err != nil {
			t.Fatalf("write owner in %s: %v", name, err)
		}
	}
	return root
}

func TestSweepStaleRootsReapsAbandonedAndSparesLive(t *testing.T) {
	base := t.TempDir()
	t.Setenv(TestTempBaseEnv, base)

	dead := makeRoot(t, base, "ntm-tmux-test-dead", strconv.Itoa(deadPID(t)))
	unmarked := makeRoot(t, base, "ntm-tmux-test-unmarked", "")
	garbage := makeRoot(t, base, "ntm-tmux-test-garbage", "not-a-pid")
	live := makeRoot(t, base, "ntm-tmux-test-live", strconv.Itoa(os.Getpid()))
	// A directory that does not match Pattern must be invisible to the sweep,
	// or a shared temp base becomes a hazard rather than a place to work in.
	bystander := makeRoot(t, base, "somebody-elses-dir", "")

	// The empty binary skips the kill and exercises the removal alone; killing a
	// real server is covered by the isolation helper's own teardown.
	reaped, err := SweepStaleRootsIn([]string{base}, "")
	if err != nil {
		t.Fatalf("SweepStaleRootsIn() error = %v", err)
	}
	if reaped != 3 {
		t.Errorf("reaped = %d, want 3", reaped)
	}

	for _, gone := range []string{dead, unmarked, garbage} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep (stat err = %v)", filepath.Base(gone), err)
		}
	}
	for _, kept := range []string{live, bystander} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was reaped and should not have been: %v", filepath.Base(kept), err)
		}
	}
}

func TestWriteOwnerMakesARootLookLive(t *testing.T) {
	base := t.TempDir()
	t.Setenv(TestTempBaseEnv, base)

	root := makeRoot(t, base, "ntm-tmux-test-claimed", "")
	if ownerAlive(root) {
		t.Fatal("an unmarked root reported a live owner")
	}
	if err := WriteOwner(root); err != nil {
		t.Fatalf("WriteOwner() error = %v", err)
	}
	if !ownerAlive(root) {
		t.Error("a root claimed by this process did not report a live owner")
	}

	reaped, err := SweepStaleRootsIn([]string{base}, "")
	if err != nil {
		t.Fatalf("SweepStaleRootsIn() error = %v", err)
	}
	if reaped != 0 {
		t.Errorf("reaped = %d, want 0 — the sweep took a root this process owns", reaped)
	}
}
