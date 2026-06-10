package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

// analyzeRangeStatement handles range statements
func (c *Checker) analyzeRangeStatement(stmt *ast.RangeStmt, stats map[string]*Stats) {
	newLoopMutexDetector(c.errorCollector, c.typesInfo).check(stmt.Body)
	c.loopCarry.reportDeferredUnlocksInLoop(stmt.Body)
	rangeStats := c.analyzeBlock(stmt.Body, stats)
	copyStatsMap(stats, rangeStats)
}

// branchClause is one arm of a switch, type switch or select statement.
type branchClause struct {
	body      []ast.Stmt
	isDefault bool
}

// switchClauses extracts the clauses of a switch or type switch body and
// reports whether any case falls through (which links arm states together and
// disables the convergence merge).
func switchClauses(body *ast.BlockStmt) (clauses []branchClause, hasDefault, hasFallthrough bool) {
	for _, stmt := range body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		if cc.List == nil {
			hasDefault = true
		}
		for _, s := range cc.Body {
			if branch, ok := s.(*ast.BranchStmt); ok && branch.Tok == token.FALLTHROUGH {
				hasFallthrough = true
			}
		}
		clauses = append(clauses, branchClause{body: cc.Body, isDefault: cc.List == nil})
	}
	return clauses, hasDefault, hasFallthrough
}

// selectClauses extracts the clauses of a select body.
func selectClauses(body *ast.BlockStmt) []branchClause {
	var clauses []branchClause
	for _, stmt := range body.List {
		if cc, ok := stmt.(*ast.CommClause); ok {
			clauses = append(clauses, branchClause{body: cc.Body, isDefault: cc.Comm == nil})
		}
	}
	return clauses
}

// analyzeSwitchStatement handles switch statements
func (c *Checker) analyzeSwitchStatement(stmt *ast.SwitchStmt, stats map[string]*Stats) {
	if stmt.Init != nil {
		c.analyzeStatement(stmt.Init, stats)
	}
	clauses, hasDefault, hasFallthrough := switchClauses(stmt.Body)
	c.analyzeBranchClauses(clauses, hasDefault && !hasFallthrough, stats, "case")
}

// analyzeTypeSwitchStatement handles type switch statements
func (c *Checker) analyzeTypeSwitchStatement(stmt *ast.TypeSwitchStmt, stats map[string]*Stats) {
	if stmt.Init != nil {
		c.analyzeStatement(stmt.Init, stats)
	}
	clauses, hasDefault, hasFallthrough := switchClauses(stmt.Body)
	c.analyzeBranchClauses(clauses, hasDefault && !hasFallthrough, stats, "case")
}

// analyzeSelectStatement handles select statements. A select always executes
// exactly one of its clauses, so the clause set is exhaustive even without a
// default arm.
func (c *Checker) analyzeSelectStatement(stmt *ast.SelectStmt, stats map[string]*Stats) {
	c.analyzeBranchClauses(selectClauses(stmt.Body), true, stats, "select")
}

// analyzeBranchClauses analyzes each clause body against the entry state and,
// mirroring analyzeIfStatement, merges the resulting state back into stats
// when the clause set covers every path (exhaustive) and the surviving arms
// agree on the lock state. Otherwise it falls back to reporting the per-arm
// deltas, leaving stats at the entry state.
func (c *Checker) analyzeBranchClauses(clauses []branchClause, exhaustive bool, stats map[string]*Stats, branchType string) {
	if len(clauses) == 0 {
		return
	}

	branchStats := make([]map[string]*Stats, len(clauses))
	terminates := make([]bool, len(clauses))
	for i, clause := range clauses {
		branchStats[i] = c.analyzeStatements(clause.body, stats)
		terminates[i] = c.termination.blockAlwaysTerminates(&ast.BlockStmt{List: clause.body})
	}

	if exhaustive {
		// All arms agree (terminating ones included): adopt the common state.
		if merged := c.convergedBranchState(branchStats, nil); merged != nil {
			copyStatsMap(stats, merged)
			return
		}
		// Arms that terminate never reach the code after the statement, so
		// only the surviving arms need to agree.
		if merged := c.convergedBranchState(branchStats, terminates); merged != nil {
			copyStatsMap(stats, merged)
			return
		}
		allTerminate := true
		for _, t := range terminates {
			if !t {
				allTerminate = false
				break
			}
		}
		if allTerminate {
			return
		}
	}

	for i := range clauses {
		c.reportUnmatchedLocksInBranch(stats, branchStats[i], branchType)
	}
}

// convergedBranchState returns the branch state shared by every considered
// branch, or nil when they disagree. When skip is non-nil, branches whose skip
// entry is true are excluded; nil is returned when no branch is considered.
func (c *Checker) convergedBranchState(branches []map[string]*Stats, skip []bool) map[string]*Stats {
	var converged map[string]*Stats
	for i, bs := range branches {
		if skip != nil && skip[i] {
			continue
		}
		if converged == nil {
			converged = bs
			continue
		}
		if !c.canMergeBranchStates(converged, bs) {
			return nil
		}
	}
	return converged
}

// analyzeStatements is a helper to analyze a list of statements
func (c *Checker) analyzeStatements(stmts []ast.Stmt, initial map[string]*Stats) map[string]*Stats {
	return c.analyzeStatementList(stmts, initial)
}

// analyzeIfStatement handles if statements with proper branch analysis
func (c *Checker) analyzeIfStatement(stmt *ast.IfStmt, stats map[string]*Stats) {
	if c.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	if stmt.Init != nil {
		c.analyzeStatement(stmt.Init, stats)
	}

	if condValue, ok := common.ConstantBoolValue(stmt.Cond, c.typesInfo); ok {
		if condValue {
			thenStats := c.analyzeBlock(stmt.Body, stats)
			copyStatsMap(stats, thenStats)
			return
		}

		if stmt.Else != nil {
			elseStats := c.analyzeElseBranch(stmt.Else, stats)
			copyStatsMap(stats, elseStats)
		}
		return
	}

	thenBase, elseBase := c.branchInitialStatsForCondition(stmt.Cond, stats)
	thenStats := c.analyzeBlock(stmt.Body, thenBase)
	thenTerminates := c.termination.blockAlwaysTerminates(stmt.Body)

	if stmt.Else != nil {
		elseStats := c.analyzeElseBranch(stmt.Else, elseBase)
		elseTerminates := c.termination.elseAlwaysTerminates(stmt.Else)

		if c.canMergeBranchStates(thenStats, elseStats) {
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

		c.reportUnmatchedLocksInBranch(stats, thenStats, "if")
		c.reportUnmatchedLocksInBranch(stats, elseStats, "else")
		return
	}

	if thenTerminates {
		copyStatsMap(stats, elseBase)
		return
	}
	c.reportUnmatchedLocksInBranch(stats, thenStats, "if")
}

func (c *Checker) branchInitialStatsForCondition(cond ast.Expr, stats map[string]*Stats) (map[string]*Stats, map[string]*Stats) {
	thenStats := cloneStatsMap(stats)
	elseStats := cloneStatsMap(stats)

	if unary, ok := cond.(*ast.UnaryExpr); ok && unary.Op == token.NOT {
		negatedThen, negatedElse := c.branchInitialStatsForCondition(unary.X, stats)
		return negatedElse, negatedThen
	}

	if c.tryLock.applyToBranch(cond, thenStats) {
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
		if c.mutexNames[varName] || c.rwMutexNames[varName] {
			thenStats[varName].lock++
			thenStats[varName].lockPos = append(thenStats[varName].lockPos, call.Pos())
		}
	case "TryRLock":
		if c.rwMutexNames[varName] {
			thenStats[varName].rlock++
			thenStats[varName].rlockPos = append(thenStats[varName].rlockPos, call.Pos())
		}
	}

	return thenStats, elseStats
}

// analyzeElseBranch handles else branches (both else and else if)
func (c *Checker) analyzeElseBranch(elseNode ast.Stmt, stats map[string]*Stats) map[string]*Stats {
	switch e := elseNode.(type) {
	case *ast.BlockStmt:
		return c.analyzeBlock(e, stats)
	case *ast.IfStmt:
		// For else if, create a synthetic block
		syntheticBlock := &ast.BlockStmt{List: []ast.Stmt{e}}
		return c.analyzeBlock(syntheticBlock, stats)
	default:
		return make(map[string]*Stats)
	}
}

func (c *Checker) canMergeBranchStates(a, b map[string]*Stats) bool {
	for name := range c.mutexNames {
		if !c.sameBranchState(a[name], b[name]) {
			return false
		}
	}

	for name := range c.rwMutexNames {
		if !c.sameBranchState(a[name], b[name]) {
			return false
		}
	}

	return true
}

func (c *Checker) sameBranchState(a, b *Stats) bool {
	if a == nil || b == nil {
		return a == b
	}

	// Compare the net outstanding lock count rather than the raw lock and
	// deferUnlock fields. Two branches that both leave the lock released by
	// function exit are equivalent even when one releases directly and the
	// other defers it (lock=1,deferUnlock=1 vs lock=0,deferUnlock=0).
	return remainingLockCount(a.lock, a.deferUnlock) == remainingLockCount(b.lock, b.deferUnlock) &&
		remainingLockCount(a.rlock, a.deferRUnlock) == remainingLockCount(b.rlock, b.deferRUnlock) &&
		a.borrowedLock == b.borrowedLock &&
		a.borrowedRLock == b.borrowedRLock
}

// analyzeGoStatement handles goroutine statements
func (c *Checker) analyzeGoStatement(stmt *ast.GoStmt, stats map[string]*Stats) {
	if c.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	if fnLit, ok := stmt.Call.Fun.(*ast.FuncLit); ok {
		cg := newCrossGoroutineDetector(c.mutexNames, c.rwMutexNames, c.commentFilter, c.typesInfo)
		// Record goroutines launched while a mutex is held that also try to
		// acquire the same mutex. The conflict is reported at function exit if
		// the parent still holds the lock.
		for varName := range c.mutexNames {
			if stats[varName] != nil && stats[varName].lock > 0 {
				if method, ok := cg.goroutineBodyLockCallMethod(fnLit.Body, varName, []string{"Lock"}); ok {
					if cg.parentBlocksBeforeUnlock(c.function, stmt.Pos(), varName, []string{"Unlock"}) {
						c.errorCollector.AddError(stmt.Pos(), category.GoroutineLockDeadlock,
							cg.deadlockMessage(varName, false, false, method, true))
						continue
					}
					c.goroutineLockConflicts = append(c.goroutineLockConflicts, goroutineLockConflict{varName: varName, pos: stmt.Pos(), requestMethod: method})
				}
			}
		}
		for varName := range c.rwMutexNames {
			if stats[varName] != nil && stats[varName].lock > 0 {
				if method, ok := cg.goroutineBodyLockCallMethod(fnLit.Body, varName, []string{"Lock", "RLock"}); ok {
					if cg.parentBlocksBeforeUnlock(c.function, stmt.Pos(), varName, []string{"Unlock"}) {
						c.errorCollector.AddError(stmt.Pos(), category.GoroutineLockDeadlock,
							cg.deadlockMessage(varName, true, false, method, true))
						continue
					}
					c.goroutineLockConflicts = append(c.goroutineLockConflicts, goroutineLockConflict{varName: varName, pos: stmt.Pos(), isRWMutex: true, requestMethod: method})
				}
			}
			if stats[varName] != nil && stats[varName].rlock > 0 {
				if method, ok := cg.goroutineBodyLockCallMethod(fnLit.Body, varName, []string{"Lock"}); ok {
					if cg.parentBlocksBeforeUnlock(c.function, stmt.Pos(), varName, []string{"RUnlock"}) {
						c.errorCollector.AddError(stmt.Pos(), category.GoroutineLockDeadlock,
							cg.deadlockMessage(varName, true, true, method, true))
						continue
					}
					c.goroutineLockConflicts = append(c.goroutineLockConflicts, goroutineLockConflict{varName: varName, pos: stmt.Pos(), isRWMutex: true, parentReadLock: true, requestMethod: method})
				}
			}
		}

		crossReleases := cg.collectReleases(fnLit.Body, stats)
		goInitial := emptyStatsLike(stats)
		goStats := c.analyzeBlock(fnLit.Body, goInitial)
		cg.suppressBorrowedReleases(goStats, crossReleases)
		c.reportUnmatchedLocksInBranch(goInitial, goStats, "goroutine")
		cg.applyReleases(stats, crossReleases)
		return
	}

	// `go someMethod()` runs the method asynchronously in another goroutine;
	// its Lock/Unlock effects belong to that goroutine, not the caller's state.
}

// analyzeForStatement handles for loop statements
func (c *Checker) analyzeForStatement(stmt *ast.ForStmt, stats map[string]*Stats) {
	if c.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	newLoopMutexDetector(c.errorCollector, c.typesInfo).check(stmt.Body)
	c.loopCarry.reportDeferredUnlocksInLoop(stmt.Body)
	forStats := c.analyzeBlock(stmt.Body, stats)
	c.loopCarry.applyLoopExitLocks(stmt, stats, forStats)
	if stmt.Cond == nil && c.termination.blockContainsReturn(stmt.Body) && !c.termination.blockContainsBreak(stmt.Body) {
		clearStats(stats)
		return
	}
	copyStatsMap(stats, forStats)
}
