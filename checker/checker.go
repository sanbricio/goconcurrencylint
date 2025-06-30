// Package checker implements a linter for detecting common mistakes
// in the use of sync.Mutex and sync.WaitGroup.
//
// Copyright (c) 2025 Santiago Bricio
// License: MIT
//
// Author: Santiago Bricio (sanbriciorojas11@gmail.com)

package checker

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
)

// Analyzer is the main entry point for the goconcurrentlint linter.
// It detects common mistakes in the use of sync.Mutex and sync.WaitGroup:
// - Locks without unlocks
// - Add without Done
var Analyzer = &analysis.Analyzer{
	Name: "goconcurrentlint",
	Doc:  "Detects common mistakes in the use of sync.Mutex and sync.WaitGroup: locks without unlock and Add without Done.",
	Run:  run,
}

// isMutex returns true if the given type is sync.Mutex or *sync.Mutex.
func isMutex(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "sync" && named.Obj().Name() == "Mutex"
}

// isWaitGroup returns true if the given type is sync.WaitGroup or *sync.WaitGroup.
func isWaitGroup(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "sync" && named.Obj().Name() == "WaitGroup"
}

// getVarName returns the variable name from an ast.Expr, or "?" if not an identifier.
func getVarName(expr ast.Expr) string {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return "?"
}

// sameExpr returns true if two expressions refer to the same variable and type.
func sameExpr(pass *analysis.Pass, a, b ast.Expr) bool {
	ta := pass.TypesInfo.Types[a]
	tb := pass.TypesInfo.Types[b]
	if idA, ok := a.(*ast.Ident); ok {
		if idB, ok := b.(*ast.Ident); ok {
			return idA.Name == idB.Name && ta.Type == tb.Type
		}
	}
	return ta.Type == tb.Type
}

// hasCallInNode checks if a call to methodName on varName exists anywhere in the AST node.
func hasCallInNode(node ast.Node, varName, methodName string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == methodName && getVarName(sel.X) == varName {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// hasDeferDone checks if a function defers wg.Done() or an equivalent call inside a deferred function literal.
func hasDeferDone(fn *ast.FuncDecl, wgName string) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		deferStmt, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		// Case 1: defer wg.Done()
		if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
			if ident, ok := call.X.(*ast.Ident); ok {
				if call.Sel.Name == "Done" && ident.Name == wgName {
					found = true
					return false
				}
			}
		}
		// Case 2: defer func() { ... wg.Done() ... }()
		if callExpr, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok {
			if hasCallInNode(callExpr.Body, wgName, "Done") {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// isAfterUnreachableCode returns true if the given position is after a panic or return statement in the block.
func isAfterUnreachableCode(body *ast.BlockStmt, pos token.Pos) bool {
	for _, stmt := range body.List {
		if stmt.End() >= pos {
			break
		}
		if exprStmt, ok := stmt.(*ast.ExprStmt); ok {
			if call, ok := exprStmt.X.(*ast.CallExpr); ok {
				if ident, ok := call.Fun.(*ast.Ident); ok {
					if ident.Name == "panic" {
						return true
					}
				}
			}
		}
		if _, ok := stmt.(*ast.ReturnStmt); ok {
			return true
		}
	}
	return false
}

// checkUnlockWithoutLock reports unlocks (direct or deferred) for a mutex that is never locked.
func checkUnlockWithoutLock(pass *analysis.Pass, fn *ast.FuncDecl) {
	mutexNames := make(map[string]bool)
	// Collect all mutex variable names used in the function
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if isMutex(pass.TypesInfo.TypeOf(sel.X)) {
					muName := getVarName(sel.X)
					mutexNames[muName] = true
				}
			}
		}
		if deferStmt, ok := n.(*ast.DeferStmt); ok {
			if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
				if isMutex(pass.TypesInfo.TypeOf(call.X)) {
					muName := getVarName(call.X)
					mutexNames[muName] = true
				}
			}
		}
		return true
	})
	for muName := range mutexNames {
		var deferUnlockPos token.Pos
		foundReachableLock := false
		var lockCount int
		// Count all Lock calls for this mutex
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if isMutex(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Lock" && getVarName(sel.X) == muName {
						lockCount++
					}
				}
			}
			return true
		})
		// Report direct unlocks without prior lock
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if exprStmt, ok := n.(*ast.ExprStmt); ok {
				if call, ok := exprStmt.X.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						if isMutex(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Unlock" && getVarName(sel.X) == muName {
							if lockCount == 0 {
								pass.Reportf(exprStmt.Pos(), "mutex '%s' is unlocked but not locked", muName)
							}
						}
					}
				}
			}
			return true
		})
		// Check for defer unlocks
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if deferStmt, ok := n.(*ast.DeferStmt); ok {
				if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
					if isMutex(pass.TypesInfo.TypeOf(call.X)) && call.Sel.Name == "Unlock" && getVarName(call.X) == muName {
						deferUnlockPos = deferStmt.Pos()
					}
				}
				if callExpr, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok {
					if hasCallInNode(callExpr.Body, muName, "Unlock") {
						deferUnlockPos = deferStmt.Pos()
					}
				}
			}
			return true
		})
		// Check for reachable locks if defer unlock exists
		if deferUnlockPos != 0 {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if exprStmt, ok := n.(*ast.ExprStmt); ok {
					if call, ok := exprStmt.X.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
							if isMutex(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Lock" && getVarName(sel.X) == muName {
								if !isAfterUnreachableCode(fn.Body, exprStmt.Pos()) {
									foundReachableLock = true
								}
							}
						}
					}
				}
				return true
			})
			if !foundReachableLock {
				pass.Reportf(deferUnlockPos, "mutex '%s' has defer unlock but no corresponding lock", muName)
			}
		}
	}
}

// checkUnlocksWithoutLocksInBlock analyzes a block and reports unlocks without locks for each mutex.
func checkUnlocksWithoutLocksInBlock(pass *analysis.Pass, block *ast.BlockStmt) {
	mutexLocks := make(map[string]int)
	ast.Inspect(block, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if isMutex(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Lock" {
					muName := getVarName(sel.X)
					mutexLocks[muName]++
				}
			}
		}
		return true
	})
	ast.Inspect(block, func(n ast.Node) bool {
		if exprStmt, ok := n.(*ast.ExprStmt); ok {
			if call, ok := exprStmt.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if isMutex(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Unlock" {
						muName := getVarName(sel.X)
						if mutexLocks[muName] == 0 {
							pass.Reportf(exprStmt.Pos(), "mutex '%s' is unlocked but not locked", muName)
						}
					}
				}
			}
		}
		if deferStmt, ok := n.(*ast.DeferStmt); ok {
			if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
				if isMutex(pass.TypesInfo.TypeOf(call.X)) && call.Sel.Name == "Unlock" {
					muName := getVarName(call.X)
					if mutexLocks[muName] == 0 {
						pass.Reportf(deferStmt.Pos(), "mutex '%s' is unlocked but not locked", muName)
					}
				}
			}
			if callExpr, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok {
				ast.Inspect(callExpr.Body, func(inner ast.Node) bool {
					if call, ok := inner.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
							if isMutex(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Unlock" {
								muName := getVarName(sel.X)
								if mutexLocks[muName] == 0 {
									pass.Reportf(deferStmt.Pos(), "mutex '%s' is unlocked but not locked", muName)
								}
							}
						}
					}
					return true
				})
			}
		}
		return true
	})
}

// run is the main analysis function for the linter.
// It checks for common concurrency mistakes in each function declaration.
func run(pass *analysis.Pass) (interface{}, error) {
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var mutexLocks, mutexUnlocks []ast.Expr
			var wgAdds, wgDones []ast.Expr
			reportedLocks := map[token.Pos]bool{}
			// Collect all Lock, Unlock, Add, Done calls
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if isMutex(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Lock" {
					mutexLocks = append(mutexLocks, sel.X)
				}
				if isMutex(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Unlock" {
					mutexUnlocks = append(mutexUnlocks, sel.X)
				}
				if isWaitGroup(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Add" {
					wgAdds = append(wgAdds, sel.X)
				}
				if isWaitGroup(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Done" {
					wgDones = append(wgDones, sel.X)
				}
				return true
			})
			// Check for unlock without lock
			checkUnlockWithoutLock(pass, fn)
			// MUTEX: Analyze Lock/Unlock balance
			mutexBalance := make(map[string][]token.Pos) // mutex name -> lock positions
			// Count all locks
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				exprStmt, ok := n.(*ast.ExprStmt)
				if !ok {
					return true
				}
				call, ok := exprStmt.X.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if isMutex(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Lock" {
					muName := getVarName(sel.X)
					mutexBalance[muName] = append(mutexBalance[muName], exprStmt.Pos())
				}
				return true
			})
			// Count all unlocks (direct and defer)
			for muName, lockPositions := range mutexBalance {
				unlockCount := 0
				// Direct unlocks
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					exprStmt, ok := n.(*ast.ExprStmt)
					if !ok {
						return true
					}
					call, ok := exprStmt.X.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if sel.Sel.Name == "Unlock" && getVarName(sel.X) == muName {
						unlockCount++
					}
					return true
				})
				// Defer unlocks
				deferUnlockCount := 0
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					deferStmt, ok := n.(*ast.DeferStmt)
					if !ok {
						return true
					}
					// Direct defer mu.Unlock()
					if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
						if call.Sel.Name == "Unlock" && getVarName(call.X) == muName {
							deferUnlockCount++
						}
					}
					// defer func() { ... mu.Unlock() ... }()
					if callExpr, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok {
						if hasCallInNode(callExpr.Body, muName, "Unlock") {
							deferUnlockCount++
						}
					}
					return true
				})
				totalUnlocks := unlockCount + deferUnlockCount
				totalLocks := len(lockPositions)
				// If there are more locks than unlocks, report the excess locks
				if totalLocks > totalUnlocks {
					unbalancedLocks := totalLocks - totalUnlocks
					for i := 0; i < unbalancedLocks; i++ {
						if i < len(lockPositions) && !reportedLocks[lockPositions[i]] {
							pass.Reportf(lockPositions[i], "mutex '%s' is locked but not unlocked", muName)
							reportedLocks[lockPositions[i]] = true
						}
					}
				}
			}
			// WAITGROUP: Analyze Add/Done
			for _, addVar := range wgAdds {
				wgName := getVarName(addVar)
				addPos := addVar.Pos()
				hasDone := false
				for _, doneVar := range wgDones {
					if sameExpr(pass, addVar, doneVar) {
						hasDone = true
						break
					}
				}
				if !hasDone && hasDeferDone(fn, wgName) {
					hasDone = true
				}
				if !hasDone {
					pass.Reportf(addPos, "waitgroup '%s' has Add without corresponding Done", wgName)
				}
			}
			// MUTEX: Check special cases in if/else branches
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if ifStmt, ok := n.(*ast.IfStmt); ok && ifStmt.Else != nil {
					checkUnlocksWithoutLocksInBlock(pass, ifStmt.Body)
					if elseBlock, ok := ifStmt.Else.(*ast.BlockStmt); ok {
						checkUnlocksWithoutLocksInBlock(pass, elseBlock)
					}
				}
				return true
			})
		}
	}
	return nil, nil
}
