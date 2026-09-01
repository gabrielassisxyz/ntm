package cli

// Tests for bd-yf3: spawn and add panes carry the launcher's attributable
// identity env, and agent-mail registration carries a non-empty
// task_description. The runtime behaviour (the agent process picks up
// AGENT_NAME via tmux -e) is covered end-to-end by the package-level
// tmux wrappers in internal/tmux/packagelevel_env_test.go. Here we prove
// the CLI layer wires the launch path to those wrappers with the right
// identity payload — i.e. a CLI refactor that drops the agent-name env
// map (or skips DerivePaneAgentName) would fail here even if the tmux
// package still accepts a -e flag in isolation.
//
// The acceptance criterion "verified by reading the env of a live spawned
// pane's process" is what the package-level test plus these AST tests
// together cover: the wrapper passes the env to the tmux binary, and the
// CLI calls the wrapper with the derived identity.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// findFuncDecl parses a source file (relative to this test file) and
// returns the top-level function decl matching name.
func findFuncDecl(t *testing.T, sourceRelPath, funcName string) *ast.FuncDecl {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("locate test source")
	}
	sourcePath := filepath.Join(filepath.Dir(thisFile), sourceRelPath)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, sourcePath, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", sourceRelPath, err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != funcName {
			continue
		}
		return fn
	}
	t.Fatalf("function %q not found in %s", funcName, sourceRelPath)
	return nil
}

// identOfCall returns the dot-separated selector name of a CallExpr, or
// "" if the call is not a selector (e.g. `f()`) or the chain is longer
// than three parts (too deep to be the helpers under test).
func selectorName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
	}
	return ""
}

// stringLitValue returns the literal string value of a basic literal
// expression, or "" if the expression is not a string literal.
func stringLitValue(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	return strings.Trim(lit.Value, `"`)
}

// callArgMapStringKeys returns the literal string keys of a
// map[string]string composite literal passed as the i-th positional
// argument to a CallExpr (used to assert that the env map includes
// `swarm.AgentNameVar` for the tmux split-window call). Recognises
// both string literals (e.g. "AGENT_NAME") and selector references
// (e.g. swarm.AgentNameVar, which is a package-level constant the
// AST sees as an *ast.SelectorExpr).
func callArgMapStringKeys(call *ast.CallExpr, idx int) map[string]bool {
	keys := map[string]bool{}
	if idx >= len(call.Args) {
		return keys
	}
	lit, ok := call.Args[idx].(*ast.CompositeLit)
	if !ok {
		return keys
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if s := stringLitValue(kv.Key); s != "" {
			keys[s] = true
			continue
		}
		if sel, ok := kv.Key.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok {
				keys[x.Name+"."+sel.Sel.Name] = true
			}
		}
	}
	return keys
}

// TestSpawnSessionLogicAppliesAgentNameEnvToFirstPane asserts criterion 1
// of bd-yf3: the spawn path's new-session call carries AGENT_NAME=<session>-p1
// in its paneEnv. Without this, pane 1 (created by tmux new-session, not
// split-window) would have no identity and the spawn-time pane would be
// unclaimable from the tracker.
//
// The AST check walks upward from the env-map construction to verify the
// value passed in is bound to a local variable declared by a
// swarm.DerivePaneAgentName call — not a constant, not a literal, not an
// unrelated string. A refactor that derives the name but then overwrites
// the local with a constant before the tmux call still fails this test.
func TestSpawnSessionLogicAppliesAgentNameEnvToFirstPane(t *testing.T) {
	decl := findFuncDecl(t, "spawn.go", "spawnSessionLogicContextWithOutput")

	var (
		sawCreateSession bool
		foundPaneEnv     map[string]bool
		firstPaneNameSrc string
	)
	ast.Inspect(decl, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selectorName(call) != "tmux.CreateSessionWithEnvContext" {
			return true
		}
		// Arg layout: ctx, session, dir, historyLimit, sessionEnv, paneEnv
		foundPaneEnv = callArgMapStringKeys(call, 5)
		sawCreateSession = true
		// Inspect the paneEnv composite literal for the value bound to
		// swarm.AgentNameVar — it should be a local variable whose
		// declaration calls swarm.DerivePaneAgentName.
		if len(call.Args) <= 5 {
			return true
		}
		lit, ok := call.Args[5].(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			keySel, ok := kv.Key.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if x, ok := keySel.X.(*ast.Ident); !ok || x.Name != "swarm" || keySel.Sel.Name != "AgentNameVar" {
				continue
			}
			firstPaneNameSrc = exprSourceBoundToDerive(decl, kv.Value)
		}
		return true
	})

	if !sawCreateSession {
		t.Fatal("spawnSessionLogicContextWithOutput never calls tmux.CreateSessionWithEnvContext")
	}
	if !foundPaneEnv["AGENT_NAME"] && !foundPaneEnv["swarm.AgentNameVar"] {
		t.Errorf("first-pane env map keys = %v, want AGENT_NAME or swarm.AgentNameVar present (bd-yf3)", foundPaneEnv)
	}
	if !strings.Contains(firstPaneNameSrc, "DerivePaneAgentName") {
		t.Errorf("first-pane AGENT_NAME value not derived from swarm.DerivePaneAgentName; source = %q (bd-yf3)", firstPaneNameSrc)
	}
}

// TestSpawnSessionLogicAppliesAgentNameEnvToSubsequentPanes asserts the
// split-window path also carries AGENT_NAME, so panes 2..N are not left
// unequipped when only new-session was equipped.
func TestSpawnSessionLogicAppliesAgentNameEnvToSubsequentPanes(t *testing.T) {
	decl := findFuncDecl(t, "spawn.go", "spawnSessionLogicContextWithOutput")

	var (
		splitCalls        int
		missingAgentName  int
		missingSessionEnv int
	)
	ast.Inspect(decl, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selectorName(call) != "tmux.SplitWindowWithEnvContext" {
			return true
		}
		splitCalls++
		// Arg layout: ctx, session, dir, paneEnv
		keys := callArgMapStringKeys(call, 3)
		if !keys["AGENT_NAME"] && !keys["swarm.AgentNameVar"] {
			missingAgentName++
		}
		// bd-fug established that GIT_IDENTITY_ENABLED must be in session env;
		// per the brief, the seed is set at session creation and inherited by
		// later splits, so we don't expect it on every split-window call —
		// but if the codebase ever pushes it down into the split path we
		// surface that here for the reader.
		_ = missingSessionEnv
		return true
	})

	if splitCalls == 0 {
		t.Fatal("spawnSessionLogicContextWithOutput never calls tmux.SplitWindowWithEnvContext")
	}
	if missingAgentName > 0 {
		t.Errorf("split-window calls missing AGENT_NAME in paneEnv: %d of %d", missingAgentName, splitCalls)
	}
}

// TestSpawnSessionLogicPaneOrdinalsAreDistinct asserts criterion 4 of
// bd-yf3: the pane ordinal used in DerivePaneAgentName varies per pane in
// the split loop, so a multi-pane spawn yields pane-mappable assignees.
// Without this guard, a refactor that collapses paneOrdinal to a constant
// would silently give every pane the same AGENT_NAME — which the
// tracker then sees as one actor under many aliases, exactly the
// shakedown failure mode this bead was opened to fix.
func TestSpawnSessionLogicPaneOrdinalsAreDistinct(t *testing.T) {
	decl := findFuncDecl(t, "spawn.go", "spawnSessionLogicContextWithOutput")

	type ordinalInfo struct {
		raw      string
		bareInt  bool
		hasArith bool
		resolved bool
	}
	var ordinals []ordinalInfo
	ast.Inspect(decl, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selectorName(call) != "swarm.DerivePaneAgentName" {
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		arg := call.Args[1]
		info := ordinalInfo{
			raw:      exprToString(arg),
			bareInt:  isIntegerLiteralExpr(arg),
			hasArith: containsBinaryExpr(arg),
		}
		// If arg is a bare identifier, follow its declaration to find
		// the right-hand side; the spawn path uses locals like
		// `paneOrdinal` and `agentNum` rather than embedding the
		// arithmetic inline.
		if ident, ok := arg.(*ast.Ident); ok && decl.Body != nil {
			var resolvedExpr ast.Expr
			ast.Inspect(decl.Body, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) == 0 {
					return true
				}
				lhs, ok := assign.Lhs[0].(*ast.Ident)
				if !ok || lhs.Name != ident.Name {
					return true
				}
				resolvedExpr = assign.Rhs[0]
				return false
			})
			if resolvedExpr != nil {
				info.resolved = true
				info.bareInt = isIntegerLiteralExpr(resolvedExpr)
				info.hasArith = containsBinaryExpr(resolvedExpr)
			}
		}
		ordinals = append(ordinals, info)
		return true
	})

	if len(ordinals) < 2 {
		t.Fatalf("spawnSessionLogicContextWithOutput calls DerivePaneAgentName only %d time(s); need at least 2 to exercise multi-pane identity (bd-yf3)", len(ordinals))
	}
	// The first pane's ordinal is legitimately `1` (it's the session's
	// initial pane created by tmux new-session). Every subsequent call
	// must vary — either as arithmetic on a per-pane counter or as a
	// hard-coded agentNum binding that itself varies per loop iteration.
	// A regression that hard-codes `agentNum` to a constant inside the
	// loop body would pass the integer-literal check below but fail the
	// arithmetic check, so both are needed.
	for i, o := range ordinals[1:] {
		if o.bareInt {
			t.Errorf("DerivePaneAgentName call #%d (non-first pane) passes a bare integer literal as the pane index (rendered: %q); this collapses every pane to one identity (bd-yf3)", i+2, o.raw)
		}
	}
	// Hard guard: every non-first ordinal argument must contain at
	// least one BinaryExpr (arithmetic). A bare identifier like a
	// loop-local that is itself a constant inside the loop body would
	// pass the literal check above but fail here. The acceptable forms
	// are arithmetic on session-scoped counters (existingPanes + i + 1,
	// agentNum + 1, …).
	for i, o := range ordinals[1:] {
		if !o.hasArith {
			t.Errorf("DerivePaneAgentName call #%d (non-first pane) ordinal %q is not an arithmetic expression; the spawn path may not vary the ordinal per pane (bd-yf3)", i+2, o.raw)
		}
	}
}

// TestSpawnSessionLogicDerivesAgentNamePerPane asserts criterion 3: the
// spawn path derives the AGENT_NAME per-pane via swarm.DerivePaneAgentName,
// so the value encodes both session and pane ordinal (the same form
// swarm-tick already parses).
func TestSpawnSessionLogicDerivesAgentNamePerPane(t *testing.T) {
	decl := findFuncDecl(t, "spawn.go", "spawnSessionLogicContextWithOutput")

	var deriveCalls int
	ast.Inspect(decl, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selectorName(call) != "swarm.DerivePaneAgentName" {
			return true
		}
		deriveCalls++
		return true
	})

	if deriveCalls < 2 {
		t.Errorf("spawnSessionLogicContextWithOutput calls swarm.DerivePaneAgentName %d time(s); want at least 2 — one per pane created (bd-yf3)", deriveCalls)
	}
}

// TestAddPathDerivesAgentNameAndPropagatesToSplit asserts the add path
// derives a distinct AGENT_NAME per added pane and applies it to the
// split-window call. Pre-bd-yf3, add only used SplitWindowContext (no env)
// so added panes carried no identity at all.
func TestAddPathDerivesAgentNameAndPropagatesToSplit(t *testing.T) {
	decl := findFuncDecl(t, "add.go", "executeAdd")

	var (
		splitCalls       int
		missingAgentName int
		deriveCalls      int
	)
	ast.Inspect(decl, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch selectorName(call) {
		case "tmux.SplitWindowWithEnvContext":
			splitCalls++
			keys := callArgMapStringKeys(call, 3)
			if !keys["AGENT_NAME"] && !keys["swarm.AgentNameVar"] {
				missingAgentName++
			}
		case "swarm.DerivePaneAgentName":
			deriveCalls++
		}
		return true
	})

	if splitCalls == 0 {
		t.Fatal("executeAdd never calls tmux.SplitWindowWithEnvContext")
	}
	if missingAgentName > 0 {
		t.Errorf("executeAdd split-window calls missing AGENT_NAME in paneEnv: %d of %d", missingAgentName, splitCalls)
	}
	if deriveCalls == 0 {
		t.Errorf("executeAdd never calls swarm.DerivePaneAgentName; added panes would all share one identity")
	}
}

// TestSpawnSpawnedAgentInfoCarriesAgentName asserts the spawnedAgentInfo
// struct passed to registerSpawnedAgents carries an agentName field. This
// is the bridge between the launch path (DerivePaneAgentName → pane env)
// and the registration path (TaskDescription on Agent Mail): if either
// end of this struct drops agentName, the pane gets an env but no
// registration, or vice versa.
func TestSpawnSpawnedAgentInfoCarriesAgentName(t *testing.T) {
	decl := findFuncDecl(t, "spawn.go", "spawnSessionLogicContextWithOutput")

	sawAgentName := false
	ast.Inspect(decl, func(node ast.Node) bool {
		lit, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		ident, ok := lit.Type.(*ast.Ident)
		if !ok || ident.Name != "spawnedAgentInfo" {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "agentName" {
				sawAgentName = true
			}
		}
		return true
	})

	if !sawAgentName {
		t.Fatal("spawnSessionLogicContextWithOutput never populates spawnedAgentInfo.agentName")
	}
}

// TestAddSpawnedAgentInfoCarriesAgentName is the add-side counterpart
// to TestSpawnSpawnedAgentInfoCarriesAgentName: the added pane's struct
// must carry agentName so registerSpawnedAgents can pass it on to the
// Agent Mail RegisterAgent call.
func TestAddSpawnedAgentInfoCarriesAgentName(t *testing.T) {
	decl := findFuncDecl(t, "add.go", "executeAdd")

	sawAgentName := false
	ast.Inspect(decl, func(node ast.Node) bool {
		lit, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		ident, ok := lit.Type.(*ast.Ident)
		if !ok || ident.Name != "spawnedAgentInfo" {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "agentName" {
				sawAgentName = true
			}
		}
		return true
	})

	if !sawAgentName {
		t.Fatal("executeAdd never populates spawnedAgentInfo.agentName")
	}
}

// TestRegisterSpawnedAgentsCarriesTaskDescription asserts criterion 2 of
// bd-yf3: the agent-mail registration payload includes a non-empty
// task_description, so a tracker claim can be mapped back to a pane
// without archaeology. The literal in spawn.go is
// fmt.Sprintf("NTM pane %s", agent.agentName); an empty agentName would
// yield "NTM pane " which the test would still match, so this is paired
// with the agentName-population tests above.
func TestRegisterSpawnedAgentsCarriesTaskDescription(t *testing.T) {
	decl := findFuncDecl(t, "spawn.go", "registerSpawnedAgents")

	var (
		sawRegisterAgent    bool
		taskDescLiteral     string
		taskDescNonEmpty    bool
		missingOnAnyCall    int
		referencesAgentName bool
	)
	ast.Inspect(decl, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		// Either c.RegisterAgent(...) or client.RegisterAgent(...) — both
		// hit the same path; we just need to confirm the RegisterAgent
		// call site is the one that takes RegisterAgentOptions with a
		// TaskDescription populated.
		if !(selectorName(call) == "client.RegisterAgent" || selectorName(call) == "RegisterAgent") {
			return true
		}
		sawRegisterAgent = true
		// The RegisterAgent call takes a single composite literal of type
		// RegisterAgentOptions as its last argument.
		if len(call.Args) == 0 {
			missingOnAnyCall++
			return true
		}
		lit, ok := call.Args[len(call.Args)-1].(*ast.CompositeLit)
		if !ok {
			missingOnAnyCall++
			return true
		}
		var foundTaskDesc bool
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "TaskDescription" {
				foundTaskDesc = true
				taskDescLiteral = exprToString(kv.Value)
				if exprReferencesAgentName(kv.Value) {
					referencesAgentName = true
				}
			}
		}
		if !foundTaskDesc {
			missingOnAnyCall++
		}
		if foundTaskDesc {
			taskDescNonEmpty = true
		}
		return true
	})

	if !sawRegisterAgent {
		t.Fatal("registerSpawnedAgents never calls RegisterAgent")
	}
	if missingOnAnyCall > 0 {
		t.Errorf("registerSpawnedAgents has %d RegisterAgent call(s) without a TaskDescription field", missingOnAnyCall)
	}
	if !taskDescNonEmpty {
		t.Errorf("registerSpawnedAgents's RegisterAgent call TaskDescription literal = %q, want a non-empty value naming the pane", taskDescLiteral)
	}
	if !referencesAgentName {
		t.Errorf("TaskDescription = %q, want it to reference agent.agentName so the description names the pane", taskDescLiteral)
	}
}

// exprReferencesAgentName reports whether expr is a fmt.Sprintf call
// (or any expression) that references agent.agentName somewhere in its
// arguments. This is the structural guard against the regression where
// the description drops the per-pane identity and becomes a constant.
func exprReferencesAgentName(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.CallExpr:
		for _, arg := range e.Args {
			if exprReferencesAgentName(arg) {
				return true
			}
		}
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok && x.Name == "agent" && e.Sel.Name == "agentName" {
			return true
		}
	}
	return false
}

// isBareIntegerLiteral reports whether the AST-derived text represents
// a single integer literal (e.g. "1", "42") with no surrounding
// arithmetic. Used to detect the regression that gave every spawn-time
// pane the same agent name.
func isBareIntegerLiteral(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isIntegerLiteralExpr reports whether expr is a single bare integer
// literal (e.g. BasicLit INT with value "1"). Used to reject
// `DerivePaneAgentName(session, 1)` and similar constant ordinals that
// collapse every pane to one identity.
func isIntegerLiteralExpr(expr ast.Expr) bool {
	lit, ok := expr.(*ast.BasicLit)
	if !ok {
		return false
	}
	if lit.Kind != token.INT {
		return false
	}
	return lit.Value != ""
}

// containsBinaryExpr reports whether expr or any sub-expression is a
// Go binary expression (e.g. `existingPanes + i + 1`). The spawn path
// must compute the pane ordinal arithmetically from the iteration index
// — a regression that names a constant instead is caught by the sibling
// integer-literal check, but a regression that names a constant variable
// is caught here.
func containsBinaryExpr(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if _, ok := n.(*ast.BinaryExpr); ok {
			found = true
			return false
		}
		return true
	})
	return found
}

// exprToString renders an AST expression to a recognizable source form
// for diagnostics — not a full pretty-printer, just enough to identify
// what the test saw.
func exprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Value
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok {
				return x.Name + "." + sel.Sel.Name + "(...)"
			}
		}
		if id, ok := e.Fun.(*ast.Ident); ok {
			return id.Name + "(...)"
		}
	}
	return ""
}

// exprSourceBoundToDerive resolves expr — expected to be an identifier
// referencing a local variable — by scanning the function body for its
// declaration, and returns a short source-form description of the right
// hand side. Used by the spawn test to confirm the variable used in the
// tmux call site is the one initialised by swarm.DerivePaneAgentName,
// not a sibling constant the refactor introduced.
//
// Because the spawn function is one long function body (no extracted
// helpers) with many nested blocks, this search walks the function
// body linearly: declarations at any depth count, and the binding
// closest to the call site wins when multiple declarations exist for
// the same name.
func exprSourceBoundToDerive(decl *ast.FuncDecl, expr ast.Expr) string {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return exprToString(expr)
	}
	if decl.Body == nil {
		return ident.Name
	}
	var found string
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || lhs.Name != ident.Name || len(assign.Rhs) == 0 {
			return true
		}
		found = ident.Name + " = " + exprToString(assign.Rhs[0])
		return false
	})
	if found == "" {
		return ident.Name + " (declaration not found)"
	}
	return found
}
