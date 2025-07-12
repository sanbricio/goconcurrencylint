package checker

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/concurrency-linter/checker/common"
	commnetfilter "github.com/sanbricio/concurrency-linter/checker/common/comment-filter"
	"github.com/sanbricio/concurrency-linter/checker/common/report"
)

// MutexAnalyzer handles the analysis of mutex and rwmutex usage
type MutexAnalyzer struct {
	mutexNames     map[string]bool
	rwMutexNames   map[string]bool
	errorCollector *report.ErrorCollector
	stats          map[string]*mutexStats
	deferErrors    *deferErrorCollector
	commentFilter  *commnetfilter.CommentFilter
}

// mutexStats tracks the state of a mutex within a block
type mutexStats struct {
	lock, rlock       int
	lockPos, rlockPos []token.Pos
}

// deferErrorCollector tracks defer-related errors to avoid duplicate reporting
type deferErrorCollector struct {
	badDeferUnlock  map[string]bool
	badDeferRUnlock map[string]bool
}

// NewMutexAnalyzer creates a new mutex analyzer
func NewMutexAnalyzer(mutexNames, rwMutexNames map[string]bool, errorCollector *report.ErrorCollector, cf *commnetfilter.CommentFilter) *MutexAnalyzer {
	return &MutexAnalyzer{
		mutexNames:     mutexNames,
		rwMutexNames:   rwMutexNames,
		errorCollector: errorCollector,
		commentFilter:  cf,
		deferErrors: &deferErrorCollector{
			badDeferUnlock:  make(map[string]bool),
			badDeferRUnlock: make(map[string]bool),
		},
	}
}

// AnalyzeFunction analyzes mutex usage in a function
func (ma *MutexAnalyzer) AnalyzeFunction(fn *ast.FuncDecl) {
	ma.initializeStats()
	finalStats := ma.analyzeBlock(fn.Body)
	ma.reportUnmatchedLocks(finalStats)
}

// initializeStats initializes the stats map for all known mutexes
func (ma *MutexAnalyzer) initializeStats() {
	ma.stats = make(map[string]*mutexStats)

	for mutexName := range ma.mutexNames {
		ma.stats[mutexName] = &mutexStats{}
	}

	for rwMutexName := range ma.rwMutexNames {
		ma.stats[rwMutexName] = &mutexStats{}
	}
}

// analyzeBlock analyzes a block statement and returns the final stats
func (ma *MutexAnalyzer) analyzeBlock(block *ast.BlockStmt) map[string]*mutexStats {
	blockStats := ma.copyStats(ma.stats)

	for _, stmt := range block.List {
		// Skip statements that are entirely within comments
		if ma.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		ma.analyzeStatement(stmt, blockStats)
	}

	return blockStats
}

// analyzeStatement analyzes individual statements
func (ma *MutexAnalyzer) analyzeStatement(stmt ast.Stmt, stats map[string]*mutexStats) {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		ma.analyzeExpressionStatement(s, stats)
	case *ast.DeferStmt:
		ma.analyzeDeferStatement(s, stats)
	case *ast.IfStmt:
		ma.analyzeIfStatement(s)
	case *ast.GoStmt:
		ma.analyzeGoStatement(s)
	case *ast.ForStmt:
		ma.analyzeForStatement(s, stats)
	case *ast.RangeStmt:
		ma.analyzeRangeStatement(s, stats)
	case *ast.SwitchStmt:
		ma.analyzeSwitchStatement(s)
	case *ast.TypeSwitchStmt:
		ma.analyzeTypeSwitchStatement(s)
	case *ast.SelectStmt:
		ma.analyzeSelectStatement(s)
	case *ast.BlockStmt:
		nestedStats := ma.analyzeBlock(s)
		ma.mergeStats(stats, nestedStats)
	}
}

// analyzeExpressionStatement handles expression statements (Lock/Unlock calls)
func (ma *MutexAnalyzer) analyzeExpressionStatement(stmt *ast.ExprStmt, stats map[string]*mutexStats) {
	call, ok := stmt.X.(*ast.CallExpr)
	if !ok {
		return
	}

	// Skip if the entire call is in a comment
	if ma.commentFilter.ShouldSkipCall(call) {
		return
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	varName := common.GetVarName(sel.X)

	if ma.mutexNames[varName] {
		ma.handleMutexCall(varName, sel.Sel.Name, call.Pos(), stats)
	}

	if ma.rwMutexNames[varName] {
		ma.handleRWMutexCall(varName, sel.Sel.Name, call.Pos(), stats)
	}
}

// handleMutexCall processes mutex method calls
func (ma *MutexAnalyzer) handleMutexCall(varName, methodName string, pos token.Pos, stats map[string]*mutexStats) {
	switch methodName {
	case "Lock":
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "Unlock":
		if stats[varName].lock == 0 {
			ma.errorCollector.AddError(pos, "mutex '"+varName+"' is unlocked but not locked")
		} else {
			stats[varName].lock--
			ma.removeFirstLockPos(stats[varName])
		}
	}
}

// handleRWMutexCall processes rwmutex method calls
func (ma *MutexAnalyzer) handleRWMutexCall(varName, methodName string, pos token.Pos, stats map[string]*mutexStats) {
	switch methodName {
	case "Lock":
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "Unlock":
		if stats[varName].lock == 0 {
			ma.errorCollector.AddError(pos, "rwmutex '"+varName+"' is unlocked but not locked")
		} else {
			stats[varName].lock--
			ma.removeFirstLockPos(stats[varName])
		}
	case "RLock":
		stats[varName].rlock++
		stats[varName].rlockPos = append(stats[varName].rlockPos, pos)
	case "RUnlock":
		if stats[varName].rlock == 0 {
			ma.errorCollector.AddError(pos, "rwmutex '"+varName+"' is runlocked but not rlocked")
		} else {
			stats[varName].rlock--
			ma.removeFirstRLockPos(stats[varName])
		}
	}
}

// analyzeDeferStatement handles defer statements
func (ma *MutexAnalyzer) analyzeDeferStatement(stmt *ast.DeferStmt, stats map[string]*mutexStats) {
	// Skip if the entire defer statement is in a comment
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
func (ma *MutexAnalyzer) handleDeferCall(call *ast.SelectorExpr, pos token.Pos, stats map[string]*mutexStats) {
	varName := common.GetVarName(call.X)

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

// handleDeferUnlock processes defer unlock calls
func (ma *MutexAnalyzer) handleDeferUnlock(varName string, pos token.Pos, stats map[string]*mutexStats, isRWMutex bool) {
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
func (ma *MutexAnalyzer) handleDeferRUnlock(varName string, pos token.Pos, stats map[string]*mutexStats) {
	if stats[varName].rlock == 0 {
		ma.errorCollector.AddError(pos, "rwmutex '"+varName+"' has defer runlock but no corresponding rlock")
		ma.deferErrors.badDeferRUnlock[varName] = true
	} else {
		stats[varName].rlock--
		ma.removeFirstRLockPos(stats[varName])
	}
}

// handleDeferFunctionLiteral processes defer with function literals
func (ma *MutexAnalyzer) handleDeferFunctionLiteral(fnlit *ast.FuncLit, pos token.Pos, stats map[string]*mutexStats) {
	// Check for mutex unlocks in function literal
	for mutexName := range ma.mutexNames {
		if ma.containsUnlock(fnlit.Body, mutexName) {
			ma.handleDeferUnlock(mutexName, pos, stats, false)
		}
	}

	// Check for rwmutex unlocks in function literal
	for rwMutexName := range ma.rwMutexNames {
		if ma.containsUnlock(fnlit.Body, rwMutexName) {
			ma.handleDeferUnlock(rwMutexName, pos, stats, true)
		}
		if ma.containsRUnlock(fnlit.Body, rwMutexName) {
			ma.handleDeferRUnlock(rwMutexName, pos, stats)
		}
	}
}

// containsUnlock checks if a block contains an unlock call for a specific mutex
func (ma *MutexAnalyzer) containsUnlock(block *ast.BlockStmt, mutexName string) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			// Skip if the call is in a comment
			if ma.commentFilter.ShouldSkipCall(call) {
				return true
			}

			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "Unlock" && common.GetVarName(sel.X) == mutexName {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// containsRUnlock checks if a block contains an runlock call for a specific rwmutex
func (ma *MutexAnalyzer) containsRUnlock(block *ast.BlockStmt, mutexName string) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			// Skip if the call is in a comment
			if ma.commentFilter.ShouldSkipCall(call) {
				return true
			}

			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "RUnlock" && common.GetVarName(sel.X) == mutexName {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// analyzeRangeStatement handles range statements
func (ma *MutexAnalyzer) analyzeRangeStatement(stmt *ast.RangeStmt, stats map[string]*mutexStats) {
	rangeStats := ma.analyzeBlock(stmt.Body)
	ma.mergeStats(stats, rangeStats)
}

// analyzeSwitchStatement handles switch statements
func (ma *MutexAnalyzer) analyzeSwitchStatement(stmt *ast.SwitchStmt) {
	for _, caseStmt := range stmt.Body.List {
		if cc, ok := caseStmt.(*ast.CaseClause); ok {
			caseStats := ma.analyzeStatements(cc.Body)
			ma.reportUnmatchedLocksInBranch(caseStats, "case")
		}
	}
}

// analyzeTypeSwitchStatement handles type switch statements
func (ma *MutexAnalyzer) analyzeTypeSwitchStatement(stmt *ast.TypeSwitchStmt) {
	for _, caseStmt := range stmt.Body.List {
		if cc, ok := caseStmt.(*ast.CaseClause); ok {
			caseStats := ma.analyzeStatements(cc.Body)
			ma.reportUnmatchedLocksInBranch(caseStats, "case")
		}
	}
}

// analyzeSelectStatement handles select statements
func (ma *MutexAnalyzer) analyzeSelectStatement(stmt *ast.SelectStmt) {
	for _, commClause := range stmt.Body.List {
		if cc, ok := commClause.(*ast.CommClause); ok {
			commStats := ma.analyzeStatements(cc.Body)
			ma.reportUnmatchedLocksInBranch(commStats, "select")
		}
	}
}

// analyzeStatements is a helper to analyze a list of statements
func (ma *MutexAnalyzer) analyzeStatements(stmts []ast.Stmt) map[string]*mutexStats {
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
func (ma *MutexAnalyzer) analyzeIfStatement(stmt *ast.IfStmt) {
	// Skip if the entire if statement is in a comment
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
func (ma *MutexAnalyzer) analyzeElseBranch(elseNode ast.Stmt) map[string]*mutexStats {
	switch e := elseNode.(type) {
	case *ast.BlockStmt:
		return ma.analyzeBlock(e)
	case *ast.IfStmt:
		// For else if, create a synthetic block
		syntheticBlock := &ast.BlockStmt{List: []ast.Stmt{e}}
		return ma.analyzeBlock(syntheticBlock)
	default:
		return make(map[string]*mutexStats)
	}
}

// analyzeGoStatement handles goroutine statements
func (ma *MutexAnalyzer) analyzeGoStatement(stmt *ast.GoStmt) {
	// Skip if the entire go statement is in a comment
	if ma.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	if fnLit, ok := stmt.Call.Fun.(*ast.FuncLit); ok {
		goStats := ma.analyzeBlock(fnLit.Body)
		ma.reportUnmatchedLocksInBranch(goStats, "goroutine")
	}
}

// analyzeForStatement handles for loop statements
func (ma *MutexAnalyzer) analyzeForStatement(stmt *ast.ForStmt, stats map[string]*mutexStats) {
	// Skip if the entire for statement is in a comment
	if ma.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	forStats := ma.analyzeBlock(stmt.Body)
	ma.mergeStats(stats, forStats)
}

// reportUnmatchedLocksInBranch reports unmatched locks in conditional branches
func (ma *MutexAnalyzer) reportUnmatchedLocksInBranch(stats map[string]*mutexStats, branchType string) {
	for mutexName := range ma.mutexNames {
		ma.reportUnmatchedMutexLocksWithContext(mutexName, stats[mutexName], false, branchType)
	}

	for rwMutexName := range ma.rwMutexNames {
		ma.reportUnmatchedMutexLocksWithContext(rwMutexName, stats[rwMutexName], true, branchType)
	}
}

// reportUnmatchedMutexLocksWithContext reports unmatched locks for a specific mutex with context
func (ma *MutexAnalyzer) reportUnmatchedMutexLocksWithContext(mutexName string, stats *mutexStats, isRWMutex bool, branchType string) {
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
func (ma *MutexAnalyzer) reportUnmatchedMutexLocks(mutexName string, stats *mutexStats, isRWMutex bool) {
	// Call the context-aware version with empty context for function-level reporting
	ma.reportUnmatchedMutexLocksWithContext(mutexName, stats, isRWMutex, "")
}

// reportUnmatchedLocks reports any remaining unmatched locks at function level
func (ma *MutexAnalyzer) reportUnmatchedLocks(stats map[string]*mutexStats) {
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

// Helper functions

// copyStats creates a deep copy of the stats map
func (ma *MutexAnalyzer) copyStats(original map[string]*mutexStats) map[string]*mutexStats {
	copy := make(map[string]*mutexStats)
	for name, stats := range original {
		copy[name] = &mutexStats{
			lock:     stats.lock,
			rlock:    stats.rlock,
			lockPos:  append([]token.Pos{}, stats.lockPos...),
			rlockPos: append([]token.Pos{}, stats.rlockPos...),
		}
	}
	return copy
}

// mergeStats merges stats from a nested block into parent stats
func (ma *MutexAnalyzer) mergeStats(parent, child map[string]*mutexStats) {
	for name, childStats := range child {
		if parentStats, exists := parent[name]; exists {
			parentStats.lock += childStats.lock
			parentStats.rlock += childStats.rlock
			parentStats.lockPos = append(parentStats.lockPos, childStats.lockPos...)
			parentStats.rlockPos = append(parentStats.rlockPos, childStats.rlockPos...)
		}
	}
}

// removeFirstLockPos removes the first lock position from the list
func (ma *MutexAnalyzer) removeFirstLockPos(stats *mutexStats) {
	if len(stats.lockPos) > 0 {
		stats.lockPos = stats.lockPos[1:]
	}
}

// removeFirstRLockPos removes the first rlock position from the list
func (ma *MutexAnalyzer) removeFirstRLockPos(stats *mutexStats) {
	if len(stats.rlockPos) > 0 {
		stats.rlockPos = stats.rlockPos[1:]
	}
}
