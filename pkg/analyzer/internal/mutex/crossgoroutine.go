package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
)

// crossGoroutineDetector reasons about lock/unlock effects that cross the
// boundary between a parent function and a goroutine it launches:
//
//   - releases: a goroutine that unlocks a mutex the parent had locked
//     ("ownership transfer"), so the parent's stats must be reconciled instead
//     of flagging an unmatched lock;
//   - deadlocks: a goroutine that tries to acquire a mutex the parent still
//     holds, which blocks forever if the parent never releases it.
//
// It was extracted from Checker because the logic is cohesive and config-only:
// it reads the primitive name sets, the comment filter and type info, and
// operates on *Stats maps passed in by the flow analyzer. It holds no
// per-function state — the one place that needs the enclosing function
// (parentBlocksBeforeUnlock) receives it as an argument — so a single instance
// serves every goroutine in every function.
type crossGoroutineDetector struct {
	mutexNames    map[string]bool
	rwMutexNames  map[string]bool
	commentFilter *commentfilter.CommentFilter
	typesInfo     *types.Info
}

func newCrossGoroutineDetector(mutexNames, rwMutexNames map[string]bool, cf *commentfilter.CommentFilter, typesInfo *types.Info) *crossGoroutineDetector {
	return &crossGoroutineDetector{
		mutexNames:    mutexNames,
		rwMutexNames:  rwMutexNames,
		commentFilter: cf,
		typesInfo:     typesInfo,
	}
}

// crossGoroutineReleases records the positions at which a goroutine releases a
// lock that belongs to the parent, split by exclusive (unlocks) and read
// (runlocks) ownership.
type crossGoroutineReleases struct {
	unlocks  map[string][]token.Pos
	runlocks map[string][]token.Pos
}

// collectReleases scans a goroutine body for Unlock/RUnlock calls that release a
// lock the parent (parentStats) is still holding, i.e. ownership handed off from
// parent to goroutine.
func (d *crossGoroutineDetector) collectReleases(body *ast.BlockStmt, parentStats map[string]*Stats) crossGoroutineReleases {
	releases := crossGoroutineReleases{
		unlocks:  make(map[string][]token.Pos),
		runlocks: make(map[string][]token.Pos),
	}
	if body == nil {
		return releases
	}

	d.scanStatements(body.List, parentStats, make(map[string]int), make(map[string]int), releases)
	return releases
}

func (d *crossGoroutineDetector) scanStatements(stmts []ast.Stmt, parentStats map[string]*Stats, localLocks, localRLocks map[string]int, releases crossGoroutineReleases) {
	for _, stmt := range stmts {
		if stmt == nil || d.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok {
				d.scanCall(call, parentStats, localLocks, localRLocks, releases)
			}
		case *ast.DeferStmt:
			d.scanCall(s.Call, parentStats, localLocks, localRLocks, releases)
		case *ast.IfStmt:
			d.scanStatements(s.Body.List, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
			if s.Else != nil {
				d.scanElse(s.Else, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
			}
		case *ast.ForStmt:
			if s.Body != nil {
				d.scanStatements(s.Body.List, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
			}
		case *ast.RangeStmt:
			if s.Body != nil {
				d.scanStatements(s.Body.List, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
			}
		case *ast.BlockStmt:
			d.scanStatements(s.List, parentStats, localLocks, localRLocks, releases)
		case *ast.LabeledStmt:
			d.scanStatements([]ast.Stmt{s.Stmt}, parentStats, localLocks, localRLocks, releases)
		case *ast.SwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					d.scanStatements(cc.Body, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					d.scanStatements(cc.Body, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
				}
			}
		case *ast.SelectStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CommClause); ok {
					d.scanStatements(cc.Body, parentStats, maps.Clone(localLocks), maps.Clone(localRLocks), releases)
				}
			}
		}
	}
}

func (d *crossGoroutineDetector) scanElse(stmt ast.Stmt, parentStats map[string]*Stats, localLocks, localRLocks map[string]int, releases crossGoroutineReleases) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		d.scanStatements(s.List, parentStats, localLocks, localRLocks, releases)
	case *ast.IfStmt:
		d.scanStatements([]ast.Stmt{s}, parentStats, localLocks, localRLocks, releases)
	}
}

func (d *crossGoroutineDetector) scanCall(call *ast.CallExpr, parentStats map[string]*Stats, localLocks, localRLocks map[string]int, releases crossGoroutineReleases) {
	if call == nil || d.commentFilter.ShouldSkipCall(call) {
		return
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	varName := common.GetVarName(sel.X)
	switch sel.Sel.Name {
	case "Lock", "TryLock":
		if d.mutexNames[varName] || d.rwMutexNames[varName] {
			localLocks[varName]++
		}
	case "Unlock":
		if d.mutexNames[varName] || d.rwMutexNames[varName] {
			if localLocks[varName] > 0 {
				localLocks[varName]--
				return
			}
			if d.parentHoldsExclusiveLock(parentStats, varName) {
				releases.unlocks[varName] = append(releases.unlocks[varName], call.Pos())
			}
		}
	case "RLock", "TryRLock":
		if d.rwMutexNames[varName] {
			localRLocks[varName]++
		}
	case "RUnlock":
		if d.rwMutexNames[varName] {
			if localRLocks[varName] > 0 {
				localRLocks[varName]--
				return
			}
			if d.parentHoldsReadLock(parentStats, varName) {
				releases.runlocks[varName] = append(releases.runlocks[varName], call.Pos())
			}
		}
	}
}

func (d *crossGoroutineDetector) parentHoldsExclusiveLock(stats map[string]*Stats, varName string) bool {
	st := stats[varName]
	if st == nil {
		return false
	}
	return remainingLockCount(st.lock, st.deferUnlock) > 0
}

func (d *crossGoroutineDetector) parentHoldsReadLock(stats map[string]*Stats, varName string) bool {
	st := stats[varName]
	if st == nil {
		return false
	}
	return remainingLockCount(st.rlock, st.deferRUnlock) > 0
}

// suppressBorrowedReleases cancels the "borrowed" lock bookkeeping for releases
// the goroutine performed on the parent's behalf, so the goroutine's own stats
// are not flagged as holding a lock it handed back.
func (d *crossGoroutineDetector) suppressBorrowedReleases(stats map[string]*Stats, releases crossGoroutineReleases) {
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

// applyReleases reconciles the parent's stats by consuming the locks the
// goroutine released on its behalf.
func (d *crossGoroutineDetector) applyReleases(stats map[string]*Stats, releases crossGoroutineReleases) {
	for varName, positions := range releases.unlocks {
		st := stats[varName]
		if st == nil {
			continue
		}
		for range positions {
			if st.lock > 0 {
				st.lock--
				st.removeFirstLockPos()
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
				st.removeFirstRLockPos()
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
func (d *crossGoroutineDetector) goroutineBodyLockCallMethod(body *ast.BlockStmt, varName string, methodNames []string) (string, bool) {
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

func (d *crossGoroutineDetector) rwMutexRequestDescription(methodName string) string {
	switch methodName {
	case "RLock", "TryRLock":
		return "read lock"
	default:
		return "write lock"
	}
}

func (d *crossGoroutineDetector) deadlockMessage(varName string, isRWMutex bool, parentReadLock bool, requestMethod string, parentBlocks bool) string {
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

	requestDescription := d.rwMutexRequestDescription(requestMethod)
	if parentBlocks {
		return "rwmutex '" + varName + "' goroutine started while write lock is held and also tries to acquire " + requestDescription + " before parent unlocks"
	}
	return "rwmutex '" + varName + "' goroutine started while write lock is held and also tries to acquire " + requestDescription + ", will deadlock if parent never releases"
}

// parentBlocksBeforeUnlock reports whether the parent, after launching the
// goroutine at goPos, reaches a blocking operation before releasing varName —
// the condition that turns a held lock into a guaranteed deadlock. function is
// the enclosing function whose body is scanned.
func (d *crossGoroutineDetector) parentBlocksBeforeUnlock(function *ast.FuncDecl, goPos token.Pos, varName string, unlockMethods []string) bool {
	if function == nil || function.Body == nil {
		return false
	}
	return d.blockBlocksBeforeUnlock(function.Body.List, goPos, varName, unlockMethods)
}

func (d *crossGoroutineDetector) blockBlocksBeforeUnlock(stmts []ast.Stmt, goPos token.Pos, varName string, unlockMethods []string) bool {
	for i, stmt := range stmts {
		if stmt == nil {
			continue
		}
		if stmt.Pos() == goPos {
			return d.followingStatementsBlockBeforeUnlock(stmts[i+1:], varName, unlockMethods)
		}

		switch s := stmt.(type) {
		case *ast.BlockStmt:
			if d.blockBlocksBeforeUnlock(s.List, goPos, varName, unlockMethods) {
				return true
			}
		case *ast.IfStmt:
			if d.blockBlocksBeforeUnlock(s.Body.List, goPos, varName, unlockMethods) {
				return true
			}
			if d.elseBlocksBeforeUnlock(s.Else, goPos, varName, unlockMethods) {
				return true
			}
		case *ast.ForStmt:
			if s.Body != nil && d.blockBlocksBeforeUnlock(s.Body.List, goPos, varName, unlockMethods) {
				return true
			}
		case *ast.RangeStmt:
			if s.Body != nil && d.blockBlocksBeforeUnlock(s.Body.List, goPos, varName, unlockMethods) {
				return true
			}
		case *ast.SwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok && d.blockBlocksBeforeUnlock(cc.Body, goPos, varName, unlockMethods) {
					return true
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok && d.blockBlocksBeforeUnlock(cc.Body, goPos, varName, unlockMethods) {
					return true
				}
			}
		case *ast.SelectStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CommClause); ok && d.blockBlocksBeforeUnlock(cc.Body, goPos, varName, unlockMethods) {
					return true
				}
			}
		case *ast.LabeledStmt:
			if d.blockBlocksBeforeUnlock([]ast.Stmt{s.Stmt}, goPos, varName, unlockMethods) {
				return true
			}
		}
	}
	return false
}

func (d *crossGoroutineDetector) elseBlocksBeforeUnlock(stmt ast.Stmt, goPos token.Pos, varName string, unlockMethods []string) bool {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return d.blockBlocksBeforeUnlock(s.List, goPos, varName, unlockMethods)
	case *ast.IfStmt:
		return d.blockBlocksBeforeUnlock([]ast.Stmt{s}, goPos, varName, unlockMethods)
	default:
		return false
	}
}

func (d *crossGoroutineDetector) followingStatementsBlockBeforeUnlock(stmts []ast.Stmt, varName string, unlockMethods []string) bool {
	for _, stmt := range stmts {
		if stmt == nil || d.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		if d.statementUnlocks(stmt, varName, unlockMethods) {
			return false
		}
		if d.statementMayBlock(stmt) {
			return true
		}
	}
	return false
}

func (d *crossGoroutineDetector) statementUnlocks(stmt ast.Stmt, varName string, unlockMethods []string) bool {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := exprStmt.X.(*ast.CallExpr)
	if !ok || d.commentFilter.ShouldSkipCall(call) {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && common.GetVarName(sel.X) == varName && containsMethod(unlockMethods, sel.Sel.Name)
}

func (d *crossGoroutineDetector) statementMayBlock(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		if unary, ok := s.X.(*ast.UnaryExpr); ok && unary.Op == token.ARROW {
			return true
		}
		if call, ok := s.X.(*ast.CallExpr); ok {
			return d.callMayBlock(call)
		}
	case *ast.SelectStmt:
		return true
	}
	return false
}

func (d *crossGoroutineDetector) callMayBlock(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Wait" || d.typesInfo == nil {
		return false
	}

	typ := d.typesInfo.TypeOf(sel.X)
	typ = common.DerefOnceAndUnalias(typ)

	return common.MatchesPkgAndName(typ, "sync", "WaitGroup", "Cond")
}
