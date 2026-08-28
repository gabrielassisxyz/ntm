// tmux_socket_guard_test.go — static guard for bd-wkq (tmux test isolation).
//
// go test ./... has twice reached the host through test code: once by
// deleting the real ~/.config (upstream #258), once by killing the
// operator's live, attached tmux server (2026-08-28). The second incident
// traced to test code issuing a raw kill-server/kill-session/new-session
// with no explicit -L/-S socket, trusting the process-wide TMUX_TMPDIR
// environment variable alone. This test is the backstop: it parses (never
// executes) the small set of files that own that containment and fails if
// a new raw invocation appears without an explicit socket, so the next
// regression is caught by a red test instead of a lost session.
//
// The guard is repo-wide and ratcheted. Every *_test.go in the tree is
// parsed, plus the two non-test files that ARE the isolation mechanism
// (tests/testutil/throttle.go and the tmuxenv package). A file that already
// carried an unsocketed call when this guard landed is listed in
// tmuxSocketGuardLegacy and tolerated; anything else fails. That shape is
// the point: a lint over a fixed file list cannot see a NEW file, and a new
// file is exactly how the next regression arrives.
//
// The legacy list only ever shrinks. An entry naming a file that no longer
// exists, or one that no longer has a violation, fails too — otherwise the
// list rots into a permanent exemption nobody rechecks. Those files are not
// unguarded meanwhile: each lives in a package whose TestMain calls
// testutil.IsolateTmuxTestProcess, which sets TMUX_TMPDIR for the whole test
// binary and hard-exits rather than falling back. That is a coarser net, and
// it is not what failed on 2026-08-28.
package tests

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// tmuxSocketGuardExtraFiles are scanned even though they are not *_test.go:
// they are the isolation mechanism the test files depend on, so a hole here
// has no other net under it.
var tmuxSocketGuardExtraFiles = []string{
	"tests/testutil/throttle.go",
	"tests/testutil/tmuxenv/sweep.go",
}

// tmuxSocketGuardSkipDirs are trees this guard does not own.
var tmuxSocketGuardSkipDirs = map[string]bool{
	".git":         true,
	"third_party":  true,
	"testdata":     true,
	"node_modules": true,
	"vendor":       true,
}

// tmuxSocketGuardLegacy holds the files that already carried an unsocketed
// call when this guard landed. Each is covered by its package TestMain's
// isolation; none is permitted to grow a new one, because a listed file is
// still parsed and only its EXISTING count is tolerated.
// The value is the number of unsocketed calls that file carried on
// 2026-08-28, snapshotted by running this guard with an empty list. A file
// may keep that many and no more; one added call fails the guard.
var tmuxSocketGuardLegacy = map[string]int{
	"e2e/checkpoint_test.go":                           2,
	"e2e/conflict_deadlock_cli_e2e_test.go":            2,
	"e2e/context_rotation_e2e_test.go":                 2,
	"e2e/ensemble_cli_test.go":                         2,
	"e2e/ensemble_spawn_e2e_test.go":                   1,
	"e2e/fakeagent_harness_test.go":                    2,
	"e2e/gates_restart_e2e_test.go":                    4,
	"e2e/per_pane_account_test.go":                     1,
	"e2e/privacy_test.go":                              1,
	"e2e/redaction_test.go":                            2,
	"e2e/robot_ensemble_test.go":                       2,
	"e2e/robot_format_test.go":                         1,
	"e2e/robot_verbosity_test.go":                      2,
	"e2e/send_tracking_ack_e2e_test.go":                2,
	"e2e/slb_approval_e2e_test.go":                     2,
	"e2e/spawn_assignment_cli_e2e_test.go":             5,
	"e2e/support_bundle_test.go":                       2,
	"e2e/swarm_lifecycle_test.go":                      3,
	"e2e/transcript_context_e2e_test.go":               2,
	"e2e/workflow_run_e2e_test.go":                     2,
	"internal/cli/monitor_integration_test.go":         2,
	"internal/robot/send_idempotency_branches_test.go": 1,
	"internal/robot/tui_parity_test.go":                1,
	"internal/status/unified_test.go":                  2,
	"tests/e2e/agent_mail_communication_test.go":       1,
	"tests/e2e/agent_registration_test.go":             5,
	"tests/e2e/agent_workflow_test.go":                 3,
	"tests/e2e/assign_full_workflow_test.go":           2,
	"tests/e2e/crash_recovery_test.go":                 5,
	"tests/e2e/dependency_assignment_test.go":          1,
	"tests/e2e/ensemble_cache_test.go":                 2,
	"tests/e2e/ensemble_stream_test.go":                2,
	"tests/e2e/file_reservation_integration_test.go":   2,
	"tests/e2e/history_replay_test.go":                 1,
	"tests/e2e/multi_session_test.go":                  7,
	"tests/e2e/robot_mode_test.go":                     13,
	"tests/e2e/session_lifecycle_test.go":              1,
	"tests/e2e/session_persist_e2e_test.go":            2,
	"tests/e2e/session_recovery_test.go":               1,
	"tests/e2e/tui_parity_test.go":                     1,
	"tests/integration/tui_parity_handlers_test.go":    24,
}

// tmuxSocketGuardTriggers are the tmux subcommands dangerous enough to
// require an explicit socket: each can reach past the caller's own
// session/pane and act on a server it does not own.
var tmuxSocketGuardTriggers = map[string]bool{
	"kill-server":  true,
	"kill-session": true,
	"new-session":  true,
}

// tmuxSocketGuardExecFuncs are the call sites through which a bare argv
// reaches tmux: os/exec constructors, and the tmux.Client methods that
// forward args to one. A call through any of these carrying a trigger
// subcommand must also carry an explicit "-L" or "-S" literal, unless its
// receiver is a Client already pinned to a socket (see sanctioned client
// detection below).
var tmuxSocketGuardExecFuncs = map[string]bool{
	"Command":          true, // exec.Command
	"CommandContext":   true, // exec.CommandContext
	"Run":              true, // (*tmux.Client).Run
	"RunContext":       true, // (*tmux.Client).RunContext
	"RunSilent":        true, // (*tmux.Client).RunSilent
	"RunSilentContext": true, // (*tmux.Client).RunSilentContext
}

// TestTmuxTestCodeAddressesExplicitSocket is the static guard. It never
// starts tmux and never runs the code it inspects, so it needs no tmux
// binary and cannot itself touch a live session.
func TestTmuxTestCodeAddressesExplicitSocket(t *testing.T) {
	root := docsRepoRoot(t)

	files, err := tmuxSocketGuardScanFiles(root)
	if err != nil {
		t.Fatalf("collecting files to scan: %v", err)
	}

	counts := make(map[string]int, len(files))
	var offenders []string
	for _, rel := range files {
		found, err := findUnsocketedTmuxKills(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("parsing %s: %v", rel, err)
		}
		if len(found) == 0 {
			continue
		}
		counts[rel] = len(found)
		tolerated := tmuxSocketGuardLegacy[rel]
		if len(found) <= tolerated {
			continue
		}
		for _, f := range found[tolerated:] {
			offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, f.line, f.detail))
		}
	}

	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("tmux test code must address an explicit -L/-S socket "+
			"(bd-wkq — a raw kill can reach the operator's live tmux server "+
			"if TMUX_TMPDIR is unset, overwritten, or inherited wrongly):\n  %s",
			strings.Join(offenders, "\n  "))
	}

	// The ratchet: a legacy entry that no longer earns its place is removed,
	// or the list becomes an exemption nobody rechecks.
	var stale []string
	for rel, tolerated := range tmuxSocketGuardLegacy {
		switch actual := counts[rel]; {
		case actual == 0:
			stale = append(stale, fmt.Sprintf("%s: no unsocketed call left (drop the entry)", rel))
		case actual < tolerated:
			stale = append(stale, fmt.Sprintf("%s: tolerates %d, only %d left (lower the entry)", rel, tolerated, actual))
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("tmuxSocketGuardLegacy is stale and only ever shrinks:\n  %s", strings.Join(stale, "\n  "))
	}
}

// tmuxSocketGuardScanFiles returns every repo-relative path this guard owns:
// all *_test.go outside the skipped trees, plus the isolation mechanism
// itself. Paths are sorted so a failure reads the same way twice.
func tmuxSocketGuardScanFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if tmuxSocketGuardSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	files = append(files, tmuxSocketGuardExtraFiles...)
	sort.Strings(files)
	return files, nil
}

type unsocketedKill struct {
	line   int
	detail string
}

// findUnsocketedTmuxKills parses one Go source file and reports every call
// that issues a kill-server/kill-session/new-session tmux command without an
// explicit socket.
//
// "Explicit socket" is satisfied either by a literal "-L" or "-S" string
// argument in the same call, or by the call being a method on a
// *tmux.Client value that was itself constructed via NewClientWithSocket —
// tracked per-file rather than per-scope, which is a deliberate
// simplification safe for this small, curated file list (see the package
// doc comment); it would under-report in a larger, less controlled set.
func findUnsocketedTmuxKills(path string) ([]unsocketedKill, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}

	sanctioned := collectSanctionedClientVars(file)

	var out []unsocketedKill
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		funcName := callExprFuncName(call)
		if !tmuxSocketGuardExecFuncs[funcName] {
			return true
		}
		trigger := firstStringLitMatching(call.Args, tmuxSocketGuardTriggers)
		if trigger == "" {
			return true
		}
		if hasStringLitArg(call.Args, "-L") || hasStringLitArg(call.Args, "-S") {
			return true
		}
		if isSanctionedClientCall(call, sanctioned) {
			return true
		}
		pos := fset.Position(call.Pos())
		out = append(out, unsocketedKill{
			line:   pos.Line,
			detail: fmt.Sprintf("%q with no -L/-S and no sanctioned socket-pinned client", trigger),
		})
		return true
	})
	return out, nil
}

// collectSanctionedClientVars returns the set of identifiers anywhere in
// file that are assigned the result of a NewClientWithSocket(...) call.
func collectSanctionedClientVars(file *ast.File) map[string]bool {
	sanctioned := make(map[string]bool)
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != len(assign.Rhs) {
			return true
		}
		for i, rhs := range assign.Rhs {
			call, ok := rhs.(*ast.CallExpr)
			if !ok || !isNewClientWithSocketCall(call) {
				continue
			}
			if ident, ok := assign.Lhs[i].(*ast.Ident); ok {
				sanctioned[ident.Name] = true
			}
		}
		return true
	})
	return sanctioned
}

// isSanctionedClientCall reports whether call is a method invocation on a
// receiver that is either a sanctioned variable or an inline
// NewClientWithSocket(...) call.
func isSanctionedClientCall(call *ast.CallExpr, sanctioned map[string]bool) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch recv := sel.X.(type) {
	case *ast.Ident:
		return sanctioned[recv.Name]
	case *ast.CallExpr:
		return isNewClientWithSocketCall(recv)
	default:
		return false
	}
}

func isNewClientWithSocketCall(call *ast.CallExpr) bool {
	return callExprFuncName(call) == "NewClientWithSocket"
}

// callExprFuncName returns the trailing identifier of a call's callee:
// "Command" for exec.Command, "RunContext" for c.RunContext, "f" for a
// plain call f(...).
func callExprFuncName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	default:
		return ""
	}
}

// firstStringLitMatching returns the unquoted value of the first argument
// that is a string literal present in want, or "" if none matches.
func firstStringLitMatching(args []ast.Expr, want map[string]bool) string {
	for _, arg := range args {
		if s, ok := stringLitValue(arg); ok && want[s] {
			return s
		}
	}
	return ""
}

func hasStringLitArg(args []ast.Expr, value string) bool {
	for _, arg := range args {
		if s, ok := stringLitValue(arg); ok && s == value {
			return true
		}
	}
	return false
}

func stringLitValue(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	unquoted, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return unquoted, true
}
