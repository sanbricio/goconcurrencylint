package mutex

import (
	"go/ast"
	"go/token"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

// analyzeBlock analyzes a block statement starting from the provided state and
// returns the resulting stats after executing that block.
func (ma *Checker) analyzeBlock(block *ast.BlockStmt, initial map[string]*Stats) map[string]*Stats {
	return ma.analyzeStatementList(block.List, initial)
}

func (ma *Checker) analyzeStatementList(stmts []ast.Stmt, initial map[string]*Stats) map[string]*Stats {
	blockStats := ma.cloneStatsMap(initial)
	skip := make(map[token.Pos]bool)
	terminatingTail := ma.terminatingTailByIndex(stmts)

	for i, stmt := range stmts {
		if ma.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		if skip[stmt.Pos()] {
			continue
		}
		if ma.skipBalancedGuardedLock(stmt, stmts[i+1:], skip) {
			continue
		}
		ma.analyzeStatementWithTail(stmt, blockStats, terminatingTail[i+1])
	}

	return blockStats
}

func (ma *Checker) analyzeStatementWithTail(stmt ast.Stmt, stats map[string]*Stats, tailTerminates bool) {
	if _, ok := stmt.(*ast.IfStmt); !ok || !tailTerminates {
		ma.analyzeStatement(stmt, stats)
		return
	}

	ma.terminatingTailDepth++
	defer func() { ma.terminatingTailDepth-- }()
	ma.analyzeStatement(stmt, stats)
}

func (ma *Checker) terminatingTailByIndex(stmts []ast.Stmt) []bool {
	tail := make([]bool, len(stmts)+1)
	for i := range slices.Backward(stmts) {
		tail[i] = tail[i+1] || ma.termination.statementAlwaysTerminates(stmts[i])
	}
	return tail
}

func (ma *Checker) skipBalancedGuardedLock(stmt ast.Stmt, rest []ast.Stmt, skip map[token.Pos]bool) bool {
	guard, varName, methodName, ok := ma.guardedMutexCall(stmt)
	if !ok || !isLockMethod(methodName) {
		return false
	}

	releaseMethod := matchingUnlockMethod(methodName)
	if releaseMethod == "" {
		return false
	}

	for _, later := range rest {
		if ma.guardedReleaseMatches(later, guard, varName, releaseMethod) {
			skip[later.Pos()] = true
			return true
		}
		if ma.statementMayExit(later) {
			return false
		}
	}

	return false
}

// guardedReleaseMatches reports whether `stmt` releases `varName` under
// `guard` on every reachable path.
func (ma *Checker) guardedReleaseMatches(stmt ast.Stmt, guard, varName, releaseMethod string) bool {
	if laterGuard, laterVar, laterMethod, ok := ma.guardedMutexCall(stmt); ok {
		return laterGuard == guard && laterVar == varName && laterMethod == releaseMethod
	}

	cond, body, ok := ma.guardedIf(stmt)
	if !ok || cond != guard {
		return false
	}
	return ma.bodyReleasesOnEveryPath(body, varName, releaseMethod)
}

func (ma *Checker) statementMayExit(stmt ast.Stmt) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.DeferStmt, *ast.GoStmt:
			return false
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			found = true
			return false
		case *ast.BranchStmt:
			if node.Tok == token.GOTO || node.Tok == token.BREAK || node.Tok == token.CONTINUE {
				found = true
				return false
			}
		case *ast.CallExpr:
			if ma.termination.callTerminatesExecution(node) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// guardedIf returns the condition and body for a plain `if cond { body }`.
func (ma *Checker) guardedIf(stmt ast.Stmt) (string, *ast.BlockStmt, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil || ifStmt.Body == nil {
		return "", nil, false
	}
	return exprString(ifStmt.Cond), ifStmt.Body, true
}

// guardedMutexCall detects `if cond { mu.Lock() }` and
// `if cond { mu.Unlock() }` forms with one mutex call.
func (ma *Checker) guardedMutexCall(stmt ast.Stmt) (string, string, string, bool) {
	cond, body, ok := ma.guardedIf(stmt)
	if !ok {
		return "", "", "", false
	}

	var varName, methodName string
	foundCalls := 0
	for _, bodyStmt := range body.List {
		if ma.statementMayExit(bodyStmt) {
			return "", "", "", false
		}
		ast.Inspect(bodyStmt, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || ma.commentFilter.ShouldSkipCall(call) {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			candidateVarName := common.GetVarName(sel.X)
			if !ma.mutexNames[candidateVarName] && !ma.rwMutexNames[candidateVarName] {
				return true
			}
			candidateMethodName := sel.Sel.Name
			if !isLockMethod(candidateMethodName) && !isUnlockMethod(candidateMethodName) {
				return true
			}
			foundCalls++
			varName = candidateVarName
			methodName = candidateMethodName
			return true
		})
	}
	if foundCalls != 1 {
		return "", "", "", false
	}

	return cond, varName, methodName, true
}

// bodyReleasesOnEveryPath reports whether `body` unlocks exactly once before
// each reachable exit.
func (ma *Checker) bodyReleasesOnEveryPath(body *ast.BlockStmt, varName, methodName string) bool {
	if body == nil {
		return false
	}
	sim := pathReleaseSimulator{analyzer: ma, varName: varName, method: methodName}
	count, terminated, ok := sim.run(body.List, 0)
	if !ok {
		return false
	}
	if terminated {
		return true
	}
	return count == 1
}

// analyzeStatement analyzes individual statements
func (ma *Checker) analyzeStatement(stmt ast.Stmt, stats map[string]*Stats) {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		ma.panicDetector.reportPotentialPanicWhileLocked(s, stats)
		ma.analyzeExpressionStatement(s, stats)
	case *ast.AssignStmt:
		ma.analyzeAssignStatement(s, stats)
	case *ast.DeclStmt:
		ma.analyzeDeclStatement(s, stats)
	case *ast.DeferStmt:
		ma.analyzeDeferStatement(s, stats)
	case *ast.ReturnStmt:
		ma.tryLock.markReturnedChecked(s)
		ma.panicDetector.reportPotentialPanicWhileLocked(s, stats)
		ma.analyzeReturnStatement(s, stats)
	case *ast.IfStmt:
		ma.analyzeIfStatement(s, stats)
	case *ast.GoStmt:
		ma.analyzeGoStatement(s, stats)
	case *ast.ForStmt:
		ma.analyzeForStatement(s, stats)
	case *ast.RangeStmt:
		ma.analyzeRangeStatement(s, stats)
	case *ast.SwitchStmt:
		ma.analyzeSwitchStatement(s, stats)
	case *ast.TypeSwitchStmt:
		ma.analyzeTypeSwitchStatement(s, stats)
	case *ast.SelectStmt:
		ma.analyzeSelectStatement(s, stats)
	case *ast.LabeledStmt:
		if s.Label != nil {
			ma.applyLabelSnapshot(s.Label.Name, stats)
		}
		ma.analyzeStatement(s.Stmt, stats)
	case *ast.BranchStmt:
		if s.Tok == token.GOTO && s.Label != nil {
			ma.captureGotoSnapshot(s.Label.Name, stats)
		}
	case *ast.BlockStmt:
		nestedStats := ma.analyzeBlock(s, stats)
		ma.copyStatsMap(stats, nestedStats)
	}
}

// analyzeAssignStatement handles assignments: collection-length bookkeeping,
// potential-panic-while-locked reporting, and TryLock result tracking (the
// latter delegated to the per-function tryLockTracker).
func (ma *Checker) analyzeAssignStatement(stmt *ast.AssignStmt, stats map[string]*Stats) {
	ma.panicDetector.recordCollectionLengthsFromAssign(stmt)
	ma.panicDetector.reportPotentialPanicWhileLocked(stmt, stats)
	ma.tryLock.recordAssignment(stmt)
}

func (ma *Checker) analyzeDeclStatement(stmt *ast.DeclStmt, stats map[string]*Stats) {
	ma.panicDetector.recordCollectionLengthsFromDecl(stmt)
	ma.panicDetector.reportPotentialPanicWhileLocked(stmt, stats)
}

// analyzeExpressionStatement handles expression statements (Lock/Unlock calls)
func (ma *Checker) analyzeExpressionStatement(stmt *ast.ExprStmt, stats map[string]*Stats) {
	expr := common.UnwrapParenExpr(stmt.X)

	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return
	}

	if ma.commentFilter.ShouldSkipCall(call) {
		return
	}

	if ma.applyLocalFunctionLiteralLifecycleEffects(call, stats) {
		return
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	if sel.Sel.Name == "Cleanup" && len(call.Args) == 1 {
		if fnlit, ok := call.Args[0].(*ast.FuncLit); ok {
			ma.handleDeferFunctionLiteral(fnlit, call.Pos(), stats)
		}
		return
	}

	varName := common.GetVarName(sel.X)

	// When a TryLock/TryRLock return value is ignored, the caller has no way to
	// know whether the lock was actually acquired, so any subsequent operation
	// that assumes the lock is held is racy.
	switch sel.Sel.Name {
	case "TryLock":
		if ma.mutexNames[varName] {
			ma.errorCollector.AddError(call.Pos(), category.UncheckedTryLock, "mutex '"+varName+"' TryLock return value not checked, lock may not be held")
			return
		}
		if ma.rwMutexNames[varName] {
			ma.errorCollector.AddError(call.Pos(), category.UncheckedTryLock, "rwmutex '"+varName+"' TryLock return value not checked, lock may not be held")
			return
		}
	case "TryRLock":
		if ma.rwMutexNames[varName] {
			ma.errorCollector.AddError(call.Pos(), category.UncheckedTryLock, "rwmutex '"+varName+"' TryRLock return value not checked, lock may not be held")
			return
		}
	}

	if ma.mutexNames[varName] {
		ma.handleMutexCall(varName, sel.Sel.Name, call.Pos(), stats)
	}

	if ma.rwMutexNames[varName] {
		ma.handleRWMutexCall(varName, sel.Sel.Name, call.Pos(), stats)
	}

	ma.applyLocalMethodLifecycleEffects(call, stats)
}

// handleMutexCall processes mutex method calls
func (ma *Checker) handleMutexCall(varName, methodName string, pos token.Pos, stats map[string]*Stats) {
	if ma.wrapper.resolve(varName, methodName) {
		return
	}

	switch methodName {
	case "Lock":
		if stats[varName].lock > 0 {
			ma.errorCollector.AddError(pos, category.DoubleLock, "mutex '"+varName+"' is re-locked before unlock")
		}
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			stats[varName].removeFirstBorrowedUnlockPos()
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "TryLock":
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			stats[varName].removeFirstBorrowedUnlockPos()
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "Unlock":
		if stats[varName].lock == 0 {
			if ma.loopCarry.isCarriedLoopUnlock(varName, pos, ma.function, []string{"Lock", "TryLock"}, []string{"Unlock"}) {
				return
			}
			stats[varName].borrowedLock++
			stats[varName].borrowedUnlockPos = append(stats[varName].borrowedUnlockPos, pos)
		} else {
			stats[varName].lock--
			stats[varName].removeFirstLockPos()
		}
	}
}

// handleRWMutexCall processes rwmutex method calls
func (ma *Checker) handleRWMutexCall(varName, methodName string, pos token.Pos, stats map[string]*Stats) {
	if ma.wrapper.resolve(varName, methodName) {
		return
	}

	switch methodName {
	case "Lock":
		if stats[varName].rlock > 0 {
			ma.errorCollector.AddError(pos, category.DoubleLock, "rwmutex '"+varName+"' attempts write Lock while read lock is held")
		}
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			stats[varName].removeFirstBorrowedUnlockPos()
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "TryLock":
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			stats[varName].removeFirstBorrowedUnlockPos()
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "Unlock":
		// Unlock called when only a read lock is held.
		// Correct the state as if RUnlock was called to avoid cascading errors.
		if stats[varName].rlock > 0 && stats[varName].lock == 0 {
			ma.errorCollector.AddError(pos, category.RWMutexAPIMismatch, "rwmutex '"+varName+"' Unlock called but only read lock is held, did you mean RUnlock?")
			stats[varName].rlock--
			stats[varName].removeFirstRLockPos()
			return
		}
		if stats[varName].lock == 0 {
			if ma.loopCarry.isCarriedLoopUnlock(varName, pos, ma.function, []string{"Lock", "TryLock"}, []string{"Unlock"}) {
				return
			}
			stats[varName].borrowedLock++
			stats[varName].borrowedUnlockPos = append(stats[varName].borrowedUnlockPos, pos)
		} else {
			stats[varName].lock--
			stats[varName].removeFirstLockPos()
		}
	case "RLock", "TryRLock":
		if stats[varName].borrowedRLock > 0 {
			stats[varName].borrowedRLock--
			stats[varName].removeFirstBorrowedRUnlockPos()
			return
		}
		stats[varName].rlock++
		stats[varName].rlockPos = append(stats[varName].rlockPos, pos)
	case "RUnlock":
		// RUnlock called when only a write lock is held.
		// Correct the state as if Unlock was called to avoid cascading errors.
		if stats[varName].lock > 0 && stats[varName].rlock == 0 {
			ma.errorCollector.AddError(pos, category.RWMutexAPIMismatch, "rwmutex '"+varName+"' RUnlock called but only write lock is held, did you mean Unlock?")
			stats[varName].lock--
			stats[varName].removeFirstLockPos()
			return
		}
		if stats[varName].rlock == 0 {
			if ma.loopCarry.isCarriedLoopUnlock(varName, pos, ma.function, []string{"RLock", "TryRLock"}, []string{"RUnlock"}) {
				return
			}
			stats[varName].borrowedRLock++
			stats[varName].borrowedRUnlockPos = append(stats[varName].borrowedRUnlockPos, pos)
		} else {
			stats[varName].rlock--
			stats[varName].removeFirstRLockPos()
		}
	}
}

func (ma *Checker) analyzeReturnStatement(stmt *ast.ReturnStmt, stats map[string]*Stats) {
	for _, result := range stmt.Results {
		call, ok := result.(*ast.CallExpr)
		if ok && !ma.commentFilter.ShouldSkipCall(call) {
			// Apply callee effects for `return helper()` forms.
			if !ma.applyLocalFunctionLiteralLifecycleEffects(call, stats) {
				ma.applyLocalMethodLifecycleEffects(call, stats)
			}
		}

		fnlit, ok := result.(*ast.FuncLit)
		if !ok {
			continue
		}
		ma.handleDeferFunctionLiteral(fnlit, stmt.Pos(), stats)
	}

	if !ma.rawBodyEffects {
		ma.reportUnmatchedLocks(stats)
	}
}

// analyzeDeferStatement handles defer statements
func (ma *Checker) analyzeDeferStatement(stmt *ast.DeferStmt, stats map[string]*Stats) {
	if ma.commentFilter.ShouldSkipCall(stmt.Call) {
		return
	}

	// Handle direct defer calls
	if call, ok := stmt.Call.Fun.(*ast.SelectorExpr); ok {
		ma.handleDeferCall(call, stmt.Pos(), stats)
		return
	}

	// Handle defer with function literals
	if fnlit, ok := stmt.Call.Fun.(*ast.FuncLit); ok {
		ma.handleDeferFunctionLiteral(fnlit, stmt.Pos(), stats)
	}
}

// handleDeferCall processes direct defer calls
func (ma *Checker) handleDeferCall(call *ast.SelectorExpr, pos token.Pos, stats map[string]*Stats) {
	varName := common.GetVarName(call.X)

	if call.Sel.Name == "Lock" && ma.consumeBorrowedDeferredLock(varName, stats) {
		return
	}
	if call.Sel.Name == "RLock" && ma.consumeBorrowedDeferredRLock(varName, stats) {
		return
	}
	if call.Sel.Name == "Lock" && ma.deferredRelockBalancesEarlierDeferredUnlock(varName, stats) {
		return
	}
	if call.Sel.Name == "RLock" && ma.deferredRRelockBalancesEarlierDeferredRUnlock(varName, stats) {
		return
	}

	// defer Lock / defer RLock re-acquires the lock on
	// function return instead of releasing it, guaranteed deadlock.
	if ma.mutexNames[varName] && call.Sel.Name == "Lock" {
		ma.errorCollector.AddError(pos, category.DeferLock, "mutex '"+varName+"' defer calls Lock instead of Unlock, will deadlock on return")
		return
	}
	if ma.rwMutexNames[varName] {
		switch call.Sel.Name {
		case "Lock":
			ma.errorCollector.AddError(pos, category.DeferLock, "rwmutex '"+varName+"' defer calls Lock instead of Unlock, will deadlock on return")
			return
		case "RLock":
			ma.errorCollector.AddError(pos, category.DeferLock, "rwmutex '"+varName+"' defer calls RLock instead of RUnlock, will deadlock on return")
			return
		}
	}

	if ma.mutexNames[varName] && call.Sel.Name == "Unlock" {
		ma.handleDeferUnlock(varName, pos, stats, false)
	}

	if ma.rwMutexNames[varName] {
		switch call.Sel.Name {
		case "Unlock":
			ma.handleDeferUnlock(varName, pos, stats, true)
		case "RUnlock":
			ma.handleDeferRUnlock(varName, pos, stats)
		}
	}
}

func (ma *Checker) consumeBorrowedDeferredLock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	if st == nil || st.borrowedLock == 0 {
		return false
	}
	st.borrowedLock--
	st.removeFirstBorrowedUnlockPos()
	return true
}

func (ma *Checker) consumeBorrowedDeferredRLock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	if st == nil || st.borrowedRLock == 0 {
		return false
	}
	st.borrowedRLock--
	st.removeFirstBorrowedRUnlockPos()
	return true
}

func (ma *Checker) deferredRelockBalancesEarlierDeferredUnlock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	return st != nil && st.lock == 0 && st.deferUnlock > 0
}

func (ma *Checker) deferredRRelockBalancesEarlierDeferredRUnlock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	return st != nil && st.rlock == 0 && st.deferRUnlock > 0
}

// handleDeferFunctionLiteral processes defer with function literals
func (ma *Checker) handleDeferFunctionLiteral(fnlit *ast.FuncLit, pos token.Pos, stats map[string]*Stats) {
	// Check for mutex unlocks in function literal
	for mutexName := range ma.mutexNames {
		if ma.containsUnlock(fnlit.Body, mutexName) && !ma.containsLock(fnlit.Body, mutexName) {
			if stats[mutexName].lock == 0 && ma.unlocksOnlyInRecoverGuard(fnlit.Body, mutexName, "Unlock") {
				continue
			}
			ma.handleDeferUnlock(mutexName, pos, stats, false)
		}
	}

	// Check for rwmutex unlocks in function literal
	for rwMutexName := range ma.rwMutexNames {
		if ma.containsUnlock(fnlit.Body, rwMutexName) && !ma.containsLock(fnlit.Body, rwMutexName) {
			if stats[rwMutexName].lock == 0 && ma.unlocksOnlyInRecoverGuard(fnlit.Body, rwMutexName, "Unlock") {
				continue
			}
			ma.handleDeferUnlock(rwMutexName, pos, stats, true)
		}
		if ma.containsRUnlock(fnlit.Body, rwMutexName) && !ma.containsRLock(fnlit.Body, rwMutexName) {
			if stats[rwMutexName].rlock == 0 && ma.unlocksOnlyInRecoverGuard(fnlit.Body, rwMutexName, "RUnlock") {
				continue
			}
			ma.handleDeferRUnlock(rwMutexName, pos, stats)
		}
	}
}
