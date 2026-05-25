package mutex

import (
	"slices"
	"go/ast"
	"go/token"
	"maps"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/category"
)

// handleDeferUnlock processes defer unlock calls
func (ma *Analyzer) handleDeferUnlock(varName string, pos token.Pos, stats map[string]*Stats, isRWMutex bool) {
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
func (ma *Analyzer) handleDeferRUnlock(varName string, pos token.Pos, stats map[string]*Stats) {
	if stats[varName].rlock == 0 {
		ma.errorCollector.AddError(pos, category.DeferUnlockWithoutLock, "rwmutex '"+varName+"' has defer runlock but no corresponding rlock")
		ma.deferErrors.badDeferRUnlock[varName] = true
	} else {
		stats[varName].deferRUnlock++
	}
}

// analyzeRangeStatement handles range statements
func (ma *Analyzer) analyzeRangeStatement(stmt *ast.RangeStmt, stats map[string]*Stats) {
	ma.checkMutexDeclaredInLoop(stmt.Body)
	ma.reportDeferredUnlocksInLoop(stmt.Body)
	rangeStats := ma.analyzeBlock(stmt.Body, stats)
	ma.copyStatsMap(stats, rangeStats)
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
	return ma.analyzeStatementList(stmts, initial)
}

// analyzeIfStatement handles if statements with proper branch analysis
func (ma *Analyzer) analyzeIfStatement(stmt *ast.IfStmt, stats map[string]*Stats) {
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
	thenTerminates := ma.blockAlwaysTerminates(stmt.Body)

	if stmt.Else != nil {
		elseStats := ma.analyzeElseBranch(stmt.Else, elseBase)
		elseTerminates := ma.elseAlwaysTerminates(stmt.Else)

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

// elseAlwaysTerminates reports whether every path through `elseNode`
// terminates.
func (ma *Analyzer) elseAlwaysTerminates(elseNode ast.Stmt) bool {
	switch e := elseNode.(type) {
	case *ast.BlockStmt:
		return ma.blockAlwaysTerminates(e)
	case *ast.IfStmt:
		return ma.blockAlwaysTerminates(&ast.BlockStmt{List: []ast.Stmt{e}})
	default:
		return false
	}
}

func (ma *Analyzer) branchInitialStatsForCondition(cond ast.Expr, stats map[string]*Stats) (map[string]*Stats, map[string]*Stats) {
	thenStats := ma.cloneStatsMap(stats)
	elseStats := ma.cloneStatsMap(stats)

	if unary, ok := cond.(*ast.UnaryExpr); ok && unary.Op == token.NOT {
		negatedThen, negatedElse := ma.branchInitialStatsForCondition(unary.X, stats)
		return negatedElse, negatedThen
	}

	if ma.applyTryLockResultToBranch(cond, thenStats) {
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
func (ma *Analyzer) analyzeGoStatement(stmt *ast.GoStmt, stats map[string]*Stats) {
	if ma.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	if fnLit, ok := stmt.Call.Fun.(*ast.FuncLit); ok {
		// Record goroutines launched while a mutex is held that also try to
		// acquire the same mutex. The conflict is reported at function exit if
		// the parent still holds the lock.
		for varName := range ma.mutexNames {
			if stats[varName] != nil && stats[varName].lock > 0 {
				if method, ok := ma.goroutineBodyLockCallMethod(fnLit.Body, varName, []string{"Lock"}); ok {
					if ma.parentBlocksBeforeUnlock(stmt.Pos(), varName, []string{"Unlock"}) {
						ma.errorCollector.AddError(stmt.Pos(), category.GoroutineLockDeadlock,
							ma.goroutineLockDeadlockMessage(varName, false, false, method, true))
						continue
					}
					ma.goroutineLockConflicts = append(ma.goroutineLockConflicts, goroutineLockConflict{varName: varName, pos: stmt.Pos(), requestMethod: method})
				}
			}
		}
		for varName := range ma.rwMutexNames {
			if stats[varName] != nil && stats[varName].lock > 0 {
				if method, ok := ma.goroutineBodyLockCallMethod(fnLit.Body, varName, []string{"Lock", "RLock"}); ok {
					if ma.parentBlocksBeforeUnlock(stmt.Pos(), varName, []string{"Unlock"}) {
						ma.errorCollector.AddError(stmt.Pos(), category.GoroutineLockDeadlock,
							ma.goroutineLockDeadlockMessage(varName, true, false, method, true))
						continue
					}
					ma.goroutineLockConflicts = append(ma.goroutineLockConflicts, goroutineLockConflict{varName: varName, pos: stmt.Pos(), isRWMutex: true, requestMethod: method})
				}
			}
			if stats[varName] != nil && stats[varName].rlock > 0 {
				if method, ok := ma.goroutineBodyLockCallMethod(fnLit.Body, varName, []string{"Lock"}); ok {
					if ma.parentBlocksBeforeUnlock(stmt.Pos(), varName, []string{"RUnlock"}) {
						ma.errorCollector.AddError(stmt.Pos(), category.GoroutineLockDeadlock,
							ma.goroutineLockDeadlockMessage(varName, true, true, method, true))
						continue
					}
					ma.goroutineLockConflicts = append(ma.goroutineLockConflicts, goroutineLockConflict{varName: varName, pos: stmt.Pos(), isRWMutex: true, parentReadLock: true, requestMethod: method})
				}
			}
		}

		crossReleases := ma.reportCrossGoroutineReleases(fnLit.Body, stats)
		goInitial := ma.emptyStatsLike(stats)
		goStats := ma.analyzeBlock(fnLit.Body, goInitial)
		ma.suppressCrossGoroutineBorrowedReleases(goStats, crossReleases)
		ma.reportUnmatchedLocksInBranch(goInitial, goStats, "goroutine")
		ma.applyCrossGoroutineReleases(stats, crossReleases)
		return
	}

	// `go someMethod()` runs the method asynchronously in another goroutine;
	// its Lock/Unlock effects belong to that goroutine, not the caller's state.
}

// analyzeForStatement handles for loop statements
func (ma *Analyzer) analyzeForStatement(stmt *ast.ForStmt, stats map[string]*Stats) {
	if ma.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	ma.checkMutexDeclaredInLoop(stmt.Body)
	ma.reportDeferredUnlocksInLoop(stmt.Body)
	forStats := ma.analyzeBlock(stmt.Body, stats)
	ma.applyLoopExitLocks(stmt, stats, forStats)
	if stmt.Cond == nil && ma.blockContainsReturn(stmt.Body) && !ma.blockContainsBreak(stmt.Body) {
		ma.clearStats(stats)
		return
	}
	ma.copyStatsMap(stats, forStats)
}

func (ma *Analyzer) blockContainsReturn(block *ast.BlockStmt) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if _, ok := n.(*ast.ReturnStmt); ok {
			found = true
			return false
		}
		return true
	})
	return found
}

func (ma *Analyzer) blockContainsBreak(block *ast.BlockStmt) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		branch, ok := n.(*ast.BranchStmt)
		if ok && branch.Tok == token.BREAK {
			found = true
			return false
		}
		return true
	})
	return found
}

func (ma *Analyzer) blockAlwaysTerminates(block *ast.BlockStmt) bool {
	if block == nil {
		return false
	}

	return slices.ContainsFunc(block.List, ma.statementAlwaysTerminates)
}

func (ma *Analyzer) statementAlwaysTerminates(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		return branchTerminatesBlock(s.Tok)
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		return ok && ma.callTerminatesExecution(call)
	case *ast.IfStmt:
		if s.Else == nil || !ma.blockAlwaysTerminates(s.Body) {
			return false
		}
		return ma.elseAlwaysTerminates(s.Else)
	default:
		return false
	}
}

func branchTerminatesBlock(tok token.Token) bool {
	return tok == token.BREAK ||
		tok == token.CONTINUE ||
		tok == token.GOTO ||
		tok == token.FALLTHROUGH
}

func (ma *Analyzer) reportDeferredUnlocksInLoop(body *ast.BlockStmt) {
	if body == nil {
		return
	}

	locked := make(map[string]bool)
	rlocked := make(map[string]bool)
	ma.reportDeferredUnlocksInLoopStatements(body.List, locked, rlocked)
}

func (ma *Analyzer) reportDeferredUnlocksInLoopStatements(stmts []ast.Stmt, locked, rlocked map[string]bool) {
	for i, stmt := range stmts {
		if ma.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || ma.commentFilter.ShouldSkipCall(call) {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			varName := common.GetVarName(sel.X)
			switch sel.Sel.Name {
			case "Lock":
				if ma.mutexNames[varName] || ma.rwMutexNames[varName] {
					locked[varName] = true
				}
			case "RLock":
				if ma.rwMutexNames[varName] {
					rlocked[varName] = true
				}
			case "Unlock":
				delete(locked, varName)
			case "RUnlock":
				delete(rlocked, varName)
			}

		case *ast.DeferStmt:
			if ma.commentFilter.ShouldSkipCall(s.Call) {
				continue
			}
			if ma.blockAlwaysTerminates(&ast.BlockStmt{List: stmts[i+1:]}) {
				continue
			}
			sel, ok := s.Call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			varName := common.GetVarName(sel.X)
			switch sel.Sel.Name {
			case "Unlock":
				if locked[varName] {
					mutexType := "mutex"
					if ma.rwMutexNames[varName] {
						mutexType = "rwmutex"
					}
					ma.errorCollector.AddError(s.Pos(), category.DeferUnlockInLoop, mutexType+" '"+varName+"' defers unlock inside loop")
				}
			case "RUnlock":
				if rlocked[varName] {
					ma.errorCollector.AddError(s.Pos(), category.DeferUnlockInLoop, "rwmutex '"+varName+"' defers runlock inside loop")
				}
			}

		case *ast.BlockStmt:
			ma.reportDeferredUnlocksInLoopStatements(s.List, maps.Clone(locked), maps.Clone(rlocked))
		case *ast.LabeledStmt:
			ma.reportDeferredUnlocksInLoopStatements([]ast.Stmt{s.Stmt}, maps.Clone(locked), maps.Clone(rlocked))
		case *ast.IfStmt:
			ma.reportDeferredUnlocksInLoopStatements(s.Body.List, maps.Clone(locked), maps.Clone(rlocked))
			if s.Else != nil {
				ma.reportDeferredUnlocksInLoopElse(s.Else, maps.Clone(locked), maps.Clone(rlocked))
			}
		case *ast.ForStmt:
			ma.reportDeferredUnlocksInLoopStatements(s.Body.List, maps.Clone(locked), maps.Clone(rlocked))
		case *ast.RangeStmt:
			ma.reportDeferredUnlocksInLoopStatements(s.Body.List, maps.Clone(locked), maps.Clone(rlocked))
		case *ast.SwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					ma.reportDeferredUnlocksInLoopStatements(cc.Body, maps.Clone(locked), maps.Clone(rlocked))
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					ma.reportDeferredUnlocksInLoopStatements(cc.Body, maps.Clone(locked), maps.Clone(rlocked))
				}
			}
		case *ast.SelectStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CommClause); ok {
					ma.reportDeferredUnlocksInLoopStatements(cc.Body, maps.Clone(locked), maps.Clone(rlocked))
				}
			}
		}
	}
}

func (ma *Analyzer) reportDeferredUnlocksInLoopElse(stmt ast.Stmt, locked, rlocked map[string]bool) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		ma.reportDeferredUnlocksInLoopStatements(s.List, locked, rlocked)
	case *ast.IfStmt:
		ma.reportDeferredUnlocksInLoopStatements([]ast.Stmt{s}, locked, rlocked)
	}
}

func (ma *Analyzer) applyLoopExitLocks(stmt *ast.ForStmt, initial, final map[string]*Stats) {
	for mutexName := range ma.mutexNames {
		if pos, ok := ma.loopMayBreakHoldingMutex(stmt.Body.List, mutexName, []string{"Lock", "TryLock"}, []string{"Unlock"}, 0, token.NoPos); ok {
			if final[mutexName].lock <= initial[mutexName].lock {
				final[mutexName].lock++
				final[mutexName].lockPos = append(final[mutexName].lockPos, pos)
			}
		}
	}

	for rwMutexName := range ma.rwMutexNames {
		if pos, ok := ma.loopMayBreakHoldingMutex(stmt.Body.List, rwMutexName, []string{"Lock", "TryLock"}, []string{"Unlock"}, 0, token.NoPos); ok {
			if final[rwMutexName].lock <= initial[rwMutexName].lock {
				final[rwMutexName].lock++
				final[rwMutexName].lockPos = append(final[rwMutexName].lockPos, pos)
			}
		}
		if pos, ok := ma.loopMayBreakHoldingMutex(stmt.Body.List, rwMutexName, []string{"RLock", "TryRLock"}, []string{"RUnlock"}, 0, token.NoPos); ok {
			if final[rwMutexName].rlock <= initial[rwMutexName].rlock {
				final[rwMutexName].rlock++
				final[rwMutexName].rlockPos = append(final[rwMutexName].rlockPos, pos)
			}
		}
	}
}

func (ma *Analyzer) loopMayBreakHoldingMutex(stmts []ast.Stmt, varName string, lockMethods, unlockMethods []string, depth int, lastLockPos token.Pos) (token.Pos, bool) {
	currentDepth := depth
	currentLockPos := lastLockPos

	for _, stmt := range stmts {
		if ma.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || ma.commentFilter.ShouldSkipCall(call) {
				continue
			}

			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || common.GetVarName(sel.X) != varName {
				continue
			}

			if containsMethod(lockMethods, sel.Sel.Name) {
				currentDepth++
				currentLockPos = call.Pos()
				continue
			}

			if containsMethod(unlockMethods, sel.Sel.Name) && currentDepth > 0 {
				currentDepth--
			}
		case *ast.IfStmt:
			if pos, ok := ma.loopMayBreakHoldingMutex(s.Body.List, varName, lockMethods, unlockMethods, currentDepth, currentLockPos); ok {
				return pos, true
			}
			if s.Else != nil {
				switch elseNode := s.Else.(type) {
				case *ast.BlockStmt:
					if pos, ok := ma.loopMayBreakHoldingMutex(elseNode.List, varName, lockMethods, unlockMethods, currentDepth, currentLockPos); ok {
						return pos, true
					}
				case *ast.IfStmt:
					if pos, ok := ma.loopMayBreakHoldingMutex([]ast.Stmt{elseNode}, varName, lockMethods, unlockMethods, currentDepth, currentLockPos); ok {
						return pos, true
					}
				}
			}
		case *ast.BlockStmt:
			if pos, ok := ma.loopMayBreakHoldingMutex(s.List, varName, lockMethods, unlockMethods, currentDepth, currentLockPos); ok {
				return pos, true
			}
		case *ast.LabeledStmt:
			if pos, ok := ma.loopMayBreakHoldingMutex([]ast.Stmt{s.Stmt}, varName, lockMethods, unlockMethods, currentDepth, currentLockPos); ok {
				return pos, true
			}
		case *ast.BranchStmt:
			if s.Tok == token.BREAK && currentDepth > 0 {
				return currentLockPos, true
			}
		}
	}

	return token.NoPos, false
}

func (ma *Analyzer) isCarriedLoopUnlock(varName string, unlockPos token.Pos, lockMethods, unlockMethods []string) bool {
	if ma.function == nil || ma.function.Body == nil || unlockPos == token.NoPos {
		return false
	}

	found := false
	ast.Inspect(ma.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}

		switch loop := n.(type) {
		case *ast.ForStmt:
			if loop.End() >= unlockPos {
				return true
			}
			if ma.loopMayCarryMutexPastIteration(loop.Body.List, varName, lockMethods, unlockMethods, 0) {
				found = true
				return false
			}
		case *ast.RangeStmt:
			if loop.End() >= unlockPos {
				return true
			}
			if ma.loopMayCarryMutexPastIteration(loop.Body.List, varName, lockMethods, unlockMethods, 0) {
				found = true
				return false
			}
		}

		return true
	})

	return found
}

func (ma *Analyzer) loopMayCarryMutexPastIteration(stmts []ast.Stmt, varName string, lockMethods, unlockMethods []string, depth int) bool {
	currentDepth := depth

	for _, stmt := range stmts {
		if ma.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || ma.commentFilter.ShouldSkipCall(call) {
				continue
			}

			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || common.GetVarName(sel.X) != varName {
				continue
			}

			if containsMethod(lockMethods, sel.Sel.Name) {
				currentDepth++
				continue
			}
			if containsMethod(unlockMethods, sel.Sel.Name) && currentDepth > 0 {
				currentDepth--
			}
		case *ast.IfStmt:
			if ma.loopMayCarryMutexPastIteration(s.Body.List, varName, lockMethods, unlockMethods, currentDepth) {
				return true
			}
			if s.Else != nil {
				switch elseNode := s.Else.(type) {
				case *ast.BlockStmt:
					if ma.loopMayCarryMutexPastIteration(elseNode.List, varName, lockMethods, unlockMethods, currentDepth) {
						return true
					}
				case *ast.IfStmt:
					if ma.loopMayCarryMutexPastIteration([]ast.Stmt{elseNode}, varName, lockMethods, unlockMethods, currentDepth) {
						return true
					}
				}
			}
		case *ast.BlockStmt:
			if ma.loopMayCarryMutexPastIteration(s.List, varName, lockMethods, unlockMethods, currentDepth) {
				return true
			}
		case *ast.LabeledStmt:
			if ma.loopMayCarryMutexPastIteration([]ast.Stmt{s.Stmt}, varName, lockMethods, unlockMethods, currentDepth) {
				return true
			}
		case *ast.ForStmt:
			if ma.loopMayCarryMutexPastIteration(s.Body.List, varName, lockMethods, unlockMethods, currentDepth) {
				return true
			}
		case *ast.RangeStmt:
			if ma.loopMayCarryMutexPastIteration(s.Body.List, varName, lockMethods, unlockMethods, currentDepth) {
				return true
			}
		case *ast.SwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok &&
					ma.loopMayCarryMutexPastIteration(cc.Body, varName, lockMethods, unlockMethods, currentDepth) {
					return true
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok &&
					ma.loopMayCarryMutexPastIteration(cc.Body, varName, lockMethods, unlockMethods, currentDepth) {
					return true
				}
			}
		case *ast.SelectStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CommClause); ok &&
					ma.loopMayCarryMutexPastIteration(cc.Body, varName, lockMethods, unlockMethods, currentDepth) {
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

// reportUnmatchedLocksInBranch reports unmatched locks in conditional branches
func (ma *Analyzer) reportUnmatchedLocksInBranch(initial, final map[string]*Stats, branchType string) {
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
		if delta := ma.remainingLockCount(final.rlock, final.deferRUnlock) - ma.remainingLockCount(initial.rlock, initial.deferRUnlock); delta > 0 {
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
func (ma *Analyzer) unlockDiagnosticSuppressed(mutexName string, acquireMethods []string) bool {
	return ma.functionIsLifecycleReleaseFor(mutexName, acquireMethods) ||
		ma.functionIsCallerManagedReleaseFor(mutexName, acquireMethods) ||
		ma.functionIsParameterUnlockHelper(mutexName, acquireMethods)
}

// terminatingTailUnlockSuppressed reports caller-owned unlocks before a
// terminating tail.
func (ma *Analyzer) terminatingTailUnlockSuppressed(mutexName string) bool {
	return ma.terminatingTailDepth > 0 && ma.varRootIsFunctionParameter(mutexName)
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

	suppressFunctionLevelLock := branchType == "" && ma.functionReturnsLifecycleHandleFor(mutexName, []string{"Unlock"})
	for _, pos := range ma.trailingPositions(stats.lockPos, ma.remainingLockCount(stats.lock, stats.deferUnlock)) {
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
		suppressFunctionLevelRLock := branchType == "" && ma.functionReturnsLifecycleHandleFor(mutexName, []string{"RUnlock"})
		for _, pos := range ma.trailingPositions(stats.rlockPos, ma.remainingLockCount(stats.rlock, stats.deferRUnlock)) {
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
func (ma *Analyzer) reportUnmatchedMutexLocks(mutexName string, stats *Stats, isRWMutex bool) {
	// Call the context-aware version with empty context for function-level reporting
	ma.reportUnmatchedMutexLocksWithContext(mutexName, stats, isRWMutex, "")
}

// reportUnmatchedLocks reports any remaining unmatched locks at function level
func (ma *Analyzer) reportUnmatchedLocks(stats map[string]*Stats) {
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
	for _, conflict := range ma.goroutineLockConflicts {
		st := stats[conflict.varName]
		if st == nil {
			continue
		}
		if conflict.parentReadLock {
			if ma.remainingLockCount(st.rlock, st.deferRUnlock) > 0 {
				ma.errorCollector.AddError(conflict.pos, category.GoroutineLockDeadlock,
					ma.goroutineLockDeadlockMessage(conflict.varName, true, true, conflict.requestMethod, false))
			}
			continue
		}

		if ma.remainingLockCount(st.lock, st.deferUnlock) > 0 {
			ma.errorCollector.AddError(conflict.pos, category.GoroutineLockDeadlock,
				ma.goroutineLockDeadlockMessage(conflict.varName, conflict.isRWMutex, false, conflict.requestMethod, false))
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
func (ma *Analyzer) checkMutexDeclaredInLoop(loopBody *ast.BlockStmt) {
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

func (ma *Analyzer) reportMutexInLoopValueSpec(vs *ast.ValueSpec) {
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

func (ma *Analyzer) reportMutexInLoopAssign(s *ast.AssignStmt) {
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
