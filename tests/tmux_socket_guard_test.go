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
// Scope is deliberately narrow, not repo-wide. Dozens of e2e/integration
// test files across the tree also call kill-session/new-session with no
// -L/-S — those rely on a *different*, coarser safety net: their package's
// own TestMain calls testutil.IsolateTmuxTestProcess, which sets
// TMUX_TMPDIR for the whole test binary before any test runs and hard-exits
// (never falls back) if it cannot. That net is unchanged by this bead and
// is not what failed on 2026-08-28. What failed is the small set of files
// below, which issue tmux commands directly rather than through a package
// TestMain, so a hole here has no other net under it. Widening this guard
// to the whole tree would fail on those unrelated, already-covered call
// sites without fixing anything real.
package tests

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// tmuxSocketGuardFiles are the files that issue tmux server/session-killing
// commands directly (not through a package TestMain's isolation). Two are
// not *_test.go by name — tests/testutil/throttle.go and the tmuxenv
// package — because they ARE the isolation mechanism the *_test.go files
// above depend on; excluding them on a naming technicality would leave the
// actual 2026-08-28 fix path unguarded.
var tmuxSocketGuardFiles = []string{
	"tests/testutil/throttle.go",
	"tests/testutil/tmuxenv/sweep.go",
	"internal/tmux/session_test.go",
	"internal/cli/bind_test.go",
	"internal/pipeline/real_tmux_integration_test.go",
	"internal/tui/dashboard/real_tmux_integration_test.go",
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

	var offenders []string
	for _, rel := range tmuxSocketGuardFiles {
		path := filepath.Join(root, rel)
		found, err := findUnsocketedTmuxKills(path)
		if err != nil {
			t.Fatalf("parsing %s: %v", rel, err)
		}
		for _, f := range found {
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
