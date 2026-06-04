package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

func (c *Checker) analyzeReturnStatement(stmt *ast.ReturnStmt, stats map[string]*Stats) {
	for _, result := range stmt.Results {
		call, ok := result.(*ast.CallExpr)
		if ok && !c.commentFilter.ShouldSkipCall(call) {
			// Apply callee effects for `return helper()` forms.
			if !c.applyLocalFunctionLiteralLifecycleEffects(call, stats) {
				c.applyLocalMethodLifecycleEffects(call, stats)
			}
		}

		fnlit, ok := result.(*ast.FuncLit)
		if !ok {
			continue
		}
		c.handleDeferFunctionLiteral(fnlit, stmt.Pos(), stats)
	}

	if !c.rawBodyEffects {
		c.reportUnmatchedLocks(stats)
	}
}

// analyzeDeferStatement handles defer statements
func (c *Checker) analyzeDeferStatement(stmt *ast.DeferStmt, stats map[string]*Stats) {
	if c.commentFilter.ShouldSkipCall(stmt.Call) {
		return
	}

	// Handle direct defer calls
	if call, ok := stmt.Call.Fun.(*ast.SelectorExpr); ok {
		c.handleDeferCall(call, stmt.Pos(), stats)
		return
	}

	// Handle defer with function literals
	if fnlit, ok := stmt.Call.Fun.(*ast.FuncLit); ok {
		c.handleDeferFunctionLiteral(fnlit, stmt.Pos(), stats)
	}
}

// handleDeferCall processes direct defer calls
func (c *Checker) handleDeferCall(call *ast.SelectorExpr, pos token.Pos, stats map[string]*Stats) {
	varName := common.GetVarName(call.X)

	if call.Sel.Name == "Lock" && c.consumeBorrowedDeferredLock(varName, stats) {
		return
	}
	if call.Sel.Name == "RLock" && c.consumeBorrowedDeferredRLock(varName, stats) {
		return
	}
	if call.Sel.Name == "Lock" && c.deferredRelockBalancesEarlierDeferredUnlock(varName, stats) {
		return
	}
	if call.Sel.Name == "RLock" && c.deferredRRelockBalancesEarlierDeferredRUnlock(varName, stats) {
		return
	}

	// defer Lock / defer RLock re-acquires the lock on
	// function return instead of releasing it, guaranteed deadlock.
	if c.mutexNames[varName] && call.Sel.Name == "Lock" {
		c.errorCollector.AddError(pos, category.DeferLock, "mutex '"+varName+"' defer calls Lock instead of Unlock, will deadlock on return")
		return
	}
	if c.rwMutexNames[varName] {
		switch call.Sel.Name {
		case "Lock":
			c.errorCollector.AddError(pos, category.DeferLock, "rwmutex '"+varName+"' defer calls Lock instead of Unlock, will deadlock on return")
			return
		case "RLock":
			c.errorCollector.AddError(pos, category.DeferLock, "rwmutex '"+varName+"' defer calls RLock instead of RUnlock, will deadlock on return")
			return
		}
	}

	if c.mutexNames[varName] && call.Sel.Name == "Unlock" {
		c.handleDeferUnlock(varName, pos, stats, false)
	}

	if c.rwMutexNames[varName] {
		switch call.Sel.Name {
		case "Unlock":
			c.handleDeferUnlock(varName, pos, stats, true)
		case "RUnlock":
			c.handleDeferRUnlock(varName, pos, stats)
		}
	}
}

func (c *Checker) consumeBorrowedDeferredLock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	if st == nil || st.borrowedLock == 0 {
		return false
	}
	st.borrowedLock--
	st.removeFirstBorrowedUnlockPos()
	return true
}

func (c *Checker) consumeBorrowedDeferredRLock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	if st == nil || st.borrowedRLock == 0 {
		return false
	}
	st.borrowedRLock--
	st.removeFirstBorrowedRUnlockPos()
	return true
}

func (c *Checker) deferredRelockBalancesEarlierDeferredUnlock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	return st != nil && st.lock == 0 && st.deferUnlock > 0
}

func (c *Checker) deferredRRelockBalancesEarlierDeferredRUnlock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	return st != nil && st.rlock == 0 && st.deferRUnlock > 0
}

// handleDeferFunctionLiteral processes defer with function literals
func (c *Checker) handleDeferFunctionLiteral(fnlit *ast.FuncLit, pos token.Pos, stats map[string]*Stats) {
	guard := newRecoverGuardInspector(c.commentFilter)

	// Check for mutex unlocks in function literal
	for mutexName := range c.mutexNames {
		if guard.containsUnlock(fnlit.Body, mutexName) && !guard.containsLock(fnlit.Body, mutexName) {
			if stats[mutexName].lock == 0 && guard.unlocksOnlyInRecoverGuard(fnlit.Body, mutexName, "Unlock") {
				continue
			}
			c.handleDeferUnlock(mutexName, pos, stats, false)
		}
	}

	// Check for rwmutex unlocks in function literal
	for rwMutexName := range c.rwMutexNames {
		if guard.containsUnlock(fnlit.Body, rwMutexName) && !guard.containsLock(fnlit.Body, rwMutexName) {
			if stats[rwMutexName].lock == 0 && guard.unlocksOnlyInRecoverGuard(fnlit.Body, rwMutexName, "Unlock") {
				continue
			}
			c.handleDeferUnlock(rwMutexName, pos, stats, true)
		}
		if guard.containsRUnlock(fnlit.Body, rwMutexName) && !guard.containsRLock(fnlit.Body, rwMutexName) {
			if stats[rwMutexName].rlock == 0 && guard.unlocksOnlyInRecoverGuard(fnlit.Body, rwMutexName, "RUnlock") {
				continue
			}
			c.handleDeferRUnlock(rwMutexName, pos, stats)
		}
	}
}

// handleDeferUnlock processes defer unlock calls
func (c *Checker) handleDeferUnlock(varName string, pos token.Pos, stats map[string]*Stats, isRWMutex bool) {
	if stats[varName].lock == 0 {
		mutexType := "mutex"
		if isRWMutex {
			mutexType = "rwmutex"
		}
		c.errorCollector.AddError(pos, category.DeferUnlockWithoutLock, mutexType+" '"+varName+"' has defer unlock but no corresponding lock")
		c.deferErrors.badDeferUnlock[varName] = true
	} else {
		stats[varName].deferUnlock++
	}
}

// handleDeferRUnlock processes defer runlock calls
func (c *Checker) handleDeferRUnlock(varName string, pos token.Pos, stats map[string]*Stats) {
	if stats[varName].rlock == 0 {
		c.errorCollector.AddError(pos, category.DeferUnlockWithoutLock, "rwmutex '"+varName+"' has defer runlock but no corresponding rlock")
		c.deferErrors.badDeferRUnlock[varName] = true
	} else {
		stats[varName].deferRUnlock++
	}
}
