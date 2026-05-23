package mutex

import (
	"go/ast"
	"go/token"
	"maps"
	"sort"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/category"
)

type lockOrderEdge struct {
	from string
	to   string
}

type crossGoroutineReleases struct {
	unlocks  map[string][]token.Pos
	runlocks map[string][]token.Pos
}

func (ma *Analyzer) checkLockOrderCycles(block *ast.BlockStmt) {
	if block == nil {
		return
	}

	edges := make(map[lockOrderEdge]token.Pos)
	reported := make(map[lockOrderEdge]bool)
	ma.scanLockOrderStatements(block.List, make(map[string]bool), edges, reported)
}

func (ma *Analyzer) scanLockOrderStatements(stmts []ast.Stmt, held map[string]bool, edges map[lockOrderEdge]token.Pos, reported map[lockOrderEdge]bool) {
	for _, stmt := range stmts {
		if stmt == nil || ma.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok {
				ma.scanLockOrderCall(call, held, edges, reported)
			}
		case *ast.DeferStmt:
			ma.scanLockOrderDefer(s, held, edges, reported)
		case *ast.GoStmt:
			if fnLit, ok := s.Call.Fun.(*ast.FuncLit); ok && fnLit.Body != nil {
				ma.scanLockOrderStatements(fnLit.Body.List, make(map[string]bool), edges, reported)
			}
		case *ast.IfStmt:
			thenHeld := maps.Clone(held)
			if varName, ok := ma.tryLockCallVar(s.Cond); ok {
				ma.recordHeldLockOrderEdges(varName, s.Cond.Pos(), thenHeld, edges, reported)
				thenHeld[varName] = true
			}
			ma.scanLockOrderStatements(s.Body.List, thenHeld, edges, reported)
			if s.Else != nil {
				ma.scanLockOrderElse(s.Else, maps.Clone(held), edges, reported)
			}
		case *ast.ForStmt:
			if s.Body != nil {
				ma.scanLockOrderStatements(s.Body.List, maps.Clone(held), edges, reported)
			}
		case *ast.RangeStmt:
			if s.Body != nil {
				ma.scanLockOrderStatements(s.Body.List, maps.Clone(held), edges, reported)
			}
		case *ast.BlockStmt:
			ma.scanLockOrderStatements(s.List, held, edges, reported)
		case *ast.LabeledStmt:
			ma.scanLockOrderStatements([]ast.Stmt{s.Stmt}, held, edges, reported)
		case *ast.SwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					ma.scanLockOrderStatements(cc.Body, maps.Clone(held), edges, reported)
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					ma.scanLockOrderStatements(cc.Body, maps.Clone(held), edges, reported)
				}
			}
		case *ast.SelectStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CommClause); ok {
					ma.scanLockOrderStatements(cc.Body, maps.Clone(held), edges, reported)
				}
			}
		}
	}
}

func (ma *Analyzer) scanLockOrderCall(call *ast.CallExpr, held map[string]bool, edges map[lockOrderEdge]token.Pos, reported map[lockOrderEdge]bool) {
	if call == nil || ma.commentFilter.ShouldSkipCall(call) {
		return
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	varName := common.GetVarName(sel.X)
	switch {
	case ma.isLockOrderAcquire(varName, sel.Sel.Name):
		ma.recordHeldLockOrderEdges(varName, call.Pos(), held, edges, reported)
		held[varName] = true
	case ma.isLockOrderRelease(varName, sel.Sel.Name):
		delete(held, varName)
	}
}

func (ma *Analyzer) scanLockOrderDefer(stmt *ast.DeferStmt, held map[string]bool, edges map[lockOrderEdge]token.Pos, reported map[lockOrderEdge]bool) {
	if stmt == nil || ma.commentFilter.ShouldSkipCall(stmt.Call) {
		return
	}
	if call, ok := stmt.Call.Fun.(*ast.FuncLit); ok && call.Body != nil {
		ma.scanLockOrderStatements(call.Body.List, maps.Clone(held), edges, reported)
	}
}

func (ma *Analyzer) scanLockOrderElse(stmt ast.Stmt, held map[string]bool, edges map[lockOrderEdge]token.Pos, reported map[lockOrderEdge]bool) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		ma.scanLockOrderStatements(s.List, held, edges, reported)
	case *ast.IfStmt:
		ma.scanLockOrderStatements([]ast.Stmt{s}, held, edges, reported)
	}
}

func (ma *Analyzer) tryLockCallVar(expr ast.Expr) (string, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	varName := common.GetVarName(sel.X)
	switch sel.Sel.Name {
	case "TryLock":
		if ma.mutexNames[varName] || ma.rwMutexNames[varName] {
			return varName, true
		}
	case "TryRLock":
		if ma.rwMutexNames[varName] {
			return varName, true
		}
	}
	return "", false
}

func (ma *Analyzer) recordHeldLockOrderEdges(varName string, pos token.Pos, held map[string]bool, edges map[lockOrderEdge]token.Pos, reported map[lockOrderEdge]bool) {
	for heldName := range held {
		if heldName == varName {
			continue
		}

		edge := lockOrderEdge{from: heldName, to: varName}
		reverse := lockOrderEdge{from: varName, to: heldName}
		if _, exists := edges[edge]; !exists {
			edges[edge] = pos
		}
		if _, exists := edges[reverse]; !exists {
			continue
		}

		reportKey := normalizedLockOrderEdge(heldName, varName)
		if reported[reportKey] {
			continue
		}
		reported[reportKey] = true
		first, second := orderedLockNames(heldName, varName)
		ma.errorCollector.AddError(pos, category.LockOrderCycle, "mutex lock order cycle between '"+first+"' and '"+second+"'")
	}
}

func normalizedLockOrderEdge(a, b string) lockOrderEdge {
	first, second := orderedLockNames(a, b)
	return lockOrderEdge{from: first, to: second}
}

func orderedLockNames(a, b string) (string, string) {
	names := []string{a, b}
	sort.Strings(names)
	return names[0], names[1]
}

func (ma *Analyzer) isLockOrderAcquire(varName, methodName string) bool {
	switch methodName {
	case "Lock", "TryLock":
		return ma.mutexNames[varName] || ma.rwMutexNames[varName]
	case "RLock", "TryRLock":
		return ma.rwMutexNames[varName]
	default:
		return false
	}
}

func (ma *Analyzer) isLockOrderRelease(varName, methodName string) bool {
	switch methodName {
	case "Unlock":
		return ma.mutexNames[varName] || ma.rwMutexNames[varName]
	case "RUnlock":
		return ma.rwMutexNames[varName]
	default:
		return false
	}
}

func (ma *Analyzer) reportCrossGoroutineReleases(body *ast.BlockStmt, parentStats map[string]*Stats) crossGoroutineReleases {
	releases := crossGoroutineReleases{
		unlocks:  make(map[string][]token.Pos),
		runlocks: make(map[string][]token.Pos),
	}
	if body == nil {
		return releases
	}

	ma.scanCrossGoroutineReleaseStatements(body.List, parentStats, make(map[string]int), make(map[string]int), releases)
	return releases
}

func (ma *Analyzer) scanCrossGoroutineReleaseStatements(stmts []ast.Stmt, parentStats map[string]*Stats, localLocks, localRLocks map[string]int, releases crossGoroutineReleases) {
	for _, stmt := range stmts {
		if stmt == nil || ma.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok {
				ma.scanCrossGoroutineReleaseCall(call, parentStats, localLocks, localRLocks, releases)
			}
		case *ast.DeferStmt:
			ma.scanCrossGoroutineReleaseCall(s.Call, parentStats, localLocks, localRLocks, releases)
		case *ast.IfStmt:
			ma.scanCrossGoroutineReleaseStatements(s.Body.List, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
			if s.Else != nil {
				ma.scanCrossGoroutineReleaseElse(s.Else, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
			}
		case *ast.ForStmt:
			if s.Body != nil {
				ma.scanCrossGoroutineReleaseStatements(s.Body.List, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
			}
		case *ast.RangeStmt:
			if s.Body != nil {
				ma.scanCrossGoroutineReleaseStatements(s.Body.List, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
			}
		case *ast.BlockStmt:
			ma.scanCrossGoroutineReleaseStatements(s.List, parentStats, localLocks, localRLocks, releases)
		case *ast.LabeledStmt:
			ma.scanCrossGoroutineReleaseStatements([]ast.Stmt{s.Stmt}, parentStats, localLocks, localRLocks, releases)
		case *ast.SwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					ma.scanCrossGoroutineReleaseStatements(cc.Body, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					ma.scanCrossGoroutineReleaseStatements(cc.Body, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
				}
			}
		case *ast.SelectStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CommClause); ok {
					ma.scanCrossGoroutineReleaseStatements(cc.Body, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
				}
			}
		}
	}
}

func (ma *Analyzer) scanCrossGoroutineReleaseElse(stmt ast.Stmt, parentStats map[string]*Stats, localLocks, localRLocks map[string]int, releases crossGoroutineReleases) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		ma.scanCrossGoroutineReleaseStatements(s.List, parentStats, localLocks, localRLocks, releases)
	case *ast.IfStmt:
		ma.scanCrossGoroutineReleaseStatements([]ast.Stmt{s}, parentStats, localLocks, localRLocks, releases)
	}
}

func (ma *Analyzer) scanCrossGoroutineReleaseCall(call *ast.CallExpr, parentStats map[string]*Stats, localLocks, localRLocks map[string]int, releases crossGoroutineReleases) {
	if call == nil || ma.commentFilter.ShouldSkipCall(call) {
		return
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	varName := common.GetVarName(sel.X)
	switch sel.Sel.Name {
	case "Lock", "TryLock":
		if ma.mutexNames[varName] || ma.rwMutexNames[varName] {
			localLocks[varName]++
		}
	case "Unlock":
		if ma.mutexNames[varName] || ma.rwMutexNames[varName] {
			if localLocks[varName] > 0 {
				localLocks[varName]--
				return
			}
			if ma.parentHoldsExclusiveLock(parentStats, varName) {
				releases.unlocks[varName] = append(releases.unlocks[varName], call.Pos())
				ma.errorCollector.AddError(call.Pos(), category.CrossGoroutineUnlock, "mutex '"+varName+"' is unlocked in a different goroutine than it was locked")
			}
		}
	case "RLock", "TryRLock":
		if ma.rwMutexNames[varName] {
			localRLocks[varName]++
		}
	case "RUnlock":
		if ma.rwMutexNames[varName] {
			if localRLocks[varName] > 0 {
				localRLocks[varName]--
				return
			}
			if ma.parentHoldsReadLock(parentStats, varName) {
				releases.runlocks[varName] = append(releases.runlocks[varName], call.Pos())
				ma.errorCollector.AddError(call.Pos(), category.CrossGoroutineUnlock, "rwmutex '"+varName+"' is runlocked in a different goroutine than it was rlocked")
			}
		}
	}
}

func (ma *Analyzer) parentHoldsExclusiveLock(stats map[string]*Stats, varName string) bool {
	st := stats[varName]
	if st == nil {
		return false
	}
	return ma.remainingLockCount(st.lock, st.deferUnlock) > 0
}

func (ma *Analyzer) parentHoldsReadLock(stats map[string]*Stats, varName string) bool {
	st := stats[varName]
	if st == nil {
		return false
	}
	return ma.remainingLockCount(st.rlock, st.deferRUnlock) > 0
}

func (ma *Analyzer) suppressCrossGoroutineBorrowedReleases(stats map[string]*Stats, releases crossGoroutineReleases) {
	for varName, positions := range releases.unlocks {
		st := stats[varName]
		if st == nil {
			continue
		}
		for _, pos := range positions {
			if removePos(&st.borrowedUnlockPos, pos) && st.borrowedLock > 0 {
				st.borrowedLock--
			}
		}
	}

	for varName, positions := range releases.runlocks {
		st := stats[varName]
		if st == nil {
			continue
		}
		for _, pos := range positions {
			if removePos(&st.borrowedRUnlockPos, pos) && st.borrowedRLock > 0 {
				st.borrowedRLock--
			}
		}
	}
}

func (ma *Analyzer) applyCrossGoroutineReleases(stats map[string]*Stats, releases crossGoroutineReleases) {
	for varName, positions := range releases.unlocks {
		st := stats[varName]
		if st == nil {
			continue
		}
		for range positions {
			if st.lock > 0 {
				st.lock--
				ma.removeFirstLockPos(st)
			}
		}
	}

	for varName, positions := range releases.runlocks {
		st := stats[varName]
		if st == nil {
			continue
		}
		for range positions {
			if st.rlock > 0 {
				st.rlock--
				ma.removeFirstRLockPos(st)
			}
		}
	}
}

func removePos(positions *[]token.Pos, pos token.Pos) bool {
	for i, candidate := range *positions {
		if candidate != pos {
			continue
		}
		*positions = append((*positions)[:i], (*positions)[i+1:]...)
		return true
	}
	return false
}

// goroutineBodyLockCallMethod returns the first direct lock method call found
// for varName. Nested goroutines are not traversed so we only flag cases that
// are directly reachable in this goroutine's frame.
func (ma *Analyzer) goroutineBodyLockCallMethod(body *ast.BlockStmt, varName string, methodNames []string) (string, bool) {
	var foundMethod string
	ast.Inspect(body, func(n ast.Node) bool {
		if foundMethod != "" {
			return false
		}
		if _, ok := n.(*ast.GoStmt); ok {
			return false // don't recurse into nested goroutines
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if containsMethod(methodNames, sel.Sel.Name) && common.GetVarName(sel.X) == varName {
			foundMethod = sel.Sel.Name
			return false
		}
		return true
	})
	return foundMethod, foundMethod != ""
}

func (ma *Analyzer) rwMutexRequestDescription(methodName string) string {
	switch methodName {
	case "RLock", "TryRLock":
		return "read lock"
	default:
		return "write lock"
	}
}

func (ma *Analyzer) goroutineLockDeadlockMessage(varName string, isRWMutex bool, parentReadLock bool, requestMethod string, parentBlocks bool) string {
	if !isRWMutex {
		if parentBlocks {
			return "mutex '" + varName + "' goroutine started while lock is held and also tries to acquire it before parent unlocks"
		}
		return "mutex '" + varName + "' goroutine started while lock is held and also tries to acquire it, will deadlock if parent never releases"
	}

	if parentReadLock {
		if parentBlocks {
			return "rwmutex '" + varName + "' goroutine started while read lock is held and also tries to acquire write lock before parent runlocks"
		}
		return "rwmutex '" + varName + "' goroutine started while read lock is held and also tries to acquire write lock, will deadlock if parent never runlocks"
	}

	requestDescription := ma.rwMutexRequestDescription(requestMethod)
	if parentBlocks {
		return "rwmutex '" + varName + "' goroutine started while write lock is held and also tries to acquire " + requestDescription + " before parent unlocks"
	}
	return "rwmutex '" + varName + "' goroutine started while write lock is held and also tries to acquire " + requestDescription + ", will deadlock if parent never releases"
}

func (ma *Analyzer) parentBlocksBeforeUnlock(goPos token.Pos, varName string, unlockMethods []string) bool {
	if ma.function == nil || ma.function.Body == nil {
		return false
	}
	return ma.blockBlocksBeforeUnlock(ma.function.Body.List, goPos, varName, unlockMethods)
}

func (ma *Analyzer) blockBlocksBeforeUnlock(stmts []ast.Stmt, goPos token.Pos, varName string, unlockMethods []string) bool {
	for i, stmt := range stmts {
		if stmt == nil {
			continue
		}
		if stmt.Pos() == goPos {
			return ma.followingStatementsBlockBeforeUnlock(stmts[i+1:], varName, unlockMethods)
		}

		switch s := stmt.(type) {
		case *ast.BlockStmt:
			if ma.blockBlocksBeforeUnlock(s.List, goPos, varName, unlockMethods) {
				return true
			}
		case *ast.IfStmt:
			if ma.blockBlocksBeforeUnlock(s.Body.List, goPos, varName, unlockMethods) {
				return true
			}
			if ma.elseBlocksBeforeUnlock(s.Else, goPos, varName, unlockMethods) {
				return true
			}
		case *ast.ForStmt:
			if s.Body != nil && ma.blockBlocksBeforeUnlock(s.Body.List, goPos, varName, unlockMethods) {
				return true
			}
		case *ast.RangeStmt:
			if s.Body != nil && ma.blockBlocksBeforeUnlock(s.Body.List, goPos, varName, unlockMethods) {
				return true
			}
		case *ast.SwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok && ma.blockBlocksBeforeUnlock(cc.Body, goPos, varName, unlockMethods) {
					return true
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok && ma.blockBlocksBeforeUnlock(cc.Body, goPos, varName, unlockMethods) {
					return true
				}
			}
		case *ast.SelectStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CommClause); ok && ma.blockBlocksBeforeUnlock(cc.Body, goPos, varName, unlockMethods) {
					return true
				}
			}
		case *ast.LabeledStmt:
			if ma.blockBlocksBeforeUnlock([]ast.Stmt{s.Stmt}, goPos, varName, unlockMethods) {
				return true
			}
		}
	}
	return false
}

func (ma *Analyzer) elseBlocksBeforeUnlock(stmt ast.Stmt, goPos token.Pos, varName string, unlockMethods []string) bool {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return ma.blockBlocksBeforeUnlock(s.List, goPos, varName, unlockMethods)
	case *ast.IfStmt:
		return ma.blockBlocksBeforeUnlock([]ast.Stmt{s}, goPos, varName, unlockMethods)
	default:
		return false
	}
}

func (ma *Analyzer) followingStatementsBlockBeforeUnlock(stmts []ast.Stmt, varName string, unlockMethods []string) bool {
	for _, stmt := range stmts {
		if stmt == nil || ma.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		if ma.statementUnlocks(stmt, varName, unlockMethods) {
			return false
		}
		if ma.statementMayBlock(stmt) {
			return true
		}
	}
	return false
}

func (ma *Analyzer) statementUnlocks(stmt ast.Stmt, varName string, unlockMethods []string) bool {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := exprStmt.X.(*ast.CallExpr)
	if !ok || ma.commentFilter.ShouldSkipCall(call) {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && common.GetVarName(sel.X) == varName && containsMethod(unlockMethods, sel.Sel.Name)
}

func (ma *Analyzer) statementMayBlock(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		if unary, ok := s.X.(*ast.UnaryExpr); ok && unary.Op == token.ARROW {
			return true
		}
		if call, ok := s.X.(*ast.CallExpr); ok {
			return ma.callMayBlock(call)
		}
	case *ast.SelectStmt:
		return true
	}
	return false
}

func (ma *Analyzer) callMayBlock(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Wait" || ma.typesInfo == nil {
		return false
	}

	typ := ma.typesInfo.TypeOf(sel.X)
	typ = common.DerefOnceAndUnalias(typ)

	return common.MatchesPkgAndName(typ, "sync", "WaitGroup", "Cond")
}
