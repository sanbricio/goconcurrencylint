package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

// handleDeferUnlock processes defer unlock calls
func (ma *Checker) handleDeferUnlock(varName string, pos token.Pos, stats map[string]*Stats, isRWMutex bool) {
	if stats[varName].lock == 0 {
		mutexType := "mutex"
		if isRWMutex {
			mutexType = "rwmutex"
		}
		ma.errorCollector.AddError(pos, category.DeferUnlockWithoutLock, mutexType+" '"+varName+"' has defer unlock but no corresponding lock")
		ma.deferErrors.badDeferUnlock[varName] = true
	} else {
		stats[varName].deferUnlock++
	}
}

// handleDeferRUnlock processes defer runlock calls
func (ma *Checker) handleDeferRUnlock(varName string, pos token.Pos, stats map[string]*Stats) {
	if stats[varName].rlock == 0 {
		ma.errorCollector.AddError(pos, category.DeferUnlockWithoutLock, "rwmutex '"+varName+"' has defer runlock but no corresponding rlock")
		ma.deferErrors.badDeferRUnlock[varName] = true
	} else {
		stats[varName].deferRUnlock++
	}
}

// analyzeRangeStatement handles range statements
func (ma *Checker) analyzeRangeStatement(stmt *ast.RangeStmt, stats map[string]*Stats) {
	ma.checkMutexDeclaredInLoop(stmt.Body)
	ma.loopCarry.reportDeferredUnlocksInLoop(stmt.Body)
	rangeStats := ma.analyzeBlock(stmt.Body, stats)
	ma.copyStatsMap(stats, rangeStats)
}

// analyzeSwitchStatement handles switch statements
func (ma *Checker) analyzeSwitchStatement(stmt *ast.SwitchStmt, stats map[string]*Stats) {
	for _, caseStmt := range stmt.Body.List {
		if cc, ok := caseStmt.(*ast.CaseClause); ok {
			caseStats := ma.analyzeStatements(cc.Body, stats)
			ma.reportUnmatchedLocksInBranch(stats, caseStats, "case")
		}
	}
}

// analyzeTypeSwitchStatement handles type switch statements
func (ma *Checker) analyzeTypeSwitchStatement(stmt *ast.TypeSwitchStmt, stats map[string]*Stats) {
	for _, caseStmt := range stmt.Body.List {
		if cc, ok := caseStmt.(*ast.CaseClause); ok {
			caseStats := ma.analyzeStatements(cc.Body, stats)
			ma.reportUnmatchedLocksInBranch(stats, caseStats, "case")
		}
	}
}

// analyzeSelectStatement handles select statements
func (ma *Checker) analyzeSelectStatement(stmt *ast.SelectStmt, stats map[string]*Stats) {
	for _, commClause := range stmt.Body.List {
		if cc, ok := commClause.(*ast.CommClause); ok {
			commStats := ma.analyzeStatements(cc.Body, stats)
			ma.reportUnmatchedLocksInBranch(stats, commStats, "select")
		}
	}
}

// analyzeStatements is a helper to analyze a list of statements
func (ma *Checker) analyzeStatements(stmts []ast.Stmt, initial map[string]*Stats) map[string]*Stats {
	return ma.analyzeStatementList(stmts, initial)
}

// analyzeIfStatement handles if statements with proper branch analysis
func (ma *Checker) analyzeIfStatement(stmt *ast.IfStmt, stats map[string]*Stats) {
	if ma.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	if stmt.Init != nil {
		ma.analyzeStatement(stmt.Init, stats)
	}

	if condValue, ok := common.ConstantBoolValue(stmt.Cond, ma.typesInfo); ok {
		if condValue {
			thenStats := ma.analyzeBlock(stmt.Body, stats)
			ma.copyStatsMap(stats, thenStats)
			return
		}

		if stmt.Else != nil {
			elseStats := ma.analyzeElseBranch(stmt.Else, stats)
			ma.copyStatsMap(stats, elseStats)
		}
		return
	}

	thenBase, elseBase := ma.branchInitialStatsForCondition(stmt.Cond, stats)
	thenStats := ma.analyzeBlock(stmt.Body, thenBase)
	thenTerminates := ma.termination.blockAlwaysTerminates(stmt.Body)

	if stmt.Else != nil {
		elseStats := ma.analyzeElseBranch(stmt.Else, elseBase)
		elseTerminates := ma.termination.elseAlwaysTerminates(stmt.Else)

		if ma.canMergeBranchStates(thenStats, elseStats) {
			ma.copyStatsMap(stats, thenStats)
			return
		}

		// Only merge states from branches that can reach the next statement.
		switch {
		case thenTerminates && elseTerminates:
			return
		case thenTerminates:
			ma.copyStatsMap(stats, elseStats)
			return
		case elseTerminates:
			ma.copyStatsMap(stats, thenStats)
			return
		}

		ma.reportUnmatchedLocksInBranch(stats, thenStats, "if")
		ma.reportUnmatchedLocksInBranch(stats, elseStats, "else")
		return
	}

	if thenTerminates {
		ma.copyStatsMap(stats, elseBase)
		return
	}
	ma.reportUnmatchedLocksInBranch(stats, thenStats, "if")
}

func (ma *Checker) branchInitialStatsForCondition(cond ast.Expr, stats map[string]*Stats) (map[string]*Stats, map[string]*Stats) {
	thenStats := ma.cloneStatsMap(stats)
	elseStats := ma.cloneStatsMap(stats)

	if unary, ok := cond.(*ast.UnaryExpr); ok && unary.Op == token.NOT {
		negatedThen, negatedElse := ma.branchInitialStatsForCondition(unary.X, stats)
		return negatedElse, negatedThen
	}

	if ma.tryLock.applyToBranch(cond, thenStats) {
		return thenStats, elseStats
	}

	call, ok := cond.(*ast.CallExpr)
	if !ok {
		return thenStats, elseStats
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return thenStats, elseStats
	}

	varName := common.GetVarName(sel.X)
	switch sel.Sel.Name {
	case "TryLock":
		if ma.mutexNames[varName] || ma.rwMutexNames[varName] {
			thenStats[varName].lock++
			thenStats[varName].lockPos = append(thenStats[varName].lockPos, call.Pos())
		}
	case "TryRLock":
		if ma.rwMutexNames[varName] {
			thenStats[varName].rlock++
			thenStats[varName].rlockPos = append(thenStats[varName].rlockPos, call.Pos())
		}
	}

	return thenStats, elseStats
}

// analyzeElseBranch handles else branches (both else and else if)
func (ma *Checker) analyzeElseBranch(elseNode ast.Stmt, stats map[string]*Stats) map[string]*Stats {
	switch e := elseNode.(type) {
	case *ast.BlockStmt:
		return ma.analyzeBlock(e, stats)
	case *ast.IfStmt:
		// For else if, create a synthetic block
		syntheticBlock := &ast.BlockStmt{List: []ast.Stmt{e}}
		return ma.analyzeBlock(syntheticBlock, stats)
	default:
		return make(map[string]*Stats)
	}
}

func (ma *Checker) canMergeBranchStates(a, b map[string]*Stats) bool {
	for name := range ma.mutexNames {
		if !ma.sameBranchState(a[name], b[name]) {
			return false
		}
	}

	for name := range ma.rwMutexNames {
		if !ma.sameBranchState(a[name], b[name]) {
			return false
		}
	}

	return true
}

func (ma *Checker) sameBranchState(a, b *Stats) bool {
	if a == nil || b == nil {
		return a == b
	}

	return a.lock == b.lock &&
		a.rlock == b.rlock &&
		a.borrowedLock == b.borrowedLock &&
		a.borrowedRLock == b.borrowedRLock &&
		a.deferUnlock == b.deferUnlock &&
		a.deferRUnlock == b.deferRUnlock
}

// analyzeGoStatement handles goroutine statements
func (ma *Checker) analyzeGoStatement(stmt *ast.GoStmt, stats map[string]*Stats) {
	if ma.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	if fnLit, ok := stmt.Call.Fun.(*ast.FuncLit); ok {
		cg := newCrossGoroutineDetector(ma.mutexNames, ma.rwMutexNames, ma.commentFilter, ma.typesInfo)
		// Record goroutines launched while a mutex is held that also try to
		// acquire the same mutex. The conflict is reported at function exit if
		// the parent still holds the lock.
		for varName := range ma.mutexNames {
			if stats[varName] != nil && stats[varName].lock > 0 {
				if method, ok := cg.goroutineBodyLockCallMethod(fnLit.Body, varName, []string{"Lock"}); ok {
					if cg.parentBlocksBeforeUnlock(ma.function, stmt.Pos(), varName, []string{"Unlock"}) {
						ma.errorCollector.AddError(stmt.Pos(), category.GoroutineLockDeadlock,
							cg.deadlockMessage(varName, false, false, method, true))
						continue
					}
					ma.goroutineLockConflicts = append(ma.goroutineLockConflicts, goroutineLockConflict{varName: varName, pos: stmt.Pos(), requestMethod: method})
				}
			}
		}
		for varName := range ma.rwMutexNames {
			if stats[varName] != nil && stats[varName].lock > 0 {
				if method, ok := cg.goroutineBodyLockCallMethod(fnLit.Body, varName, []string{"Lock", "RLock"}); ok {
					if cg.parentBlocksBeforeUnlock(ma.function, stmt.Pos(), varName, []string{"Unlock"}) {
						ma.errorCollector.AddError(stmt.Pos(), category.GoroutineLockDeadlock,
							cg.deadlockMessage(varName, true, false, method, true))
						continue
					}
					ma.goroutineLockConflicts = append(ma.goroutineLockConflicts, goroutineLockConflict{varName: varName, pos: stmt.Pos(), isRWMutex: true, requestMethod: method})
				}
			}
			if stats[varName] != nil && stats[varName].rlock > 0 {
				if method, ok := cg.goroutineBodyLockCallMethod(fnLit.Body, varName, []string{"Lock"}); ok {
					if cg.parentBlocksBeforeUnlock(ma.function, stmt.Pos(), varName, []string{"RUnlock"}) {
						ma.errorCollector.AddError(stmt.Pos(), category.GoroutineLockDeadlock,
							cg.deadlockMessage(varName, true, true, method, true))
						continue
					}
					ma.goroutineLockConflicts = append(ma.goroutineLockConflicts, goroutineLockConflict{varName: varName, pos: stmt.Pos(), isRWMutex: true, parentReadLock: true, requestMethod: method})
				}
			}
		}

		crossReleases := cg.collectReleases(fnLit.Body, stats)
		goInitial := ma.emptyStatsLike(stats)
		goStats := ma.analyzeBlock(fnLit.Body, goInitial)
		cg.suppressBorrowedReleases(goStats, crossReleases)
		ma.reportUnmatchedLocksInBranch(goInitial, goStats, "goroutine")
		cg.applyReleases(stats, crossReleases)
		return
	}

	// `go someMethod()` runs the method asynchronously in another goroutine;
	// its Lock/Unlock effects belong to that goroutine, not the caller's state.
}

// analyzeForStatement handles for loop statements
func (ma *Checker) analyzeForStatement(stmt *ast.ForStmt, stats map[string]*Stats) {
	if ma.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	ma.checkMutexDeclaredInLoop(stmt.Body)
	ma.loopCarry.reportDeferredUnlocksInLoop(stmt.Body)
	forStats := ma.analyzeBlock(stmt.Body, stats)
	ma.loopCarry.applyLoopExitLocks(stmt, stats, forStats)
	if stmt.Cond == nil && ma.termination.blockContainsReturn(stmt.Body) && !ma.termination.blockContainsBreak(stmt.Body) {
		ma.clearStats(stats)
		return
	}
	ma.copyStatsMap(stats, forStats)
}

// reportUnmatchedLocksInBranch reports unmatched locks in conditional branches
func (ma *Checker) reportUnmatchedLocksInBranch(initial, final map[string]*Stats, branchType string) {
	if ma.rawBodyEffects {
		return
	}

	for mutexName := range ma.mutexNames {
		ma.reportBranchDelta(mutexName, initial[mutexName], final[mutexName], false, branchType)
	}

	for rwMutexName := range ma.rwMutexNames {
		ma.reportBranchDelta(rwMutexName, initial[rwMutexName], final[rwMutexName], true, branchType)
	}
}

// reportBranchDelta reports only the extra locks that remain held compared to
// the branch entry state.
func (ma *Checker) reportBranchDelta(mutexName string, initial, final *Stats, isRWMutex bool, branchType string) {
	if final == nil {
		return
	}
	if initial == nil {
		initial = &Stats{}
	}

	mutexType := "mutex"
	if isRWMutex {
		mutexType = "rwmutex"
	}

	lockMessage := mutexType + " '" + mutexName + "' is locked but not unlocked in " + branchType
	if delta := remainingLockCount(final.lock, final.deferUnlock) - remainingLockCount(initial.lock, initial.deferUnlock); delta > 0 {
		for _, pos := range ma.trailingPositions(final.lockPos, delta) {
			ma.errorCollector.AddError(pos, category.LockWithoutUnlock, lockMessage)
		}
	}

	suppressBorrowedUnlock := ma.unlockDiagnosticSuppressed(mutexName, []string{"Lock", "TryLock"}) ||
		ma.terminatingTailUnlockSuppressed(mutexName)
	unlockMessage := mutexType + " '" + mutexName + "' is unlocked but not locked"
	if delta := final.borrowedLock - initial.borrowedLock; delta > 0 && !suppressBorrowedUnlock {
		for _, pos := range ma.trailingPositions(final.borrowedUnlockPos, delta) {
			ma.errorCollector.AddError(pos, category.UnlockWithoutLock, unlockMessage)
		}
	}

	if isRWMutex {
		rlockMessage := "rwmutex '" + mutexName + "' is rlocked but not runlocked in " + branchType
		if delta := remainingLockCount(final.rlock, final.deferRUnlock) - remainingLockCount(initial.rlock, initial.deferRUnlock); delta > 0 {
			for _, pos := range ma.trailingPositions(final.rlockPos, delta) {
				ma.errorCollector.AddError(pos, category.LockWithoutUnlock, rlockMessage)
			}
		}

		suppressBorrowedRUnlock := ma.unlockDiagnosticSuppressed(mutexName, []string{"RLock", "TryRLock"}) ||
			ma.terminatingTailUnlockSuppressed(mutexName)
		runlockMessage := "rwmutex '" + mutexName + "' is runlocked but not rlocked"
		if delta := final.borrowedRLock - initial.borrowedRLock; delta > 0 && !suppressBorrowedRUnlock {
			for _, pos := range ma.trailingPositions(final.borrowedRUnlockPos, delta) {
				ma.errorCollector.AddError(pos, category.UnlockWithoutLock, runlockMessage)
			}
		}
	}
}

// unlockDiagnosticSuppressed reports whether ownership is managed outside the
// current lock/unlock pair.
func (ma *Checker) unlockDiagnosticSuppressed(mutexName string, acquireMethods []string) bool {
	return ma.lifecycle.isReleaseFor(mutexName, acquireMethods) ||
		ma.lifecycle.isCallerManagedReleaseFor(mutexName, acquireMethods) ||
		ma.functionIsParameterUnlockHelper(mutexName, acquireMethods)
}

// terminatingTailUnlockSuppressed reports caller-owned unlocks before a
// terminating tail.
func (ma *Checker) terminatingTailUnlockSuppressed(mutexName string) bool {
	return ma.terminatingTailDepth > 0 && ma.varRootIsFunctionParameter(mutexName)
}

func remainingLockCount(lockCount, deferredUnlocks int) int {
	if lockCount <= deferredUnlocks {
		return 0
	}
	return lockCount - deferredUnlocks
}

func (ma *Checker) trailingPositions(positions []token.Pos, count int) []token.Pos {
	if count <= 0 {
		return nil
	}
	if count >= len(positions) {
		return positions
	}
	return positions[len(positions)-count:]
}

// reportUnmatchedMutexLocksWithContext reports unmatched locks for a specific mutex with context
func (ma *Checker) reportUnmatchedMutexLocksWithContext(mutexName string, stats *Stats, isRWMutex bool, branchType string) {
	if stats == nil {
		return
	}

	mutexType := "mutex"
	if isRWMutex {
		mutexType = "rwmutex"
	}

	// Create context-aware messages
	var lockMessage, rlockMessage string
	if branchType == "" {
		// For function-level reporting (no context)
		lockMessage = mutexType + " '" + mutexName + "' is locked but not unlocked"
		rlockMessage = "rwmutex '" + mutexName + "' is rlocked but not runlocked"
	} else {
		// For branch-level reporting (with context)
		lockMessage = mutexType + " '" + mutexName + "' is locked but not unlocked in " + branchType
		rlockMessage = "rwmutex '" + mutexName + "' is rlocked but not runlocked in " + branchType
	}

	suppressFunctionLevelLock := branchType == "" && ma.lifecycle.returnsHandleFor(mutexName, []string{"Unlock"})
	for _, pos := range ma.trailingPositions(stats.lockPos, remainingLockCount(stats.lock, stats.deferUnlock)) {
		if suppressFunctionLevelLock {
			continue
		}
		ma.errorCollector.AddError(pos, category.LockWithoutUnlock, lockMessage)
	}

	suppressFunctionLevelUnlock := branchType == "" && ma.unlockDiagnosticSuppressed(mutexName, []string{"Lock", "TryLock"})
	for _, pos := range stats.borrowedUnlockPos {
		if suppressFunctionLevelUnlock {
			continue
		}
		ma.errorCollector.AddError(pos, category.UnlockWithoutLock, mutexType+" '"+mutexName+"' is unlocked but not locked")
	}

	if isRWMutex {
		suppressFunctionLevelRLock := branchType == "" && ma.lifecycle.returnsHandleFor(mutexName, []string{"RUnlock"})
		for _, pos := range ma.trailingPositions(stats.rlockPos, remainingLockCount(stats.rlock, stats.deferRUnlock)) {
			if suppressFunctionLevelRLock {
				continue
			}
			ma.errorCollector.AddError(pos, category.LockWithoutUnlock, rlockMessage)
		}
		suppressFunctionLevelRUnlock := branchType == "" && ma.unlockDiagnosticSuppressed(mutexName, []string{"RLock", "TryRLock"})
		for _, pos := range stats.borrowedRUnlockPos {
			if suppressFunctionLevelRUnlock {
				continue
			}
			ma.errorCollector.AddError(pos, category.UnlockWithoutLock, "rwmutex '"+mutexName+"' is runlocked but not rlocked")
		}
	}
}

// reportUnmatchedMutexLocks reports unmatched locks for a specific mutex
func (ma *Checker) reportUnmatchedMutexLocks(mutexName string, stats *Stats, isRWMutex bool) {
	// Call the context-aware version with empty context for function-level reporting
	ma.reportUnmatchedMutexLocksWithContext(mutexName, stats, isRWMutex, "")
}

// reportUnmatchedLocks reports any remaining unmatched locks at function level
func (ma *Checker) reportUnmatchedLocks(stats map[string]*Stats) {
	if ma.rawBodyEffects {
		return
	}

	for mutexName := range ma.mutexNames {
		if ma.deferErrors.badDeferUnlock[mutexName] {
			continue
		}
		ma.reportUnmatchedMutexLocks(mutexName, stats[mutexName], false)
	}

	for rwMutexName := range ma.rwMutexNames {
		if ma.deferErrors.badDeferUnlock[rwMutexName] || ma.deferErrors.badDeferRUnlock[rwMutexName] {
			continue
		}
		ma.reportUnmatchedMutexLocks(rwMutexName, stats[rwMutexName], true)
	}

	// Report goroutine-parent deadlocks only when the parent exits while still
	// holding the lock, so the goroutine can never acquire it.
	cg := newCrossGoroutineDetector(ma.mutexNames, ma.rwMutexNames, ma.commentFilter, ma.typesInfo)
	for _, conflict := range ma.goroutineLockConflicts {
		st := stats[conflict.varName]
		if st == nil {
			continue
		}
		if conflict.parentReadLock {
			if remainingLockCount(st.rlock, st.deferRUnlock) > 0 {
				ma.errorCollector.AddError(conflict.pos, category.GoroutineLockDeadlock,
					cg.deadlockMessage(conflict.varName, true, true, conflict.requestMethod, false))
			}
			continue
		}

		if remainingLockCount(st.lock, st.deferUnlock) > 0 {
			ma.errorCollector.AddError(conflict.pos, category.GoroutineLockDeadlock,
				cg.deadlockMessage(conflict.varName, conflict.isRWMutex, false, conflict.requestMethod, false))
		}
	}
}

// checkMutexDeclaredInLoop reports sync.Mutex / sync.RWMutex variables that are
// declared directly inside a loop body.  Each iteration creates a fresh mutex
// that is invisible to other iterations and therefore cannot protect shared state.
// Only the top-level statements of the loop body are examined; nested loops are
// handled when they themselves are analysed as for/range statements.
// Function literals inside the loop are skipped to avoid false positives for
// patterns like `for { go func() { var mu sync.Mutex; … }() }`.
func (ma *Checker) checkMutexDeclaredInLoop(loopBody *ast.BlockStmt) {
	if loopBody == nil || ma.typesInfo == nil {
		return
	}
	for _, stmt := range loopBody.List {
		switch s := stmt.(type) {
		case *ast.DeclStmt:
			gen, ok := s.Decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				ma.reportMutexInLoopValueSpec(vs)
			}
		case *ast.AssignStmt:
			if s.Tok == token.DEFINE {
				ma.reportMutexInLoopAssign(s)
			}
		}
	}
}

func (ma *Checker) reportMutexInLoopValueSpec(vs *ast.ValueSpec) {
	for _, name := range vs.Names {
		obj := ma.typesInfo.Defs[name]
		if obj == nil {
			continue
		}
		typ := obj.Type()
		switch {
		case common.IsMutex(typ):
			ma.errorCollector.AddError(name.Pos(),
				category.MutexInLoop, "mutex '"+name.Name+"' declared inside loop, each iteration creates a new mutex that cannot protect shared state")
		case common.IsRWMutex(typ):
			ma.errorCollector.AddError(name.Pos(),
				category.MutexInLoop, "rwmutex '"+name.Name+"' declared inside loop, each iteration creates a new mutex that cannot protect shared state")
		}
	}
}

func (ma *Checker) reportMutexInLoopAssign(s *ast.AssignStmt) {
	for i, lhs := range s.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || i >= len(s.Rhs) {
			continue
		}
		typ := ma.typesInfo.TypeOf(s.Rhs[i])
		if typ == nil {
			continue
		}
		typ = common.DerefOnceAndUnalias(typ)
		switch {
		case common.IsMutex(typ):
			ma.errorCollector.AddError(ident.Pos(),
				category.MutexInLoop, "mutex '"+ident.Name+"' declared inside loop, each iteration creates a new mutex that cannot protect shared state")
		case common.IsRWMutex(typ):
			ma.errorCollector.AddError(ident.Pos(),
				category.MutexInLoop, "rwmutex '"+ident.Name+"' declared inside loop, each iteration creates a new mutex that cannot protect shared state")
		}
	}
}
