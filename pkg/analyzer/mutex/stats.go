package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
)

// analyzeBlock analyzes a block statement starting from the provided state and
// returns the resulting stats after executing that block.
func (ma *Analyzer) analyzeBlock(block *ast.BlockStmt, initial map[string]*Stats) map[string]*Stats {
	blockStats := ma.copyStats(initial)

	for _, stmt := range block.List {
		if ma.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		ma.analyzeStatement(stmt, blockStats)
	}

	return blockStats
}

// analyzeStatement analyzes individual statements
func (ma *Analyzer) analyzeStatement(stmt ast.Stmt, stats map[string]*Stats) {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		ma.analyzeExpressionStatement(s, stats)
	case *ast.DeferStmt:
		ma.analyzeDeferStatement(s, stats)
	case *ast.ReturnStmt:
		ma.analyzeReturnStatement(s, stats)
	case *ast.IfStmt:
		ma.analyzeIfStatement(s, stats)
	case *ast.GoStmt:
		ma.analyzeGoStatement(s)
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
		ma.analyzeStatement(s.Stmt, stats)
	case *ast.BlockStmt:
		nestedStats := ma.analyzeBlock(s, stats)
		ma.replaceStats(stats, nestedStats)
	}
}

// analyzeExpressionStatement handles expression statements (Lock/Unlock calls)
func (ma *Analyzer) analyzeExpressionStatement(stmt *ast.ExprStmt, stats map[string]*Stats) {
	call, ok := stmt.X.(*ast.CallExpr)
	if !ok {
		return
	}

	if ma.commentFilter.ShouldSkipCall(call) {
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

	if ma.mutexNames[varName] {
		ma.handleMutexCall(varName, sel.Sel.Name, call.Pos(), stats)
	}

	if ma.rwMutexNames[varName] {
		ma.handleRWMutexCall(varName, sel.Sel.Name, call.Pos(), stats)
	}
}

// handleMutexCall processes mutex method calls
func (ma *Analyzer) handleMutexCall(varName, methodName string, pos token.Pos, stats map[string]*Stats) {
	if ma.isBorrowedWrapperCall(varName, methodName) {
		return
	}

	switch methodName {
	case "Lock", "TryLock":
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			ma.removeFirstBorrowedUnlockPos(stats[varName])
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "Unlock":
		if stats[varName].lock == 0 {
			stats[varName].borrowedLock++
			stats[varName].borrowedUnlockPos = append(stats[varName].borrowedUnlockPos, pos)
		} else {
			stats[varName].lock--
			ma.removeFirstLockPos(stats[varName])
		}
	}
}

// handleRWMutexCall processes rwmutex method calls
func (ma *Analyzer) handleRWMutexCall(varName, methodName string, pos token.Pos, stats map[string]*Stats) {
	if ma.isBorrowedWrapperCall(varName, methodName) {
		return
	}

	switch methodName {
	case "Lock", "TryLock":
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			ma.removeFirstBorrowedUnlockPos(stats[varName])
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "Unlock":
		if stats[varName].lock == 0 {
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
		if stats[varName].rlock == 0 {
			stats[varName].borrowedRLock++
			stats[varName].borrowedRUnlockPos = append(stats[varName].borrowedRUnlockPos, pos)
		} else {
			stats[varName].rlock--
			ma.removeFirstRLockPos(stats[varName])
		}
	}
}

func (ma *Analyzer) analyzeReturnStatement(stmt *ast.ReturnStmt, stats map[string]*Stats) {
	for _, result := range stmt.Results {
		fnlit, ok := result.(*ast.FuncLit)
		if !ok {
			continue
		}
		ma.handleDeferFunctionLiteral(fnlit, stmt.Pos(), stats)
	}
}

// analyzeDeferStatement handles defer statements
func (ma *Analyzer) analyzeDeferStatement(stmt *ast.DeferStmt, stats map[string]*Stats) {
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
func (ma *Analyzer) handleDeferCall(call *ast.SelectorExpr, pos token.Pos, stats map[string]*Stats) {
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

// handleDeferFunctionLiteral processes defer with function literals
func (ma *Analyzer) handleDeferFunctionLiteral(fnlit *ast.FuncLit, pos token.Pos, stats map[string]*Stats) {
	// Check for mutex unlocks in function literal
	for mutexName := range ma.mutexNames {
		if ma.containsUnlock(fnlit.Body, mutexName) && !ma.containsLock(fnlit.Body, mutexName) {
			ma.handleDeferUnlock(mutexName, pos, stats, false)
		}
	}

	// Check for rwmutex unlocks in function literal
	for rwMutexName := range ma.rwMutexNames {
		if ma.containsUnlock(fnlit.Body, rwMutexName) && !ma.containsLock(fnlit.Body, rwMutexName) {
			ma.handleDeferUnlock(rwMutexName, pos, stats, true)
		}
		if ma.containsRUnlock(fnlit.Body, rwMutexName) && !ma.containsRLock(fnlit.Body, rwMutexName) {
			ma.handleDeferRUnlock(rwMutexName, pos, stats)
		}
	}
}

// containsUnlock checks if a block contains an unlock call for a specific mutex
func (ma *Analyzer) containsUnlock(block *ast.BlockStmt, mutexName string) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
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

// containsLock checks if a block contains a lock call for a specific mutex
func (ma *Analyzer) containsLock(block *ast.BlockStmt, mutexName string) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if ma.commentFilter.ShouldSkipCall(call) {
				return true
			}

			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "Lock" && common.GetVarName(sel.X) == mutexName {
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
func (ma *Analyzer) containsRUnlock(block *ast.BlockStmt, mutexName string) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
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

// containsRLock checks if a block contains an rlock call for a specific rwmutex
func (ma *Analyzer) containsRLock(block *ast.BlockStmt, mutexName string) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if ma.commentFilter.ShouldSkipCall(call) {
				return true
			}

			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "RLock" && common.GetVarName(sel.X) == mutexName {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}
