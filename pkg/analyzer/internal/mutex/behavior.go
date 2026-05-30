package mutex

import (
	"go/ast"
	"go/token"
	"maps"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

type crossGoroutineReleases struct {
	unlocks  map[string][]token.Pos
	runlocks map[string][]token.Pos
}

func (ma *Checker) collectCrossGoroutineReleases(body *ast.BlockStmt, parentStats map[string]*Stats) crossGoroutineReleases {
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

func (ma *Checker) scanCrossGoroutineReleaseStatements(stmts []ast.Stmt, parentStats map[string]*Stats, localLocks, localRLocks map[string]int, releases crossGoroutineReleases) {
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

func (ma *Checker) scanCrossGoroutineReleaseElse(stmt ast.Stmt, parentStats map[string]*Stats, localLocks, localRLocks map[string]int, releases crossGoroutineReleases) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		ma.scanCrossGoroutineReleaseStatements(s.List, parentStats, localLocks, localRLocks, releases)
	case *ast.IfStmt:
		ma.scanCrossGoroutineReleaseStatements([]ast.Stmt{s}, parentStats, localLocks, localRLocks, releases)
	}
}

func (ma *Checker) scanCrossGoroutineReleaseCall(call *ast.CallExpr, parentStats map[string]*Stats, localLocks, localRLocks map[string]int, releases crossGoroutineReleases) {
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
			}
		}
	}
}

func (ma *Checker) parentHoldsExclusiveLock(stats map[string]*Stats, varName string) bool {
	st := stats[varName]
	if st == nil {
		return false
	}
	return ma.remainingLockCount(st.lock, st.deferUnlock) > 0
}

func (ma *Checker) parentHoldsReadLock(stats map[string]*Stats, varName string) bool {
	st := stats[varName]
	if st == nil {
		return false
	}
	return ma.remainingLockCount(st.rlock, st.deferRUnlock) > 0
}

func (ma *Checker) suppressCrossGoroutineBorrowedReleases(stats map[string]*Stats, releases crossGoroutineReleases) {
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

func (ma *Checker) applyCrossGoroutineReleases(stats map[string]*Stats, releases crossGoroutineReleases) {
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
func (ma *Checker) goroutineBodyLockCallMethod(body *ast.BlockStmt, varName string, methodNames []string) (string, bool) {
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

func (ma *Checker) rwMutexRequestDescription(methodName string) string {
	switch methodName {
	case "RLock", "TryRLock":
		return "read lock"
	default:
		return "write lock"
	}
}

func (ma *Checker) goroutineLockDeadlockMessage(varName string, isRWMutex bool, parentReadLock bool, requestMethod string, parentBlocks bool) string {
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

func (ma *Checker) parentBlocksBeforeUnlock(goPos token.Pos, varName string, unlockMethods []string) bool {
	if ma.function == nil || ma.function.Body == nil {
		return false
	}
	return ma.blockBlocksBeforeUnlock(ma.function.Body.List, goPos, varName, unlockMethods)
}

func (ma *Checker) blockBlocksBeforeUnlock(stmts []ast.Stmt, goPos token.Pos, varName string, unlockMethods []string) bool {
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

func (ma *Checker) elseBlocksBeforeUnlock(stmt ast.Stmt, goPos token.Pos, varName string, unlockMethods []string) bool {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return ma.blockBlocksBeforeUnlock(s.List, goPos, varName, unlockMethods)
	case *ast.IfStmt:
		return ma.blockBlocksBeforeUnlock([]ast.Stmt{s}, goPos, varName, unlockMethods)
	default:
		return false
	}
}

func (ma *Checker) followingStatementsBlockBeforeUnlock(stmts []ast.Stmt, varName string, unlockMethods []string) bool {
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

func (ma *Checker) statementUnlocks(stmt ast.Stmt, varName string, unlockMethods []string) bool {
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

func (ma *Checker) statementMayBlock(stmt ast.Stmt) bool {
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

func (ma *Checker) callMayBlock(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Wait" || ma.typesInfo == nil {
		return false
	}

	typ := ma.typesInfo.TypeOf(sel.X)
	typ = common.DerefOnceAndUnalias(typ)

	return common.MatchesPkgAndName(typ, "sync", "WaitGroup", "Cond")
}
