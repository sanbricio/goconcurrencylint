package mutex

import (
	"go/ast"
	"go/token"
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
		stats[varName].lock--
		ma.removeFirstLockPos(stats[varName])
	}
}

// handleDeferRUnlock processes defer runlock calls
func (ma *Analyzer) handleDeferRUnlock(varName string, pos token.Pos, stats map[string]*Stats) {
	if stats[varName].rlock == 0 {
		ma.errorCollector.AddError(pos, "rwmutex '"+varName+"' has defer runlock but no corresponding rlock")
		ma.deferErrors.badDeferRUnlock[varName] = true
	} else {
		stats[varName].rlock--
		ma.removeFirstRLockPos(stats[varName])
	}
}

// analyzeRangeStatement handles range statements
func (ma *Analyzer) analyzeRangeStatement(stmt *ast.RangeStmt, stats map[string]*Stats) {
	rangeStats := ma.analyzeBlock(stmt.Body)
	ma.mergeStats(stats, rangeStats)
}

// analyzeSwitchStatement handles switch statements
func (ma *Analyzer) analyzeSwitchStatement(stmt *ast.SwitchStmt) {
	for _, caseStmt := range stmt.Body.List {
		if cc, ok := caseStmt.(*ast.CaseClause); ok {
			caseStats := ma.analyzeStatements(cc.Body)
			ma.reportUnmatchedLocksInBranch(caseStats, "case")
		}
	}
}

// analyzeTypeSwitchStatement handles type switch statements
func (ma *Analyzer) analyzeTypeSwitchStatement(stmt *ast.TypeSwitchStmt) {
	for _, caseStmt := range stmt.Body.List {
		if cc, ok := caseStmt.(*ast.CaseClause); ok {
			caseStats := ma.analyzeStatements(cc.Body)
			ma.reportUnmatchedLocksInBranch(caseStats, "case")
		}
	}
}

// analyzeSelectStatement handles select statements
func (ma *Analyzer) analyzeSelectStatement(stmt *ast.SelectStmt) {
	for _, commClause := range stmt.Body.List {
		if cc, ok := commClause.(*ast.CommClause); ok {
			commStats := ma.analyzeStatements(cc.Body)
			ma.reportUnmatchedLocksInBranch(commStats, "select")
		}
	}
}

// analyzeStatements is a helper to analyze a list of statements
func (ma *Analyzer) analyzeStatements(stmts []ast.Stmt) map[string]*Stats {
	blockStats := ma.copyStats(ma.stats)

	for _, stmt := range stmts {
		if ma.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		ma.analyzeStatement(stmt, blockStats)
	}

	return blockStats
}

// analyzeIfStatement handles if statements with proper branch analysis
func (ma *Analyzer) analyzeIfStatement(stmt *ast.IfStmt) {
	if ma.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	thenStats := ma.analyzeBlock(stmt.Body)
	ma.reportUnmatchedLocksInBranch(thenStats, "if")

	if stmt.Else != nil {
		elseStats := ma.analyzeElseBranch(stmt.Else)
		ma.reportUnmatchedLocksInBranch(elseStats, "else")
	}
}

// analyzeElseBranch handles else branches (both else and else if)
func (ma *Analyzer) analyzeElseBranch(elseNode ast.Stmt) map[string]*Stats {
	switch e := elseNode.(type) {
	case *ast.BlockStmt:
		return ma.analyzeBlock(e)
	case *ast.IfStmt:
		// For else if, create a synthetic block
		syntheticBlock := &ast.BlockStmt{List: []ast.Stmt{e}}
		return ma.analyzeBlock(syntheticBlock)
	default:
		return make(map[string]*Stats)
	}
}

// analyzeGoStatement handles goroutine statements
func (ma *Analyzer) analyzeGoStatement(stmt *ast.GoStmt) {
	if ma.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	if fnLit, ok := stmt.Call.Fun.(*ast.FuncLit); ok {
		goStats := ma.analyzeBlock(fnLit.Body)
		ma.reportUnmatchedLocksInBranch(goStats, "goroutine")
	}
}

// analyzeForStatement handles for loop statements
func (ma *Analyzer) analyzeForStatement(stmt *ast.ForStmt, stats map[string]*Stats) {
	if ma.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	forStats := ma.analyzeBlock(stmt.Body)
	ma.mergeStats(stats, forStats)
}

// reportUnmatchedLocksInBranch reports unmatched locks in conditional branches
func (ma *Analyzer) reportUnmatchedLocksInBranch(stats map[string]*Stats, branchType string) {
	for mutexName := range ma.mutexNames {
		ma.reportUnmatchedMutexLocksWithContext(mutexName, stats[mutexName], false, branchType)
	}

	for rwMutexName := range ma.rwMutexNames {
		ma.reportUnmatchedMutexLocksWithContext(rwMutexName, stats[rwMutexName], true, branchType)
	}
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

	for _, pos := range stats.lockPos {
		ma.errorCollector.AddError(pos, lockMessage)
	}

	if isRWMutex {
		for _, pos := range stats.rlockPos {
			ma.errorCollector.AddError(pos, rlockMessage)
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
