//go:build e2e
// +build e2e

// Per-pane account pinning (bd-jyy). Spawning two cc panes with different
// accounts in ONE `ntm spawn` must put each pane on its own account, verified
// from the process environment (SHALLOW_PROFILE/HOME as set by the
// claude-account wrapper's caam shallow-spawn), with config.toml untouched.
//
// The real claude-account wrapper cannot run here (it needs caam and real
// credentials), so the fixture substitutes a shim with the same contract: it
// takes the account as argv[1] and pins it into the pane process environment,
// exactly like the real wrapper's caam shallow-spawn does. What is exercised
// for real is the part this bead fixes: the spec parser, the template
// renderer, and the spawn command line that carry the account to the pane.

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

const (
	fakeAccountTemplate = `claude-account {{shellQuote (.Account | default "bianca")}}{{if .Model}} --model {{shellQuote .Model}}{{end}}`
	fakeAccountA        = "gmail"
	fakeAccountB        = "primary"
)

func TestE2EPerPaneAccount(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}

	runtimeRoot := t.TempDir()
	tmuxRoot := testutil.ShortTmuxTempDir(t)
	projectsBase := filepath.Join(runtimeRoot, "projects")
	fakeBin := filepath.Join(runtimeRoot, "bin")
	for _, path := range []string{projectsBase, fakeBin, filepath.Join(runtimeRoot, "home"), filepath.Join(runtimeRoot, "config"), filepath.Join(runtimeRoot, "data")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("create fixture path %s: %v", path, err)
		}
	}

	// A claude-account shim with the real wrapper's contract: argv[1] is the
	// account, and the pane's process environment must end up naming it. The
	// real wrapper resolves through caam shallow-spawn which sets exactly
	// SHALLOW_PROFILE and HOME per account.
	shim := strings.Join([]string{
		"#!/bin/sh",
		"account=\"${1:-}\"",
		"case \"$account\" in",
		"  gmail|primary|bianca) ;;",
		"  *) printf 'claude-account: unknown account %s\\n' \"$account\" >&2; exit 2 ;;",
		"esac",
		"SHALLOW_PROFILE=\"$account\" HOME=\"/fake-home-$account\" exec sleep 300",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(fakeBin, "claude-account"), []byte(shim), 0o700); err != nil {
		t.Fatalf("write fake claude-account shim: %v", err)
	}

	session := fmt.Sprintf("ntm-e2e-account-%d-%d", os.Getpid(), time.Now().UnixNano())
	// The pane environment must NOT inherit anything from the test process:
	// this harness may itself run under `claude-account <account>`, so a
	// leaked SHALLOW_PROFILE/AGENT_SCOPE would put an account on every pane
	// and the per-pane assertion below could never fail — the exact silent
	// false-green the bead's mutation rule warns about. Build the env from a
	// fixed whitelist instead.
	env := []string{
		"HOME=" + filepath.Join(runtimeRoot, "home"),
		"XDG_CONFIG_HOME=" + filepath.Join(runtimeRoot, "config"),
		"XDG_DATA_HOME=" + filepath.Join(runtimeRoot, "data"),
		"TMUX_TMPDIR=" + tmuxRoot,
		"NO_COLOR=1",
		"TERM=xterm-256color",
		"SHELL=/bin/sh",
		"PATH=" + fakeBin + string(os.PathListSeparator) + "/usr/bin:/bin",
	}

	configPath := filepath.Join(runtimeRoot, "config", "ntm", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("create ntm config dir: %v", err)
	}
	config := "projects_base = " + strconv.Quote(projectsBase) + "\n\n[agents]\nclaude = " + strconv.Quote(fakeAccountTemplate) + "\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write isolated ntm config: %v", err)
	}

	tmuxConf := filepath.Join(runtimeRoot, "tmux.conf")
	if err := os.WriteFile(tmuxConf, []byte("set -g base-index 0\nsetw -g pane-base-index 0\nset -g status off\n"), 0o600); err != nil {
		t.Fatalf("write isolated tmux config: %v", err)
	}

	// Start the isolated tmux server with our config up front: `ntm spawn`
	// creates its session on the server already listening in TMUX_TMPDIR, and
	// the server must be running so the pane-account probe below can use the
	// same config.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	startCmd := exec.CommandContext(ctx, tmuxPath, "-f", tmuxConf, "start-server")
	startCmd.Env = env
	if output, err := startCmd.CombinedOutput(); err != nil {
		t.Fatalf("start isolated tmux server: %v: %s", err, output)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		killCmd := exec.CommandContext(shutdownCtx, tmuxPath, "-f", tmuxConf, "kill-server")
		killCmd.Env = env
		_ = killCmd.Run()
	})

	// One spawn command, two cc panes, two different accounts — the exact
	// requirement the config comment now documents (bd-jyy). config.toml is
	// written once above and never touched again. The project directory must
	// pre-exist (spawn resolves it from projects_base); create it once.
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("create spawn project dir: %v", err)
	}
	spawnArgs := []string{"spawn", session, "--cc", "1:sonnet::" + fakeAccountA, "--cc", "1:sonnet::" + fakeAccountB}
	cmd := exec.CommandContext(ctx, ntmPath, spawnArgs...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			t.Fatalf("ntm spawn timed out: %s", strings.Join(spawnArgs, " "))
		}
		t.Fatalf("ntm spawn failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}

	// Wait for the two panes to come up and read each pane's account from the
	// process environment, walking the child chain (tmux's pane_pid may be the
	// launching shell rather than the shim that exec'd sleep). The spawn's own
	// user pane (a plain shell) has no account and is ignored.
	accounts := make(map[int]string)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		listCmd := exec.CommandContext(ctx, tmuxPath, "-f", tmuxConf, "list-panes", "-s", "-t", session, "-F", "#{window_index}.#{pane_index}:#{pane_pid}")
		listCmd.Env = env
		output, listErr := listCmd.Output()
		if listErr == nil {
			accounts = make(map[int]string)
			for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
				fields := strings.SplitN(line, ":", 3)
				if len(fields) < 2 {
					continue
				}
				pid, parseErr := strconv.Atoi(strings.TrimSpace(fields[1]))
				if parseErr != nil || pid <= 0 {
					continue
				}
				if account, ok := processAccount(pid); ok {
					accounts[pid] = account
				}
			}
			if len(accounts) == 2 {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for panes: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}

	if len(accounts) != 2 {
		t.Fatalf("found %d panes with an account in their environment, want 2; tmux said: %v", len(accounts), accounts)
	}
	var got []string
	for pid, account := range accounts {
		got = append(got, strconv.Itoa(pid)+"="+account)
	}
	sort.Strings(got)
	t.Logf("pane accounts from /proc/<pid>/environ: %s", strings.Join(got, " "))

	seen := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		seen[account] = true
	}
	if !seen[fakeAccountA] || !seen[fakeAccountB] {
		t.Fatalf("the two panes must carry DIFFERENT accounts (%s and %s); got %v", fakeAccountA, fakeAccountB, accounts)
	}
	if len(seen) != 2 {
		t.Fatalf("both panes ran under the same account %v — per-pane account pinning is not active", accounts)
	}
}

// processAccount walks the process tree rooted at pid (children only; the
// pane's own pid may be a shell that exec'd the real process) and returns the
// claude-account profile name found in a process environment, if any.
func processAccount(pid int) (string, bool) {
	visited := make(map[int]bool)
	var walk func(int) (string, bool)
	walk = func(current int) (string, bool) {
		if current <= 0 || visited[current] {
			return "", false
		}
		visited[current] = true
		if account := environAccount(current); account != "" {
			return account, true
		}
		children := childPIDs(current)
		for _, child := range children {
			if account, ok := walk(child); ok {
				return account, true
			}
		}
		return "", false
	}
	return walk(pid)
}

// environAccount returns the SHALLOW_PROFILE value from /proc/<pid>/environ
// when present (the pane's own HOME is derived from it in the real wrapper).
func environAccount(pid int) string {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return ""
	}
	for _, entry := range strings.Split(string(raw), "\x00") {
		if strings.HasPrefix(entry, "SHALLOW_PROFILE=") {
			return strings.TrimPrefix(entry, "SHALLOW_PROFILE=")
		}
	}
	return ""
}

func childPIDs(pid int) []int {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
	if err != nil {
		return nil
	}
	var result []int
	for _, field := range strings.Fields(string(raw)) {
		if child, parseErr := strconv.Atoi(field); parseErr == nil && child > 0 {
			result = append(result, child)
		}
	}
	return result
}
