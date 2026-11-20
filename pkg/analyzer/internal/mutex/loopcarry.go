package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// loopCarryAnalyzer detects mutex usage patterns that cross loop iteration
// boundaries: deferred unlocks inside loops and locks that are "carried" past
// an iteration via break or continue.
//
// It is config-only (no per-function mutable state) so a single instance may
// be shared between a Checker and all its simulation forks.
type loopCarryAnalyzer struct {
	mutexNames     map[string]bool
	rwMutexNames   map[string]bool
	commentFilter  *commentfilter.CommentFilter
	errorCollector report.Reporter
	termination    *terminationAnalyzer
}

// newLoopCarryAnalyzer creates a loopCarryAnalyzer with the given dependencies.
func newLoopCarryAnalyzer(
	mutexNames, rwMutexNames map[string]bool,
	cf *commentfilter.CommentFilter,
	ec report.Reporter,
	term *terminationAnalyzer,
) *loopCarryAnalyzer {
	return &loopCarryAnalyzer{
		mutexNames:     mutexNames,
		rwMutexNames:   rwMutexNames,
		commentFilter:  cf,
		errorCollector: ec,
		termination:    term,
	}
}

// reportDeferredUnlocksInLoop reports a lock taken in loop's body whose unlock
// is deferred to function return, so the next iteration takes it again while
// it is still held. A lock named through a variable bound per iteration is
// left alone: it may be a different lock each time, and taking a set of locks
// together this way (`for _, v := range vs { v.mu.Lock(); defer
// v.mu.Unlock() }`) holds each one once.
func (lc *loopCarryAnalyzer) reportDeferredUnlocksInLoop(loop ast.Stmt, body *ast.BlockStmt) {
	if body == nil {
		return
	}

	locked := make(map[string]bool)
	rlocked := make(map[string]bool)
	lc.reportDeferredUnlocksInLoopStatements(body.List, locked, rlocked, perIterationVars(loop, lc.termination.typesInfo), false)
}

// perIterationVars returns the variables loop binds anew on each iteration:
// its range variables, its init variables that its post statement or body
// reassign, and the locals of its body whose initializer reads one of them.
func perIterationVars(loop ast.Stmt, info *types.Info) map[types.Object]bool {
	if info == nil {
		return nil
	}
	vars := make(map[types.Object]bool)
	define := func(lhs ...ast.Expr) {
		for _, e := range lhs {
			if ident, ok := e.(*ast.Ident); ok {
				if obj := info.Defs[ident]; obj != nil {
					vars[obj] = true
				}
			}
		}
	}
	var body *ast.BlockStmt
	switch l := loop.(type) {
	case *ast.RangeStmt:
		if l.Tok == token.DEFINE {
			define(l.Key, l.Value)
		}
		body = l.Body
	case *ast.ForStmt:
		if init, ok := l.Init.(*ast.AssignStmt); ok && init.Tok == token.DEFINE {
			for _, lhs := range init.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && reassignedIn(info.Defs[ident], info, l.Post, l.Body) {
					define(ident)
				}
			}
		}
		body = l.Body
	}
	if body == nil || len(vars) == 0 {
		return vars
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			if node.Tok == token.DEFINE && slices.ContainsFunc(node.Rhs, func(e ast.Expr) bool { return mentionsAny(e, vars, info) }) {
				define(node.Lhs...)
			}
		case *ast.ValueSpec:
			if slices.ContainsFunc(node.Values, func(e ast.Expr) bool { return mentionsAny(e, vars, info) }) {
				for _, name := range node.Names {
					define(name)
				}
			}
		}
		return true
	})
	return vars
}

// reassignedIn reports whether one of nodes assigns obj or steps it with ++/--.
func reassignedIn(obj types.Object, info *types.Info, nodes ...ast.Node) bool {
	if obj == nil {
		return false
	}
	found := false
	assigns := func(expr ast.Expr) {
		if ident, ok := common.UnwrapParenExpr(expr).(*ast.Ident); ok && info.Uses[ident] == obj {
			found = true
		}
	}
	for _, n := range nodes {
		if n == nil || found {
			continue
		}
		ast.Inspect(n, func(node ast.Node) bool {
			switch x := node.(type) {
			case *ast.AssignStmt:
				for _, lhs := range x.Lhs {
					assigns(lhs)
				}
			case *ast.IncDecStmt:
				assigns(x.X)
			}
			return !found
		})
	}
	return found
}

// mentionsAny reports whether expr reads one of vars.
func mentionsAny(expr ast.Expr, vars map[types.Object]bool, info *types.Info) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && vars[info.Uses[ident]] {
			found = true
		}
		return !found
	})
	return found
}

// reportDeferredUnlocksInLoopStatements scans stmts, a list inside the loop
// body. exitsAfter says that finishing the list leaves the function before the
// loop starts over: the list sits in a branch followed, in every enclosing
// list up to the loop body, by statements that end in a return.
func (lc *loopCarryAnalyzer) reportDeferredUnlocksInLoopStatements(stmts []ast.Stmt, locked, rlocked map[string]bool, perIteration map[types.Object]bool, exitsAfter bool) {
	for i, stmt := range stmts {
		if lc.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		// What follows stmt leaves the function before the next iteration: a
		// defer in a branch of stmt runs at most once. rest must get there
		// without a break, continue or goto, which the termination analysis
		// also counts as ending a block.
		rest := stmts[i+1:]
		restEnds := lc.termination.blockAlwaysTerminates(&ast.BlockStmt{List: rest})
		leavesAfter := !jumpsAway(rest) && (restEnds || exitsAfter)

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || lc.commentFilter.ShouldSkipCall(call) {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			varName := common.GetVarName(sel.X)
			switch sel.Sel.Name {
			case "Lock":
				if lc.mutexNames[varName] || lc.rwMutexNames[varName] {
					locked[varName] = true
				}
			case "RLock":
				if lc.rwMutexNames[varName] {
					rlocked[varName] = true
				}
			case "Unlock":
				delete(locked, varName)
			case "RUnlock":
				delete(rlocked, varName)
			}

		case *ast.DeferStmt:
			if lc.commentFilter.ShouldSkipCall(s.Call) {
				continue
			}
			if restEnds || leavesAfter {
				continue
			}
			sel, ok := s.Call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			varName := common.GetVarName(sel.X)
			if len(perIteration) > 0 && mentionsAny(sel.X, perIteration, lc.termination.typesInfo) {
				continue
			}
			switch sel.Sel.Name {
			case "Unlock":
				if locked[varName] {
					mutexType := "mutex"
					if lc.rwMutexNames[varName] {
						mutexType = "rwmutex"
					}
					lc.errorCollector.AddError(s.Pos(), category.DeferUnlockInLoop, mutexType+" '"+varName+"' defers unlock inside loop")
				}
			case "RUnlock":
				if rlocked[varName] {
					lc.errorCollector.AddError(s.Pos(), category.DeferUnlockInLoop, "rwmutex '"+varName+"' defers runlock inside loop")
				}
			}

		case *ast.BlockStmt:
			lc.reportDeferredUnlocksInLoopStatements(s.List, maps.Clone(locked), maps.Clone(rlocked), perIteration, leavesAfter)
		case *ast.LabeledStmt:
			lc.reportDeferredUnlocksInLoopStatements([]ast.Stmt{s.Stmt}, maps.Clone(locked), maps.Clone(rlocked), perIteration, leavesAfter)
		case *ast.IfStmt:
			// `if !flag { mu.Lock(); defer mu.Unlock(); flag = true }` is a
			// self-disabling branch: setting the flag it tests means the body —
			// and its deferred unlock — executes at most once no matter how many
			// times the loop iterates (grpc interop's hasORCALock idiom). One
			// deferred unlock held to function exit is the ordinary defer
			// pattern, not per-iteration accumulation.
			if !ifBranchDisablesItself(s) {
				lc.reportDeferredUnlocksInLoopStatements(s.Body.List, maps.Clone(locked), maps.Clone(rlocked), perIteration, leavesAfter)
			}
			if s.Else != nil {
				lc.reportDeferredUnlocksInLoopElse(s.Else, maps.Clone(locked), maps.Clone(rlocked), perIteration, leavesAfter)
			}
		case *ast.ForStmt:
			lc.reportDeferredUnlocksInLoopStatements(s.Body.List, maps.Clone(locked), maps.Clone(rlocked), perIteration, false)
		case *ast.RangeStmt:
			lc.reportDeferredUnlocksInLoopStatements(s.Body.List, maps.Clone(locked), maps.Clone(rlocked), perIteration, false)
		case *ast.SwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					lc.reportDeferredUnlocksInLoopStatements(cc.Body, maps.Clone(locked), maps.Clone(rlocked), perIteration, leavesAfter)
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					lc.reportDeferredUnlocksInLoopStatements(cc.Body, maps.Clone(locked), maps.Clone(rlocked), perIteration, leavesAfter)
				}
			}
		case *ast.SelectStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CommClause); ok {
					lc.reportDeferredUnlocksInLoopStatements(cc.Body, maps.Clone(locked), maps.Clone(rlocked), perIteration, leavesAfter)
				}
			}
		}
	}
}

// ifBranchDisablesItself reports whether the statement is `if !flag { ...;
// flag = true; ... }` — a branch guarded by a boolean it sets, so its body runs
// at most once across all loop iterations.
func ifBranchDisablesItself(s *ast.IfStmt) bool {
	unary, ok := s.Cond.(*ast.UnaryExpr)
	if !ok || unary.Op != token.NOT {
		return false
	}
	flagExpr := exprString(unary.X)
	if flagExpr == "" || s.Body == nil {
		return false
	}

	for _, stmt := range s.Body.List {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 || assign.Tok != token.ASSIGN {
			continue
		}
		rhs, ok := assign.Rhs[0].(*ast.Ident)
		if !ok || rhs.Name != "true" {
			continue
		}
		if exprString(assign.Lhs[0]) == flagExpr {
			return true
		}
	}
	return false
}

func (lc *loopCarryAnalyzer) reportDeferredUnlocksInLoopElse(stmt ast.Stmt, locked, rlocked map[string]bool, perIteration map[types.Object]bool, exitsAfter bool) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		lc.reportDeferredUnlocksInLoopStatements(s.List, locked, rlocked, perIteration, exitsAfter)
	case *ast.IfStmt:
		lc.reportDeferredUnlocksInLoopStatements([]ast.Stmt{s}, locked, rlocked, perIteration, exitsAfter)
	}
}

// jumpsAway reports whether stmts hold a break, continue or goto that may skip
// their end. Jumps inside a nested loop or function literal stay there and do
// not count; anything else counts, even a break that only leaves a switch.
func jumpsAway(stmts []ast.Stmt) bool {
	found := false
	for _, stmt := range stmts {
		ast.Inspect(stmt, func(n ast.Node) bool {
			if found {
				return false
			}
			switch x := n.(type) {
			case *ast.FuncLit, *ast.ForStmt, *ast.RangeStmt:
				return false
			case *ast.BranchStmt:
				if x.Tok != token.FALLTHROUGH {
					found = true
				}
			}
			return !found
		})
	}
	return found
}

// applyLoopExitLocks adds to final a lock that a break carries out of the
// loop. Every iteration starts from what was held at the loop head, so the
// walk starts at that depth: a body that drops the lock and takes it back
// before breaking leaves the loop holding exactly what it entered with. The
// carried lock is added only when final does not already account for it,
// comparing what is held net of borrowed releases, so a function that enters
// holding the lock, drops it and takes it back in the loop stays balanced.
//
// resolve maps a variable that stands for a lock (`f := s.f`) to the lock it
// names, as the lock state does: calls through the alias count on the lock
// itself, and the alias has no state of its own.
func (lc *loopCarryAnalyzer) applyLoopExitLocks(stmt *ast.ForStmt, initial, final map[string]*Stats, resolve func(string) string) {
	for mutexName := range lc.mutexNames {
		if resolveName(resolve, mutexName) != mutexName {
			continue
		}
		in, out := initial[mutexName], final[mutexName]
		head := heldCount(in, false)
		if pos, ok := lc.loopMayBreakHoldingMutex(stmt.Body.List, mutexName, WriteLockPattern, head, head, token.NoPos, resolve); ok && out != nil {
			if netHeld(out, false) <= netHeld(in, false) {
				out.carryLock(pos)
			}
		}
	}

	for rwMutexName := range lc.rwMutexNames {
		if resolveName(resolve, rwMutexName) != rwMutexName {
			continue
		}
		in, out := initial[rwMutexName], final[rwMutexName]
		head := heldCount(in, false)
		if pos, ok := lc.loopMayBreakHoldingMutex(stmt.Body.List, rwMutexName, WriteLockPattern, head, head, token.NoPos, resolve); ok && out != nil {
			if netHeld(out, false) <= netHeld(in, false) {
				out.carryLock(pos)
			}
		}
		readHead := heldCount(in, true)
		if pos, ok := lc.loopMayBreakHoldingMutex(stmt.Body.List, rwMutexName, ReadLockPattern, readHead, readHead, token.NoPos, resolve); ok && out != nil {
			if netHeld(out, true) <= netHeld(in, true) {
				out.carryRLock(pos)
			}
		}
	}
}

// resolveName applies resolve to name; a nil resolve leaves it unchanged.
func resolveName(resolve func(string) string, name string) string {
	if resolve == nil {
		return name
	}
	return resolve(name)
}

// heldCount is how many write (or read) locks st holds; nil holds none.
func heldCount(st *Stats, read bool) int {
	switch {
	case st == nil:
		return 0
	case read:
		return st.rlock
	default:
		return st.lock
	}
}

// netHeld is heldCount less the releases st made without holding the lock.
func netHeld(st *Stats, read bool) int {
	switch {
	case st == nil:
		return 0
	case read:
		return st.rlock - st.borrowedRLock
	default:
		return st.lock - st.borrowedLock
	}
}

// loopMayBreakHoldingMutex reports whether a break leaves the loop holding
// varName deeper than head, the depth held at the loop head.
func (lc *loopCarryAnalyzer) loopMayBreakHoldingMutex(stmts []ast.Stmt, varName string, pattern LockPattern, head, depth int, lastLockPos token.Pos, resolve func(string) string) (token.Pos, bool) {
	currentDepth := depth
	currentLockPos := lastLockPos

	for _, stmt := range stmts {
		if lc.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || lc.commentFilter.ShouldSkipCall(call) {
				continue
			}

			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || resolveName(resolve, common.GetVarName(sel.X)) != varName {
				continue
			}

			if containsMethod(pattern.LockMethods, sel.Sel.Name) {
				currentDepth++
				currentLockPos = call.Pos()
				continue
			}

			if containsMethod(pattern.UnlockMethods, sel.Sel.Name) && currentDepth > 0 {
				currentDepth--
			}
		case *ast.IfStmt:
			if pos, ok := lc.loopMayBreakHoldingMutex(s.Body.List, varName, pattern, head, currentDepth, currentLockPos, resolve); ok {
				return pos, true
			}
			if s.Else != nil {
				switch elseNode := s.Else.(type) {
				case *ast.BlockStmt:
					if pos, ok := lc.loopMayBreakHoldingMutex(elseNode.List, varName, pattern, head, currentDepth, currentLockPos, resolve); ok {
						return pos, true
					}
				case *ast.IfStmt:
					if pos, ok := lc.loopMayBreakHoldingMutex([]ast.Stmt{elseNode}, varName, pattern, head, currentDepth, currentLockPos, resolve); ok {
						return pos, true
					}
				}
			}
		case *ast.BlockStmt:
			if pos, ok := lc.loopMayBreakHoldingMutex(s.List, varName, pattern, head, currentDepth, currentLockPos, resolve); ok {
				return pos, true
			}
		case *ast.LabeledStmt:
			if pos, ok := lc.loopMayBreakHoldingMutex([]ast.Stmt{s.Stmt}, varName, pattern, head, currentDepth, currentLockPos, resolve); ok {
				return pos, true
			}
		case *ast.BranchStmt:
			if s.Tok == token.BREAK && currentDepth > head {
				return currentLockPos, true
			}
		}
	}

	return token.NoPos, false
}

// isCarriedLoopUnlock reports whether the unlock at unlockPos occurs inside a
// loop that also locks varName and may carry the lock past the iteration
// boundary (i.e. continue without unlocking). function must be the enclosing
// *ast.FuncDecl; if nil or its Body is nil the method returns false.
func (lc *loopCarryAnalyzer) isCarriedLoopUnlock(varName string, unlockPos token.Pos, function *ast.FuncDecl, pattern LockPattern, resolve func(string) string) bool {
	if function == nil || function.Body == nil || unlockPos == token.NoPos {
		return false
	}

	found := false
	ast.Inspect(function.Body, func(n ast.Node) bool {
		if found {
			return false
		}

		switch loop := n.(type) {
		case *ast.ForStmt:
			if loop.End() >= unlockPos {
				return true
			}
			if lc.loopMayCarryMutexPastIteration(loop.Body.List, varName, pattern, 0, resolve) {
				found = true
				return false
			}
		case *ast.RangeStmt:
			if loop.End() >= unlockPos {
				return true
			}
			if lc.loopMayCarryMutexPastIteration(loop.Body.List, varName, pattern, 0, resolve) {
				found = true
				return false
			}
		}

		return true
	})

	return found
}

func (lc *loopCarryAnalyzer) loopMayCarryMutexPastIteration(stmts []ast.Stmt, varName string, pattern LockPattern, depth int, resolve func(string) string) bool {
	currentDepth := depth

	for _, stmt := range stmts {
		if lc.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || lc.commentFilter.ShouldSkipCall(call) {
				continue
			}

			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || resolveName(resolve, common.GetVarName(sel.X)) != varName {
				continue
			}

			if containsMethod(pattern.LockMethods, sel.Sel.Name) {
				currentDepth++
				continue
			}
			if containsMethod(pattern.UnlockMethods, sel.Sel.Name) && currentDepth > 0 {
				currentDepth--
			}
		case *ast.IfStmt:
			if lc.loopMayCarryMutexPastIteration(s.Body.List, varName, pattern, currentDepth, resolve) {
				return true
			}
			if s.Else != nil {
				switch elseNode := s.Else.(type) {
				case *ast.BlockStmt:
					if lc.loopMayCarryMutexPastIteration(elseNode.List, varName, pattern, currentDepth, resolve) {
						return true
					}
				case *ast.IfStmt:
					if lc.loopMayCarryMutexPastIteration([]ast.Stmt{elseNode}, varName, pattern, currentDepth, resolve) {
						return true
					}
				}
			}
		case *ast.BlockStmt:
			if lc.loopMayCarryMutexPastIteration(s.List, varName, pattern, currentDepth, resolve) {
				return true
			}
		case *ast.LabeledStmt:
			if lc.loopMayCarryMutexPastIteration([]ast.Stmt{s.Stmt}, varName, pattern, currentDepth, resolve) {
				return true
			}
		case *ast.ForStmt:
			if lc.loopMayCarryMutexPastIteration(s.Body.List, varName, pattern, currentDepth, resolve) {
				return true
			}
		case *ast.RangeStmt:
			if lc.loopMayCarryMutexPastIteration(s.Body.List, varName, pattern, currentDepth, resolve) {
				return true
			}
		case *ast.SwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok &&
					lc.loopMayCarryMutexPastIteration(cc.Body, varName, pattern, currentDepth, resolve) {
					return true
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok &&
					lc.loopMayCarryMutexPastIteration(cc.Body, varName, pattern, currentDepth, resolve) {
					return true
				}
			}
		case *ast.SelectStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CommClause); ok &&
					lc.loopMayCarryMutexPastIteration(cc.Body, varName, pattern, currentDepth, resolve) {
					return true
				}
			}
		case *ast.BranchStmt:
			if s.Tok == token.CONTINUE && currentDepth > 0 {
				return true
			}
		}
	}

	return false
}
