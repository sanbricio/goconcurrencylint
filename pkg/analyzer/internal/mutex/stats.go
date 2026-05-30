package mutex

import (
	"bytes"
	"go/ast"
	"go/printer"
	"go/token"
	"go/types"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

// analyzeBlock analyzes a block statement starting from the provided state and
// returns the resulting stats after executing that block.
func (ma *Checker) analyzeBlock(block *ast.BlockStmt, initial map[string]*Stats) map[string]*Stats {
	return ma.analyzeStatementList(block.List, initial)
}

func (ma *Checker) analyzeStatementList(stmts []ast.Stmt, initial map[string]*Stats) map[string]*Stats {
	blockStats := ma.cloneStatsMap(initial)
	skip := make(map[token.Pos]bool)
	terminatingTail := ma.terminatingTailByIndex(stmts)

	for i, stmt := range stmts {
		if ma.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		if skip[stmt.Pos()] {
			continue
		}
		if ma.skipBalancedGuardedLock(stmt, stmts[i+1:], skip) {
			continue
		}
		ma.analyzeStatementWithTail(stmt, blockStats, terminatingTail[i+1])
	}

	return blockStats
}

func (ma *Checker) analyzeStatementWithTail(stmt ast.Stmt, stats map[string]*Stats, tailTerminates bool) {
	if _, ok := stmt.(*ast.IfStmt); !ok || !tailTerminates {
		ma.analyzeStatement(stmt, stats)
		return
	}

	ma.terminatingTailDepth++
	defer func() { ma.terminatingTailDepth-- }()
	ma.analyzeStatement(stmt, stats)
}

func (ma *Checker) terminatingTailByIndex(stmts []ast.Stmt) []bool {
	tail := make([]bool, len(stmts)+1)
	for i := range slices.Backward(stmts) {
		tail[i] = tail[i+1] || ma.statementAlwaysTerminates(stmts[i])
	}
	return tail
}

func (ma *Checker) skipBalancedGuardedLock(stmt ast.Stmt, rest []ast.Stmt, skip map[token.Pos]bool) bool {
	guard, varName, methodName, ok := ma.guardedMutexCall(stmt)
	if !ok || !isLockMethod(methodName) {
		return false
	}

	releaseMethod := matchingUnlockMethod(methodName)
	if releaseMethod == "" {
		return false
	}

	for _, later := range rest {
		if ma.guardedReleaseMatches(later, guard, varName, releaseMethod) {
			skip[later.Pos()] = true
			return true
		}
		if ma.statementMayExit(later) {
			return false
		}
	}

	return false
}

// guardedReleaseMatches reports whether `stmt` releases `varName` under
// `guard` on every reachable path.
func (ma *Checker) guardedReleaseMatches(stmt ast.Stmt, guard, varName, releaseMethod string) bool {
	if laterGuard, laterVar, laterMethod, ok := ma.guardedMutexCall(stmt); ok {
		return laterGuard == guard && laterVar == varName && laterMethod == releaseMethod
	}

	cond, body, ok := ma.guardedIf(stmt)
	if !ok || cond != guard {
		return false
	}
	return ma.bodyReleasesOnEveryPath(body, varName, releaseMethod)
}

func (ma *Checker) statementMayExit(stmt ast.Stmt) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.DeferStmt, *ast.GoStmt:
			return false
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			found = true
			return false
		case *ast.BranchStmt:
			if node.Tok == token.GOTO || node.Tok == token.BREAK || node.Tok == token.CONTINUE {
				found = true
				return false
			}
		case *ast.CallExpr:
			if ma.callTerminatesExecution(node) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func (ma *Checker) callTerminatesExecution(call *ast.CallExpr) bool {
	if call == nil {
		return false
	}
	if ident, ok := call.Fun.(*ast.Ident); ok {
		return ma.isBuiltinPanic(ident)
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	methodName := sel.Sel.Name
	if ma.typesInfo != nil {
		if obj := ma.typesInfo.ObjectOf(sel.Sel); obj != nil && obj.Pkg() != nil {
			switch obj.Pkg().Path() {
			case "os":
				return methodName == "Exit"
			case "runtime":
				return methodName == "Goexit"
			case "log", "testing":
				return isFatalMethod(methodName)
			}
		}
	}

	receiverName := common.GetVarName(sel.X)
	switch receiverName {
	case "os":
		return methodName == "Exit"
	case "runtime":
		return methodName == "Goexit"
	case "log":
		return isFatalMethod(methodName)
	default:
		return false
	}
}

func (ma *Checker) isBuiltinPanic(ident *ast.Ident) bool {
	if ident == nil || ident.Name != "panic" {
		return false
	}
	if ma.typesInfo == nil {
		return true
	}
	obj := ma.typesInfo.ObjectOf(ident)
	if obj == nil {
		return true
	}
	_, ok := obj.(*types.Builtin)
	return ok
}

func isFatalMethod(methodName string) bool {
	return methodName == "Fatal" || methodName == "Fatalf" || methodName == "Fatalln"
}

// guardedIf returns the condition and body for a plain `if cond { body }`.
func (ma *Checker) guardedIf(stmt ast.Stmt) (string, *ast.BlockStmt, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil || ifStmt.Body == nil {
		return "", nil, false
	}
	return exprString(ifStmt.Cond), ifStmt.Body, true
}

// guardedMutexCall detects `if cond { mu.Lock() }` and
// `if cond { mu.Unlock() }` forms with one mutex call.
func (ma *Checker) guardedMutexCall(stmt ast.Stmt) (string, string, string, bool) {
	cond, body, ok := ma.guardedIf(stmt)
	if !ok {
		return "", "", "", false
	}

	var varName, methodName string
	foundCalls := 0
	for _, bodyStmt := range body.List {
		if ma.statementMayExit(bodyStmt) {
			return "", "", "", false
		}
		ast.Inspect(bodyStmt, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || ma.commentFilter.ShouldSkipCall(call) {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			candidateVarName := common.GetVarName(sel.X)
			if !ma.mutexNames[candidateVarName] && !ma.rwMutexNames[candidateVarName] {
				return true
			}
			candidateMethodName := sel.Sel.Name
			if !isLockMethod(candidateMethodName) && !isUnlockMethod(candidateMethodName) {
				return true
			}
			foundCalls++
			varName = candidateVarName
			methodName = candidateMethodName
			return true
		})
	}
	if foundCalls != 1 {
		return "", "", "", false
	}

	return cond, varName, methodName, true
}

// bodyReleasesOnEveryPath reports whether `body` unlocks exactly once before
// each reachable exit.
func (ma *Checker) bodyReleasesOnEveryPath(body *ast.BlockStmt, varName, methodName string) bool {
	if body == nil {
		return false
	}
	sim := pathReleaseSimulator{analyzer: ma, varName: varName, method: methodName}
	count, terminated, ok := sim.run(body.List, 0)
	if !ok {
		return false
	}
	if terminated {
		return true
	}
	return count == 1
}

// pathReleaseSimulator checks simple paths for one matching unlock before exit.
type pathReleaseSimulator struct {
	analyzer *Checker
	varName  string
	method   string
}

// run returns the release count, whether all paths terminate, and whether the
// statement list can be modelled.
func (s *pathReleaseSimulator) run(stmts []ast.Stmt, incoming int) (count int, terminated bool, ok bool) {
	count = incoming
	for _, stmt := range stmts {
		if stmt == nil || s.analyzer.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		switch n := stmt.(type) {
		case *ast.ExprStmt:
			if s.isMatchingRelease(n.X) {
				count++
				continue
			}
			if s.exprTerminates(n.X) {
				return releasePathTerminated(count)
			}
		case *ast.AssignStmt, *ast.DeclStmt, *ast.IncDecStmt, *ast.SendStmt:
		case *ast.ReturnStmt:
			return releasePathTerminated(count)
		case *ast.BranchStmt:
			if branchTerminatesBlock(n.Tok) {
				return releasePathTerminated(count)
			}
			return 0, false, false
		case *ast.IfStmt:
			next, term, branchOK := s.simulateIf(n, count)
			if !branchOK {
				return 0, false, false
			}
			if term {
				return next, true, true
			}
			count = next
		case *ast.BlockStmt:
			bc, bt, bo := s.run(n.List, count)
			if !bo {
				return 0, false, false
			}
			if bt {
				return bc, true, true
			}
			count = bc
		default:
			return 0, false, false
		}
	}
	return count, false, true
}

func releasePathTerminated(count int) (int, bool, bool) {
	if count != 1 {
		return 0, true, false
	}
	return count, true, true
}

// simulateIf merges the release counts from the then and else branches.
func (s *pathReleaseSimulator) simulateIf(n *ast.IfStmt, incoming int) (int, bool, bool) {
	if n.Init != nil {
		initCount, initTerm, initOK := s.run([]ast.Stmt{n.Init}, incoming)
		if !initOK {
			return 0, false, false
		}
		if initTerm {
			return initCount, true, true
		}
		incoming = initCount
	}

	thenCount, thenTerm, thenOK := s.run(n.Body.List, incoming)
	if !thenOK {
		return 0, false, false
	}

	var elseCount int
	elseTerm := false
	if n.Else != nil {
		ec, et, eo := s.runElse(n.Else, incoming)
		if !eo {
			return 0, false, false
		}
		elseCount, elseTerm = ec, et
	} else {
		// Skipping the `if` keeps the incoming count unchanged.
		elseCount = incoming
	}

	switch {
	case thenTerm && elseTerm:
		return incoming, true, true
	case thenTerm:
		return elseCount, false, true
	case elseTerm:
		return thenCount, false, true
	}

	if thenCount != elseCount {
		return 0, false, false
	}
	return thenCount, false, true
}

func (s *pathReleaseSimulator) runElse(elseNode ast.Stmt, incoming int) (int, bool, bool) {
	switch e := elseNode.(type) {
	case *ast.BlockStmt:
		return s.run(e.List, incoming)
	case *ast.IfStmt:
		return s.run([]ast.Stmt{e}, incoming)
	default:
		return 0, false, false
	}
}

func (s *pathReleaseSimulator) exprTerminates(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	return ok && s.analyzer.callTerminatesExecution(call)
}

// isMatchingRelease reports whether `expr` calls the tracked unlock method.
func (s *pathReleaseSimulator) isMatchingRelease(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || s.analyzer.commentFilter.ShouldSkipCall(call) {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if common.GetVarName(sel.X) != s.varName {
		return false
	}
	return sel.Sel.Name == s.method
}

func exprString(expr ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, token.NewFileSet(), expr); err != nil {
		return ""
	}
	return buf.String()
}

func isLockMethod(methodName string) bool {
	return methodName == "Lock" || methodName == "RLock"
}

func isUnlockMethod(methodName string) bool {
	return methodName == "Unlock" || methodName == "RUnlock"
}

func matchingUnlockMethod(methodName string) string {
	switch methodName {
	case "Lock":
		return "Unlock"
	case "RLock":
		return "RUnlock"
	default:
		return ""
	}
}

// analyzeStatement analyzes individual statements
func (ma *Checker) analyzeStatement(stmt ast.Stmt, stats map[string]*Stats) {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		ma.reportPotentialPanicWhileLocked(s, stats)
		ma.analyzeExpressionStatement(s, stats)
	case *ast.AssignStmt:
		ma.analyzeAssignStatement(s, stats)
	case *ast.DeclStmt:
		ma.analyzeDeclStatement(s, stats)
	case *ast.DeferStmt:
		ma.analyzeDeferStatement(s, stats)
	case *ast.ReturnStmt:
		ma.tryLock.markReturnedChecked(s)
		ma.reportPotentialPanicWhileLocked(s, stats)
		ma.analyzeReturnStatement(s, stats)
	case *ast.IfStmt:
		ma.analyzeIfStatement(s, stats)
	case *ast.GoStmt:
		ma.analyzeGoStatement(s, stats)
	case *ast.ForStmt:
		ma.analyzeForStatement(s, stats)
	case *ast.RangeStmt:
		ma.analyzeRangeStatement(s, stats)
	case *ast.SwitchStmt:
		ma.analyzeSwitchStatement(s, stats)
	case *ast.TypeSwitchStmt:
		ma.analyzeTypeSwitchStatement(s, stats)
	case *ast.SelectStmt:
		ma.analyzeSelectStatement(s, stats)
	case *ast.LabeledStmt:
		if s.Label != nil {
			ma.applyLabelSnapshot(s.Label.Name, stats)
		}
		ma.analyzeStatement(s.Stmt, stats)
	case *ast.BranchStmt:
		if s.Tok == token.GOTO && s.Label != nil {
			ma.captureGotoSnapshot(s.Label.Name, stats)
		}
	case *ast.BlockStmt:
		nestedStats := ma.analyzeBlock(s, stats)
		ma.copyStatsMap(stats, nestedStats)
	}
}

// analyzeAssignStatement handles assignments: collection-length bookkeeping,
// potential-panic-while-locked reporting, and TryLock result tracking (the
// latter delegated to the per-function tryLockTracker).
func (ma *Checker) analyzeAssignStatement(stmt *ast.AssignStmt, stats map[string]*Stats) {
	ma.recordCollectionLengthsFromAssign(stmt)
	ma.reportPotentialPanicWhileLocked(stmt, stats)
	ma.tryLock.recordAssignment(stmt)
}

func (ma *Checker) analyzeDeclStatement(stmt *ast.DeclStmt, stats map[string]*Stats) {
	ma.recordCollectionLengthsFromDecl(stmt)
	ma.reportPotentialPanicWhileLocked(stmt, stats)
}

// analyzeExpressionStatement handles expression statements (Lock/Unlock calls)
func (ma *Checker) analyzeExpressionStatement(stmt *ast.ExprStmt, stats map[string]*Stats) {
	call, ok := stmt.X.(*ast.CallExpr)
	if !ok {
		return
	}

	if ma.commentFilter.ShouldSkipCall(call) {
		return
	}

	if ma.applyLocalFunctionLiteralLifecycleEffects(call, stats) {
		return
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	if sel.Sel.Name == "Cleanup" && len(call.Args) == 1 {
		if fnlit, ok := call.Args[0].(*ast.FuncLit); ok {
			ma.handleDeferFunctionLiteral(fnlit, call.Pos(), stats)
		}
		return
	}

	varName := common.GetVarName(sel.X)

	// When a TryLock/TryRLock return value is ignored, the caller has no way to
	// know whether the lock was actually acquired, so any subsequent operation
	// that assumes the lock is held is racy.
	switch sel.Sel.Name {
	case "TryLock":
		if ma.mutexNames[varName] {
			ma.errorCollector.AddError(call.Pos(), category.UncheckedTryLock, "mutex '"+varName+"' TryLock return value not checked, lock may not be held")
			return
		}
		if ma.rwMutexNames[varName] {
			ma.errorCollector.AddError(call.Pos(), category.UncheckedTryLock, "rwmutex '"+varName+"' TryLock return value not checked, lock may not be held")
			return
		}
	case "TryRLock":
		if ma.rwMutexNames[varName] {
			ma.errorCollector.AddError(call.Pos(), category.UncheckedTryLock, "rwmutex '"+varName+"' TryRLock return value not checked, lock may not be held")
			return
		}
	}

	if ma.mutexNames[varName] {
		ma.handleMutexCall(varName, sel.Sel.Name, call.Pos(), stats)
	}

	if ma.rwMutexNames[varName] {
		ma.handleRWMutexCall(varName, sel.Sel.Name, call.Pos(), stats)
	}

	ma.applyLocalMethodLifecycleEffects(call, stats)
}

// handleMutexCall processes mutex method calls
func (ma *Checker) handleMutexCall(varName, methodName string, pos token.Pos, stats map[string]*Stats) {
	if ma.isBorrowedWrapperCall(varName, methodName) {
		return
	}

	switch methodName {
	case "Lock":
		if stats[varName].lock > 0 {
			ma.errorCollector.AddError(pos, category.DoubleLock, "mutex '"+varName+"' is re-locked before unlock")
		}
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			ma.removeFirstBorrowedUnlockPos(stats[varName])
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "TryLock":
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			ma.removeFirstBorrowedUnlockPos(stats[varName])
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "Unlock":
		if stats[varName].lock == 0 {
			if ma.isCarriedLoopUnlock(varName, pos, []string{"Lock", "TryLock"}, []string{"Unlock"}) {
				return
			}
			stats[varName].borrowedLock++
			stats[varName].borrowedUnlockPos = append(stats[varName].borrowedUnlockPos, pos)
		} else {
			stats[varName].lock--
			ma.removeFirstLockPos(stats[varName])
		}
	}
}

// handleRWMutexCall processes rwmutex method calls
func (ma *Checker) handleRWMutexCall(varName, methodName string, pos token.Pos, stats map[string]*Stats) {
	if ma.isBorrowedWrapperCall(varName, methodName) {
		return
	}

	switch methodName {
	case "Lock":
		if stats[varName].rlock > 0 {
			ma.errorCollector.AddError(pos, category.DoubleLock, "rwmutex '"+varName+"' attempts write Lock while read lock is held")
		}
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			ma.removeFirstBorrowedUnlockPos(stats[varName])
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "TryLock":
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			ma.removeFirstBorrowedUnlockPos(stats[varName])
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "Unlock":
		// Unlock called when only a read lock is held.
		// Correct the state as if RUnlock was called to avoid cascading errors.
		if stats[varName].rlock > 0 && stats[varName].lock == 0 {
			ma.errorCollector.AddError(pos, category.RWMutexAPIMismatch, "rwmutex '"+varName+"' Unlock called but only read lock is held, did you mean RUnlock?")
			stats[varName].rlock--
			ma.removeFirstRLockPos(stats[varName])
			return
		}
		if stats[varName].lock == 0 {
			if ma.isCarriedLoopUnlock(varName, pos, []string{"Lock", "TryLock"}, []string{"Unlock"}) {
				return
			}
			stats[varName].borrowedLock++
			stats[varName].borrowedUnlockPos = append(stats[varName].borrowedUnlockPos, pos)
		} else {
			stats[varName].lock--
			ma.removeFirstLockPos(stats[varName])
		}
	case "RLock", "TryRLock":
		if stats[varName].borrowedRLock > 0 {
			stats[varName].borrowedRLock--
			ma.removeFirstBorrowedRUnlockPos(stats[varName])
			return
		}
		stats[varName].rlock++
		stats[varName].rlockPos = append(stats[varName].rlockPos, pos)
	case "RUnlock":
		// RUnlock called when only a write lock is held.
		// Correct the state as if Unlock was called to avoid cascading errors.
		if stats[varName].lock > 0 && stats[varName].rlock == 0 {
			ma.errorCollector.AddError(pos, category.RWMutexAPIMismatch, "rwmutex '"+varName+"' RUnlock called but only write lock is held, did you mean Unlock?")
			stats[varName].lock--
			ma.removeFirstLockPos(stats[varName])
			return
		}
		if stats[varName].rlock == 0 {
			if ma.isCarriedLoopUnlock(varName, pos, []string{"RLock", "TryRLock"}, []string{"RUnlock"}) {
				return
			}
			stats[varName].borrowedRLock++
			stats[varName].borrowedRUnlockPos = append(stats[varName].borrowedRUnlockPos, pos)
		} else {
			stats[varName].rlock--
			ma.removeFirstRLockPos(stats[varName])
		}
	}
}

func (ma *Checker) analyzeReturnStatement(stmt *ast.ReturnStmt, stats map[string]*Stats) {
	for _, result := range stmt.Results {
		call, ok := result.(*ast.CallExpr)
		if ok && !ma.commentFilter.ShouldSkipCall(call) {
			// Apply callee effects for `return helper()` forms.
			if !ma.applyLocalFunctionLiteralLifecycleEffects(call, stats) {
				ma.applyLocalMethodLifecycleEffects(call, stats)
			}
		}

		fnlit, ok := result.(*ast.FuncLit)
		if !ok {
			continue
		}
		ma.handleDeferFunctionLiteral(fnlit, stmt.Pos(), stats)
	}

	if !ma.rawBodyEffects {
		ma.reportUnmatchedLocks(stats)
	}
}

// analyzeDeferStatement handles defer statements
func (ma *Checker) analyzeDeferStatement(stmt *ast.DeferStmt, stats map[string]*Stats) {
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
func (ma *Checker) handleDeferCall(call *ast.SelectorExpr, pos token.Pos, stats map[string]*Stats) {
	varName := common.GetVarName(call.X)

	if call.Sel.Name == "Lock" && ma.consumeBorrowedDeferredLock(varName, stats) {
		return
	}
	if call.Sel.Name == "RLock" && ma.consumeBorrowedDeferredRLock(varName, stats) {
		return
	}
	if call.Sel.Name == "Lock" && ma.deferredRelockBalancesEarlierDeferredUnlock(varName, stats) {
		return
	}
	if call.Sel.Name == "RLock" && ma.deferredRRelockBalancesEarlierDeferredRUnlock(varName, stats) {
		return
	}

	// defer Lock / defer RLock re-acquires the lock on
	// function return instead of releasing it, guaranteed deadlock.
	if ma.mutexNames[varName] && call.Sel.Name == "Lock" {
		ma.errorCollector.AddError(pos, category.DeferLock, "mutex '"+varName+"' defer calls Lock instead of Unlock, will deadlock on return")
		return
	}
	if ma.rwMutexNames[varName] {
		switch call.Sel.Name {
		case "Lock":
			ma.errorCollector.AddError(pos, category.DeferLock, "rwmutex '"+varName+"' defer calls Lock instead of Unlock, will deadlock on return")
			return
		case "RLock":
			ma.errorCollector.AddError(pos, category.DeferLock, "rwmutex '"+varName+"' defer calls RLock instead of RUnlock, will deadlock on return")
			return
		}
	}

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

func (ma *Checker) consumeBorrowedDeferredLock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	if st == nil || st.borrowedLock == 0 {
		return false
	}
	st.borrowedLock--
	ma.removeFirstBorrowedUnlockPos(st)
	return true
}

func (ma *Checker) consumeBorrowedDeferredRLock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	if st == nil || st.borrowedRLock == 0 {
		return false
	}
	st.borrowedRLock--
	ma.removeFirstBorrowedRUnlockPos(st)
	return true
}

func (ma *Checker) deferredRelockBalancesEarlierDeferredUnlock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	return st != nil && st.lock == 0 && st.deferUnlock > 0
}

func (ma *Checker) deferredRRelockBalancesEarlierDeferredRUnlock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	return st != nil && st.rlock == 0 && st.deferRUnlock > 0
}

// handleDeferFunctionLiteral processes defer with function literals
func (ma *Checker) handleDeferFunctionLiteral(fnlit *ast.FuncLit, pos token.Pos, stats map[string]*Stats) {
	// Check for mutex unlocks in function literal
	for mutexName := range ma.mutexNames {
		if ma.containsUnlock(fnlit.Body, mutexName) && !ma.containsLock(fnlit.Body, mutexName) {
			if stats[mutexName].lock == 0 && ma.unlocksOnlyInRecoverGuard(fnlit.Body, mutexName, "Unlock") {
				continue
			}
			ma.handleDeferUnlock(mutexName, pos, stats, false)
		}
	}

	// Check for rwmutex unlocks in function literal
	for rwMutexName := range ma.rwMutexNames {
		if ma.containsUnlock(fnlit.Body, rwMutexName) && !ma.containsLock(fnlit.Body, rwMutexName) {
			if stats[rwMutexName].lock == 0 && ma.unlocksOnlyInRecoverGuard(fnlit.Body, rwMutexName, "Unlock") {
				continue
			}
			ma.handleDeferUnlock(rwMutexName, pos, stats, true)
		}
		if ma.containsRUnlock(fnlit.Body, rwMutexName) && !ma.containsRLock(fnlit.Body, rwMutexName) {
			if stats[rwMutexName].rlock == 0 && ma.unlocksOnlyInRecoverGuard(fnlit.Body, rwMutexName, "RUnlock") {
				continue
			}
			ma.handleDeferRUnlock(rwMutexName, pos, stats)
		}
	}
}

// unlocksOnlyInRecoverGuard reports whether the block contains at least one
// target unlock and every target unlock is guarded by a recover() check.
func (ma *Checker) unlocksOnlyInRecoverGuard(block *ast.BlockStmt, mutexName, methodName string) bool {
	foundUnlock := false
	foundUnguardedUnlock := false

	var (
		visitStmt  func(ast.Stmt, bool)
		visitBlock func(*ast.BlockStmt, bool)
		visitExpr  func(ast.Expr, bool)
	)

	visitExpr = func(expr ast.Expr, recoverGuarded bool) {
		if expr == nil || foundUnguardedUnlock {
			return
		}
		ast.Inspect(expr, func(n ast.Node) bool {
			if foundUnguardedUnlock {
				return false
			}
			if fnlit, ok := n.(*ast.FuncLit); ok && fnlit != expr {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || ma.commentFilter.ShouldSkipCall(call) {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != methodName || common.GetVarName(sel.X) != mutexName {
				return true
			}
			foundUnlock = true
			if !recoverGuarded {
				foundUnguardedUnlock = true
			}
			return false
		})
	}

	visitBlock = func(block *ast.BlockStmt, recoverGuarded bool) {
		if block == nil || foundUnguardedUnlock {
			return
		}
		for _, stmt := range block.List {
			visitStmt(stmt, recoverGuarded)
			if foundUnguardedUnlock {
				return
			}
		}
	}

	visitStmt = func(stmt ast.Stmt, recoverGuarded bool) {
		if stmt == nil || foundUnguardedUnlock {
			return
		}
		switch s := stmt.(type) {
		case *ast.BlockStmt:
			visitBlock(s, recoverGuarded)
		case *ast.LabeledStmt:
			visitStmt(s.Stmt, recoverGuarded)
		case *ast.IfStmt:
			bodyGuarded := recoverGuarded || containsRecoverCall(s.Init) || containsRecoverCall(s.Cond)
			visitBlock(s.Body, bodyGuarded)
			if s.Else != nil {
				visitStmt(s.Else, recoverGuarded)
			}
		case *ast.ForStmt:
			visitStmt(s.Init, recoverGuarded)
			visitExpr(s.Cond, recoverGuarded)
			visitStmt(s.Post, recoverGuarded)
			visitBlock(s.Body, recoverGuarded)
		case *ast.RangeStmt:
			visitExpr(s.X, recoverGuarded)
			visitBlock(s.Body, recoverGuarded)
		case *ast.SwitchStmt:
			visitStmt(s.Init, recoverGuarded)
			visitExpr(s.Tag, recoverGuarded)
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					visitBlock(&ast.BlockStmt{List: cc.Body}, recoverGuarded)
				}
			}
		case *ast.TypeSwitchStmt:
			visitStmt(s.Init, recoverGuarded)
			visitStmt(s.Assign, recoverGuarded)
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					visitBlock(&ast.BlockStmt{List: cc.Body}, recoverGuarded)
				}
			}
		case *ast.SelectStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CommClause); ok {
					visitStmt(cc.Comm, recoverGuarded)
					visitBlock(&ast.BlockStmt{List: cc.Body}, recoverGuarded)
				}
			}
		case *ast.ExprStmt:
			visitExpr(s.X, recoverGuarded)
		case *ast.DeferStmt:
			visitExpr(s.Call, recoverGuarded)
		case *ast.GoStmt:
			visitExpr(s.Call, recoverGuarded)
		case *ast.AssignStmt:
			for _, expr := range s.Rhs {
				visitExpr(expr, recoverGuarded)
			}
		case *ast.DeclStmt:
			if gen, ok := s.Decl.(*ast.GenDecl); ok {
				for _, spec := range gen.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, expr := range vs.Values {
							visitExpr(expr, recoverGuarded)
						}
					}
				}
			}
		case *ast.ReturnStmt:
			for _, expr := range s.Results {
				visitExpr(expr, recoverGuarded)
			}
		}
	}

	visitBlock(block, false)
	return foundUnlock && !foundUnguardedUnlock
}

// containsRecoverCall reports whether node contains a recover() call, ignoring
// nested function literals that execute in a different frame.
func containsRecoverCall(node ast.Node) bool {
	if node == nil {
		return false
	}
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		if fnlit, ok := n.(*ast.FuncLit); ok && fnlit != node {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if ok && ident.Name == "recover" {
			found = true
			return false
		}
		return true
	})
	return found
}

// containsUnlock checks if a block contains an unlock call for a specific mutex
func (ma *Checker) containsUnlock(block *ast.BlockStmt, mutexName string) bool {
	return ma.containsMutexMethodCall(block, mutexName, "Unlock")
}

// containsLock checks if a block contains a lock call for a specific mutex
func (ma *Checker) containsLock(block *ast.BlockStmt, mutexName string) bool {
	return ma.containsMutexMethodCall(block, mutexName, "Lock")
}

// containsRUnlock checks if a block contains an runlock call for a specific rwmutex
func (ma *Checker) containsRUnlock(block *ast.BlockStmt, mutexName string) bool {
	return ma.containsMutexMethodCall(block, mutexName, "RUnlock")
}

// containsRLock checks if a block contains an rlock call for a specific rwmutex
func (ma *Checker) containsRLock(block *ast.BlockStmt, mutexName string) bool {
	return ma.containsMutexMethodCall(block, mutexName, "RLock")
}

// containsMutexMethodCall checks if a block contains a call to a specific
// method on the given mutex variable.
func (ma *Checker) containsMutexMethodCall(block *ast.BlockStmt, mutexName, method string) bool {
	var found bool
	ast.Inspect(block, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || ma.commentFilter.ShouldSkipCall(call) {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		if sel.Sel.Name == method &&
			common.GetVarName(sel.X) == mutexName {
			found = true
		}

		return !found
	})
	return found
}
