package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
)

// handleDeferUnlock processes defer unlock calls
func (ma *Analyzer) handleDeferUnlock(varName string, pos token.Pos, stats map[string]*Stats, isRWMutex bool) {
	if stats[varName].lock == 0 {
		mutexType := "mutex"
		if isRWMutex {
			mutexType = "rwmutex"
		}
		ma.errorCollector.AddError(pos, mutexType+" '"+varName+"' has defer unlock but no corresponding lock")
		ma.deferErrors.badDeferUnlock[varName] = true
	} else {
		stats[varName].deferUnlock++
	}
}

// handleDeferRUnlock processes defer runlock calls
func (ma *Analyzer) handleDeferRUnlock(varName string, pos token.Pos, stats map[string]*Stats) {
	if stats[varName].rlock == 0 {
		ma.errorCollector.AddError(pos, "rwmutex '"+varName+"' has defer runlock but no corresponding rlock")
		ma.deferErrors.badDeferRUnlock[varName] = true
	} else {
		stats[varName].deferRUnlock++
	}
}

// analyzeRangeStatement handles range statements
func (ma *Analyzer) analyzeRangeStatement(stmt *ast.RangeStmt, stats map[string]*Stats) {
	rangeStats := ma.analyzeBlock(stmt.Body, stats)
	ma.replaceStats(stats, rangeStats)
}

// analyzeSwitchStatement handles switch statements
func (ma *Analyzer) analyzeSwitchStatement(stmt *ast.SwitchStmt, stats map[string]*Stats) {
	for _, caseStmt := range stmt.Body.List {
		if cc, ok := caseStmt.(*ast.CaseClause); ok {
			caseStats := ma.analyzeStatements(cc.Body, stats)
			ma.reportUnmatchedLocksInBranch(stats, caseStats, "case")
		}
	}
}

// analyzeTypeSwitchStatement handles type switch statements
func (ma *Analyzer) analyzeTypeSwitchStatement(stmt *ast.TypeSwitchStmt, stats map[string]*Stats) {
	for _, caseStmt := range stmt.Body.List {
		if cc, ok := caseStmt.(*ast.CaseClause); ok {
			caseStats := ma.analyzeStatements(cc.Body, stats)
			ma.reportUnmatchedLocksInBranch(stats, caseStats, "case")
		}
	}
}

// analyzeSelectStatement handles select statements
func (ma *Analyzer) analyzeSelectStatement(stmt *ast.SelectStmt, stats map[string]*Stats) {
	for _, commClause := range stmt.Body.List {
		if cc, ok := commClause.(*ast.CommClause); ok {
			commStats := ma.analyzeStatements(cc.Body, stats)
			ma.reportUnmatchedLocksInBranch(stats, commStats, "select")
		}
	}
}

// analyzeStatements is a helper to analyze a list of statements
func (ma *Analyzer) analyzeStatements(stmts []ast.Stmt, initial map[string]*Stats) map[string]*Stats {
	blockStats := ma.copyStats(initial)

	for _, stmt := range stmts {
		if ma.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		ma.analyzeStatement(stmt, blockStats)
	}

	return blockStats
}

// analyzeIfStatement handles if statements with proper branch analysis
func (ma *Analyzer) analyzeIfStatement(stmt *ast.IfStmt, stats map[string]*Stats) {
	if ma.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	thenBase, elseBase := ma.branchInitialStatsForCondition(stmt.Cond, stats)
	thenStats := ma.analyzeBlock(stmt.Body, thenBase)

	if stmt.Else != nil {
		elseStats := ma.analyzeElseBranch(stmt.Else, elseBase)
		if ma.canMergeBranchStates(thenStats, elseStats) {
			ma.replaceStats(stats, thenStats)
			return
		}
		ma.reportUnmatchedLocksInBranch(stats, thenStats, "if")
		ma.reportUnmatchedLocksInBranch(stats, elseStats, "else")
		return
	}

	ma.reportUnmatchedLocksInBranch(stats, thenStats, "if")
}

func (ma *Analyzer) branchInitialStatsForCondition(cond ast.Expr, stats map[string]*Stats) (map[string]*Stats, map[string]*Stats) {
	thenStats := ma.copyStats(stats)
	elseStats := ma.copyStats(stats)

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
func (ma *Analyzer) analyzeElseBranch(elseNode ast.Stmt, stats map[string]*Stats) map[string]*Stats {
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

func (ma *Analyzer) canMergeBranchStates(a, b map[string]*Stats) bool {
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

func (ma *Analyzer) sameBranchState(a, b *Stats) bool {
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
func (ma *Analyzer) analyzeGoStatement(stmt *ast.GoStmt) {
	if ma.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	if fnLit, ok := stmt.Call.Fun.(*ast.FuncLit); ok {
		goStats := ma.analyzeBlock(fnLit.Body, ma.stats)
		ma.reportUnmatchedLocksInBranch(ma.stats, goStats, "goroutine")
	}
}

// analyzeForStatement handles for loop statements
func (ma *Analyzer) analyzeForStatement(stmt *ast.ForStmt, stats map[string]*Stats) {
	if ma.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	forStats := ma.analyzeBlock(stmt.Body, stats)
	ma.replaceStats(stats, forStats)
}

// reportUnmatchedLocksInBranch reports unmatched locks in conditional branches
func (ma *Analyzer) reportUnmatchedLocksInBranch(initial, final map[string]*Stats, branchType string) {
	for mutexName := range ma.mutexNames {
		ma.reportBranchDelta(mutexName, initial[mutexName], final[mutexName], false, branchType)
	}

	for rwMutexName := range ma.rwMutexNames {
		ma.reportBranchDelta(rwMutexName, initial[rwMutexName], final[rwMutexName], true, branchType)
	}
}

// reportBranchDelta reports only the extra locks that remain held compared to
// the branch entry state.
func (ma *Analyzer) reportBranchDelta(mutexName string, initial, final *Stats, isRWMutex bool, branchType string) {
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
	if delta := ma.remainingLockCount(final.lock, final.deferUnlock) - ma.remainingLockCount(initial.lock, initial.deferUnlock); delta > 0 {
		for _, pos := range ma.trailingPositions(final.lockPos, delta) {
			ma.errorCollector.AddError(pos, lockMessage)
		}
	}

	unlockMessage := mutexType + " '" + mutexName + "' is unlocked but not locked"
	if delta := final.borrowedLock - initial.borrowedLock; delta > 0 {
		for _, pos := range ma.trailingPositions(final.borrowedUnlockPos, delta) {
			ma.errorCollector.AddError(pos, unlockMessage)
		}
	}

	if isRWMutex {
		rlockMessage := "rwmutex '" + mutexName + "' is rlocked but not runlocked in " + branchType
		if delta := ma.remainingLockCount(final.rlock, final.deferRUnlock) - ma.remainingLockCount(initial.rlock, initial.deferRUnlock); delta > 0 {
			for _, pos := range ma.trailingPositions(final.rlockPos, delta) {
				ma.errorCollector.AddError(pos, rlockMessage)
			}
		}

		runlockMessage := "rwmutex '" + mutexName + "' is runlocked but not rlocked"
		if delta := final.borrowedRLock - initial.borrowedRLock; delta > 0 {
			for _, pos := range ma.trailingPositions(final.borrowedRUnlockPos, delta) {
				ma.errorCollector.AddError(pos, runlockMessage)
			}
		}
	}
}

func (ma *Analyzer) remainingLockCount(lockCount, deferredUnlocks int) int {
	if lockCount <= deferredUnlocks {
		return 0
	}
	return lockCount - deferredUnlocks
}

func (ma *Analyzer) trailingPositions(positions []token.Pos, count int) []token.Pos {
	if count <= 0 {
		return nil
	}
	if count >= len(positions) {
		return positions
	}
	return positions[len(positions)-count:]
}

// reportUnmatchedMutexLocksWithContext reports unmatched locks for a specific mutex with context
func (ma *Analyzer) reportUnmatchedMutexLocksWithContext(mutexName string, stats *Stats, isRWMutex bool, branchType string) {
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

	for _, pos := range ma.trailingPositions(stats.lockPos, ma.remainingLockCount(stats.lock, stats.deferUnlock)) {
		ma.errorCollector.AddError(pos, lockMessage)
	}

	for _, pos := range stats.borrowedUnlockPos {
		ma.errorCollector.AddError(pos, mutexType+" '"+mutexName+"' is unlocked but not locked")
	}

	if isRWMutex {
		for _, pos := range ma.trailingPositions(stats.rlockPos, ma.remainingLockCount(stats.rlock, stats.deferRUnlock)) {
			ma.errorCollector.AddError(pos, rlockMessage)
		}
		for _, pos := range stats.borrowedRUnlockPos {
			ma.errorCollector.AddError(pos, "rwmutex '"+mutexName+"' is runlocked but not rlocked")
		}
	}
}

// reportUnmatchedMutexLocks reports unmatched locks for a specific mutex
func (ma *Analyzer) reportUnmatchedMutexLocks(mutexName string, stats *Stats, isRWMutex bool) {
	// Call the context-aware version with empty context for function-level reporting
	ma.reportUnmatchedMutexLocksWithContext(mutexName, stats, isRWMutex, "")
}

// reportUnmatchedLocks reports any remaining unmatched locks at function level
func (ma *Analyzer) reportUnmatchedLocks(stats map[string]*Stats) {
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
}
