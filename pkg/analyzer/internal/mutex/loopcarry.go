package mutex

import (
	"go/ast"
	"go/token"
	"maps"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// loopCarryAnalyzer detects mutex usage patterns that cross loop iteration
// boundaries: deferred unlocks inside loops and locks that are "carried" past
// an iteration via break or continue.
//
// It is config-only (no per-function mutable state) so a single instance may
// be shared between a Checker and all its simulation forks.
type loopCarryAnalyzer struct {
	mutexNames     map[string]bool
	rwMutexNames   map[string]bool
	commentFilter  *commentfilter.CommentFilter
	errorCollector report.Reporter
	termination    *terminationAnalyzer
}

// newLoopCarryAnalyzer creates a loopCarryAnalyzer with the given dependencies.
func newLoopCarryAnalyzer(
	mutexNames, rwMutexNames map[string]bool,
	cf *commentfilter.CommentFilter,
	ec report.Reporter,
	term *terminationAnalyzer,
) *loopCarryAnalyzer {
	return &loopCarryAnalyzer{
		mutexNames:     mutexNames,
		rwMutexNames:   rwMutexNames,
		commentFilter:  cf,
		errorCollector: ec,
		termination:    term,
	}
}

func (lc *loopCarryAnalyzer) reportDeferredUnlocksInLoop(body *ast.BlockStmt) {
	if body == nil {
		return
	}

	locked := make(map[string]bool)
	rlocked := make(map[string]bool)
	lc.reportDeferredUnlocksInLoopStatements(body.List, locked, rlocked)
}

func (lc *loopCarryAnalyzer) reportDeferredUnlocksInLoopStatements(stmts []ast.Stmt, locked, rlocked map[string]bool) {
	for i, stmt := range stmts {
		if lc.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || lc.commentFilter.ShouldSkipCall(call) {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			varName := common.GetVarName(sel.X)
			switch sel.Sel.Name {
			case "Lock":
				if lc.mutexNames[varName] || lc.rwMutexNames[varName] {
					locked[varName] = true
				}
			case "RLock":
				if lc.rwMutexNames[varName] {
					rlocked[varName] = true
				}
			case "Unlock":
				delete(locked, varName)
			case "RUnlock":
				delete(rlocked, varName)
			}

		case *ast.DeferStmt:
			if lc.commentFilter.ShouldSkipCall(s.Call) {
				continue
			}
			if lc.termination.blockAlwaysTerminates(&ast.BlockStmt{List: stmts[i+1:]}) {
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
					if lc.rwMutexNames[varName] {
						mutexType = "rwmutex"
					}
					lc.errorCollector.AddError(s.Pos(), category.DeferUnlockInLoop, mutexType+" '"+varName+"' defers unlock inside loop")
				}
			case "RUnlock":
				if rlocked[varName] {
					lc.errorCollector.AddError(s.Pos(), category.DeferUnlockInLoop, "rwmutex '"+varName+"' defers runlock inside loop")
				}
			}

		case *ast.BlockStmt:
			lc.reportDeferredUnlocksInLoopStatements(s.List, maps.Clone(locked), maps.Clone(rlocked))
		case *ast.LabeledStmt:
			lc.reportDeferredUnlocksInLoopStatements([]ast.Stmt{s.Stmt}, maps.Clone(locked), maps.Clone(rlocked))
		case *ast.IfStmt:
			lc.reportDeferredUnlocksInLoopStatements(s.Body.List, maps.Clone(locked), maps.Clone(rlocked))
			if s.Else != nil {
				lc.reportDeferredUnlocksInLoopElse(s.Else, maps.Clone(locked), maps.Clone(rlocked))
			}
		case *ast.ForStmt:
			lc.reportDeferredUnlocksInLoopStatements(s.Body.List, maps.Clone(locked), maps.Clone(rlocked))
		case *ast.RangeStmt:
			lc.reportDeferredUnlocksInLoopStatements(s.Body.List, maps.Clone(locked), maps.Clone(rlocked))
		case *ast.SwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					lc.reportDeferredUnlocksInLoopStatements(cc.Body, maps.Clone(locked), maps.Clone(rlocked))
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					lc.reportDeferredUnlocksInLoopStatements(cc.Body, maps.Clone(locked), maps.Clone(rlocked))
				}
			}
		case *ast.SelectStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CommClause); ok {
					lc.reportDeferredUnlocksInLoopStatements(cc.Body, maps.Clone(locked), maps.Clone(rlocked))
				}
			}
		}
	}
}

func (lc *loopCarryAnalyzer) reportDeferredUnlocksInLoopElse(stmt ast.Stmt, locked, rlocked map[string]bool) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		lc.reportDeferredUnlocksInLoopStatements(s.List, locked, rlocked)
	case *ast.IfStmt:
		lc.reportDeferredUnlocksInLoopStatements([]ast.Stmt{s}, locked, rlocked)
	}
}

func (lc *loopCarryAnalyzer) applyLoopExitLocks(stmt *ast.ForStmt, initial, final map[string]*Stats) {
	for mutexName := range lc.mutexNames {
		if pos, ok := lc.loopMayBreakHoldingMutex(stmt.Body.List, mutexName, WriteLockPattern, 0, token.NoPos); ok {
			if final[mutexName].lock <= initial[mutexName].lock {
				final[mutexName].lock++
				final[mutexName].lockPos = append(final[mutexName].lockPos, pos)
			}
		}
	}

	for rwMutexName := range lc.rwMutexNames {
		if pos, ok := lc.loopMayBreakHoldingMutex(stmt.Body.List, rwMutexName, WriteLockPattern, 0, token.NoPos); ok {
			if final[rwMutexName].lock <= initial[rwMutexName].lock {
				final[rwMutexName].lock++
				final[rwMutexName].lockPos = append(final[rwMutexName].lockPos, pos)
			}
		}
		if pos, ok := lc.loopMayBreakHoldingMutex(stmt.Body.List, rwMutexName, ReadLockPattern, 0, token.NoPos); ok {
			if final[rwMutexName].rlock <= initial[rwMutexName].rlock {
				final[rwMutexName].rlock++
				final[rwMutexName].rlockPos = append(final[rwMutexName].rlockPos, pos)
			}
		}
	}
}

func (lc *loopCarryAnalyzer) loopMayBreakHoldingMutex(stmts []ast.Stmt, varName string, pattern LockPattern, depth int, lastLockPos token.Pos) (token.Pos, bool) {
	currentDepth := depth
	currentLockPos := lastLockPos

	for _, stmt := range stmts {
		if lc.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || lc.commentFilter.ShouldSkipCall(call) {
				continue
			}

			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || common.GetVarName(sel.X) != varName {
				continue
			}

			if containsMethod(pattern.LockMethods, sel.Sel.Name) {
				currentDepth++
				currentLockPos = call.Pos()
				continue
			}

			if containsMethod(pattern.UnlockMethods, sel.Sel.Name) && currentDepth > 0 {
				currentDepth--
			}
		case *ast.IfStmt:
			if pos, ok := lc.loopMayBreakHoldingMutex(s.Body.List, varName, pattern, currentDepth, currentLockPos); ok {
				return pos, true
			}
			if s.Else != nil {
				switch elseNode := s.Else.(type) {
				case *ast.BlockStmt:
					if pos, ok := lc.loopMayBreakHoldingMutex(elseNode.List, varName, pattern, currentDepth, currentLockPos); ok {
						return pos, true
					}
				case *ast.IfStmt:
					if pos, ok := lc.loopMayBreakHoldingMutex([]ast.Stmt{elseNode}, varName, pattern, currentDepth, currentLockPos); ok {
						return pos, true
					}
				}
			}
		case *ast.BlockStmt:
			if pos, ok := lc.loopMayBreakHoldingMutex(s.List, varName, pattern, currentDepth, currentLockPos); ok {
				return pos, true
			}
		case *ast.LabeledStmt:
			if pos, ok := lc.loopMayBreakHoldingMutex([]ast.Stmt{s.Stmt}, varName, pattern, currentDepth, currentLockPos); ok {
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

// isCarriedLoopUnlock reports whether the unlock at unlockPos occurs inside a
// loop that also locks varName and may carry the lock past the iteration
// boundary (i.e. continue without unlocking). function must be the enclosing
// *ast.FuncDecl; if nil or its Body is nil the method returns false.
func (lc *loopCarryAnalyzer) isCarriedLoopUnlock(varName string, unlockPos token.Pos, function *ast.FuncDecl, pattern LockPattern) bool {
	if function == nil || function.Body == nil || unlockPos == token.NoPos {
		return false
	}

	found := false
	ast.Inspect(function.Body, func(n ast.Node) bool {
		if found {
			return false
		}

		switch loop := n.(type) {
		case *ast.ForStmt:
			if loop.End() >= unlockPos {
				return true
			}
			if lc.loopMayCarryMutexPastIteration(loop.Body.List, varName, pattern, 0) {
				found = true
				return false
			}
		case *ast.RangeStmt:
			if loop.End() >= unlockPos {
				return true
			}
			if lc.loopMayCarryMutexPastIteration(loop.Body.List, varName, pattern, 0) {
				found = true
				return false
			}
		}

		return true
	})

	return found
}

func (lc *loopCarryAnalyzer) loopMayCarryMutexPastIteration(stmts []ast.Stmt, varName string, pattern LockPattern, depth int) bool {
	currentDepth := depth

	for _, stmt := range stmts {
		if lc.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || lc.commentFilter.ShouldSkipCall(call) {
				continue
			}

			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || common.GetVarName(sel.X) != varName {
				continue
			}

			if containsMethod(pattern.LockMethods, sel.Sel.Name) {
				currentDepth++
				continue
			}
			if containsMethod(pattern.UnlockMethods, sel.Sel.Name) && currentDepth > 0 {
				currentDepth--
			}
		case *ast.IfStmt:
			if lc.loopMayCarryMutexPastIteration(s.Body.List, varName, pattern, currentDepth) {
				return true
			}
			if s.Else != nil {
				switch elseNode := s.Else.(type) {
				case *ast.BlockStmt:
					if lc.loopMayCarryMutexPastIteration(elseNode.List, varName, pattern, currentDepth) {
						return true
					}
				case *ast.IfStmt:
					if lc.loopMayCarryMutexPastIteration([]ast.Stmt{elseNode}, varName, pattern, currentDepth) {
						return true
					}
				}
			}
		case *ast.BlockStmt:
			if lc.loopMayCarryMutexPastIteration(s.List, varName, pattern, currentDepth) {
				return true
			}
		case *ast.LabeledStmt:
			if lc.loopMayCarryMutexPastIteration([]ast.Stmt{s.Stmt}, varName, pattern, currentDepth) {
				return true
			}
		case *ast.ForStmt:
			if lc.loopMayCarryMutexPastIteration(s.Body.List, varName, pattern, currentDepth) {
				return true
			}
		case *ast.RangeStmt:
			if lc.loopMayCarryMutexPastIteration(s.Body.List, varName, pattern, currentDepth) {
				return true
			}
		case *ast.SwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok &&
					lc.loopMayCarryMutexPastIteration(cc.Body, varName, pattern, currentDepth) {
					return true
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok &&
					lc.loopMayCarryMutexPastIteration(cc.Body, varName, pattern, currentDepth) {
					return true
				}
			}
		case *ast.SelectStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CommClause); ok &&
					lc.loopMayCarryMutexPastIteration(cc.Body, varName, pattern, currentDepth) {
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
