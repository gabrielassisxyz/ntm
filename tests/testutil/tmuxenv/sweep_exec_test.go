package tmuxenv

import (
	"os/exec"
	"testing"
)

// exeTrue returns a command that exits immediately and successfully, used only
// to mint a pid that is guaranteed to be dead by the time it is looked up.
func exeTrue(t *testing.T) *exec.Cmd {
	t.Helper()
	path, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("no `true` binary available to mint a dead pid: %v", err)
	}
	return exec.Command(path)
}
