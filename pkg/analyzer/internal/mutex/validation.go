package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

// analyzeRangeStatement handles range statements
func (ma *Checker) analyzeRangeStatement(stmt *ast.RangeStmt, stats map[string]*Stats) {
	newLoopMutexDetector(ma.errorCollector, ma.typesInfo).check(stmt.Body)
	ma.loopCarry.reportDeferredUnlocksInLoop(stmt.Body)
	rangeStats := ma.analyzeBlock(stmt.Body, stats)
	copyStatsMap(stats, rangeStats)
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
			copyStatsMap(stats, thenStats)
			return
		}

		if stmt.Else != nil {
			elseStats := ma.analyzeElseBranch(stmt.Else, stats)
			copyStatsMap(stats, elseStats)
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
			copyStatsMap(stats, thenStats)
			return
		}

		// Only merge states from branches that can reach the next statement.
		switch {
		case thenTerminates && elseTerminates:
			return
		case thenTerminates:
			copyStatsMap(stats, elseStats)
			return
		case elseTerminates:
			copyStatsMap(stats, thenStats)
			return
		}

		ma.reportUnmatchedLocksInBranch(stats, thenStats, "if")
		ma.reportUnmatchedLocksInBranch(stats, elseStats, "else")
		return
	}

	if thenTerminates {
		copyStatsMap(stats, elseBase)
		return
	}
	ma.reportUnmatchedLocksInBranch(stats, thenStats, "if")
}

func (ma *Checker) branchInitialStatsForCondition(cond ast.Expr, stats map[string]*Stats) (map[string]*Stats, map[string]*Stats) {
	thenStats := cloneStatsMap(stats)
	elseStats := cloneStatsMap(stats)

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
		goInitial := emptyStatsLike(stats)
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

	newLoopMutexDetector(ma.errorCollector, ma.typesInfo).check(stmt.Body)
	ma.loopCarry.reportDeferredUnlocksInLoop(stmt.Body)
	forStats := ma.analyzeBlock(stmt.Body, stats)
	ma.loopCarry.applyLoopExitLocks(stmt, stats, forStats)
	if stmt.Cond == nil && ma.termination.blockContainsReturn(stmt.Body) && !ma.termination.blockContainsBreak(stmt.Body) {
		clearStats(stats)
		return
	}
	copyStatsMap(stats, forStats)
}
