//go:build integration

package tmux

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A bare session name is not a session target. tmux resolves a target with no
// colon as a WINDOW of the current session first, matching window names by
// prefix, and only falls through to a session when nothing matches. So when the
// current session happens to hold a window whose name starts with the session
// name being asked about, `list-panes -s -t <name>` enumerates the current
// session's panes instead — with no error anywhere.
//
// That is the shape that misled `ntm spawn`: it counts the panes it gets back to
// decide how many to create, read another session's panes as its own, created
// none, and launched every agent into windows belonging to whoever was attached.
func TestRealGetPanesResolvesTheNamedSessionNotAWindowOfTheCurrentOne(t *testing.T) {
	skipIfNoTmux(t)

	named := uniqueSessionName("target-named")
	t.Cleanup(func() { cleanupSession(t, named) })
	if err := CreateSession(named, t.TempDir()); err != nil {
		t.Fatalf("CreateSession(%s) failed: %v", named, err)
	}
	// Two panes, so a wrong answer differs in count and not only in identity.
	if _, err := SplitWindow(named, t.TempDir()); err != nil {
		t.Fatalf("SplitWindow(%s) failed: %v", named, err)
	}

	decoy := uniqueSessionName("target-decoy")
	t.Cleanup(func() { cleanupSession(t, decoy) })
	if err := CreateSession(decoy, t.TempDir()); err != nil {
		t.Fatalf("CreateSession(%s) failed: %v", decoy, err)
	}
	// The collision: a window of the current session carrying the other
	// session's name. Nothing forbids this, and nothing warns about it.
	if _, err := DefaultClient.Run("rename-window", "-t", decoy+":", named); err != nil {
		t.Fatalf("rename-window in %s failed: %v", decoy, err)
	}
	makeCurrentSession(t, decoy)
	time.Sleep(100 * time.Millisecond)

	want := paneIDsOfSession(t, named)
	if len(want) != 2 {
		t.Fatalf("setup: session %s has %d pane(s), want 2", named, len(want))
	}

	panes, err := GetPanes(named)
	if err != nil {
		t.Fatalf("GetPanes(%s) failed: %v", named, err)
	}
	if len(panes) != len(want) {
		t.Fatalf("GetPanes(%s) returned %d pane(s), want %d — the target resolved to a window of the current session",
			named, len(panes), len(want))
	}
	belongs := map[string]bool{}
	for _, id := range want {
		belongs[id] = true
	}
	for _, p := range panes {
		if !belongs[p.ID] {
			t.Fatalf("GetPanes(%s) returned pane %s, which belongs to another session", named, p.ID)
		}
	}
}

// makeCurrentSession points $TMUX at a session, which is where tmux reads the
// current session from when it resolves an unqualified target.
func makeCurrentSession(t *testing.T, session string) {
	t.Helper()
	out, err := DefaultClient.Run("display-message", "-p", "-t", session+":", "#{socket_path},#{pid},#{session_id}")
	if err != nil {
		t.Fatalf("display-message for %s failed: %v", session, err)
	}
	fields := strings.Split(strings.TrimSpace(out), ",")
	if len(fields) != 3 {
		t.Fatalf("display-message returned %q, want socket,pid,session_id", out)
	}
	// $TMUX carries the session id without its leading '$'.
	t.Setenv("TMUX", fmt.Sprintf("%s,%s,%s", fields[0], fields[1], strings.TrimPrefix(fields[2], "$")))
}

// paneIDsOfSession lists a session's pane IDs through an unambiguous target, so
// the expectation is not built with the call under test.
func paneIDsOfSession(t *testing.T, session string) []string {
	t.Helper()
	out, err := DefaultClient.Run("list-panes", "-s", "-t", session+":", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("reference list-panes for %s failed: %v", session, err)
	}
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			ids = append(ids, line)
		}
	}
	return ids
}
