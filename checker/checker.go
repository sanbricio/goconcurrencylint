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

var Analyzer = &analysis.Analyzer{
	Name: "goconcurrentlint",
	Doc:  "Detects common mistakes in the use of sync.Mutex and sync.WaitGroup: locks without unlock and Add without Done.",
	Run:  run,
}

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

func getVarName(expr ast.Expr) string {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return "?"
}

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

// hasUnlockInNode busca unlock en cualquier nodo AST, incluyendo funciones anónimas
func hasUnlockInNode(node ast.Node, muName string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}

		// Direct unlock call
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "Unlock" && getVarName(sel.X) == muName {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func hasDeferUnlock(fn *ast.FuncDecl, muName string) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}

		deferStmt, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}

		// Case 1: defer mu.Unlock()
		if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
			if ident, ok := call.X.(*ast.Ident); ok {
				if call.Sel.Name == "Unlock" && ident.Name == muName {
					found = true
					return false
				}
			}
		}

		// Case 2: defer func() { ... mu.Unlock() ... }()
		if callExpr, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok {
			if hasUnlockInNode(callExpr.Body, muName) {
				found = true
				return false
			}
		}

		return true
	})
	return found
}

// hasDoneLiteInNode busca Done en cualquier nodo AST, incluyendo funciones anónimas
func hasDoneInNode(node ast.Node, wgName string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}

		// Direct Done call
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "Done" && getVarName(sel.X) == wgName {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

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
			if hasDoneInNode(callExpr.Body, wgName) {
				found = true
				return false
			}
		}

		return true
	})
	return found
}

// Busca unlock o defer unlock después de un if en el bloque superior
func unlockAfterIf(block *ast.BlockStmt, afterPos token.Pos, muName string) bool {
	for _, stmt := range block.List {
		if stmt.Pos() <= afterPos {
			continue
		}
		// Direct unlock
		exprStmt, ok := stmt.(*ast.ExprStmt)
		if ok {
			call, ok := exprStmt.X.(*ast.CallExpr)
			if ok {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "Unlock" && getVarName(sel.X) == muName {
					return true
				}
			}
		}
		// Defer unlock
		deferStmt, ok := stmt.(*ast.DeferStmt)
		if ok {
			// Direct defer mu.Unlock()
			if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
				if call.Sel.Name == "Unlock" && getVarName(call.X) == muName {
					return true
				}
			}
			// defer func() { ... mu.Unlock() ... }()
			if callExpr, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok {
				if hasUnlockInNode(callExpr.Body, muName) {
					return true
				}
			}
		}
	}
	return false
}

// Analiza if sin else, reporta lock si no unlockea ni en la rama ni fuera
func checkLocksInIfNoElse(pass *analysis.Pass, fn *ast.FuncDecl, reportedLocks map[token.Pos]bool) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok || ifStmt.Else != nil {
			return true // Solo if sin else
		}
		for i, stmt := range ifStmt.Body.List {
			exprStmt, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := exprStmt.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			typ := pass.TypesInfo.TypeOf(sel.X)
			if isMutex(typ) && sel.Sel.Name == "Lock" {
				muName := getVarName(sel.X)
				lockPos := stmt.Pos()
				foundUnlock := false
				// Busca unlock en el mismo bloque después del lock
				for _, afterStmt := range ifStmt.Body.List[i+1:] {
					// Direct unlock
					afterExpr, ok := afterStmt.(*ast.ExprStmt)
					if ok {
						afterCall, ok := afterExpr.X.(*ast.CallExpr)
						if ok {
							afterSel, ok := afterCall.Fun.(*ast.SelectorExpr)
							if ok && afterSel.Sel.Name == "Unlock" && getVarName(afterSel.X) == muName {
								foundUnlock = true
								break
							}
						}
					}
					// Defer unlock
					deferStmt, ok := afterStmt.(*ast.DeferStmt)
					if ok {
						// Direct defer mu.Unlock()
						if deferCall, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
							if deferCall.Sel.Name == "Unlock" && getVarName(deferCall.X) == muName {
								foundUnlock = true
								break
							}
						}
						// defer func() { ... mu.Unlock() ... }()
						if callExpr, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok {
							if hasUnlockInNode(callExpr.Body, muName) {
								foundUnlock = true
								break
							}
						}
					}
				}
				// Unlock global después del if o defer en la función
				if !foundUnlock && !unlockAfterIf(fn.Body, ifStmt.End(), muName) && !hasDeferUnlock(fn, muName) {
					if !reportedLocks[lockPos] {
						pass.Reportf(lockPos, "mutex '%s' is locked but not unlocked", muName)
						reportedLocks[lockPos] = true
					}
				}
			}
		}
		return true
	})
}

// Analiza if/else: ambas ramas deben unlockear el mismo mutex si lo lockean
func checkLocksInIfElse(pass *analysis.Pass, fn *ast.FuncDecl, reportedLocks map[token.Pos]bool) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok || ifStmt.Else == nil {
			return true // Solo if con else
		}
		locksInThen := findLocksInBlock(pass, ifStmt.Body)
		locksInElse := findLocksInBlock(pass, ifStmt.Else.(*ast.BlockStmt))
		// Si hay lock en alguna rama, debe unlockearse en esa rama
		for _, lockThen := range locksInThen {
			if !hasUnlockInBlock(ifStmt.Body, lockThen.varName) {
				if !reportedLocks[lockThen.pos] {
					pass.Reportf(lockThen.pos, "mutex '%s' is locked but not unlocked", lockThen.varName)
					reportedLocks[lockThen.pos] = true
				}
			}
		}
		for _, lockElse := range locksInElse {
			if !hasUnlockInBlock(ifStmt.Else.(*ast.BlockStmt), lockElse.varName) {
				if !reportedLocks[lockElse.pos] {
					pass.Reportf(lockElse.pos, "mutex '%s' is locked but not unlocked", lockElse.varName)
					reportedLocks[lockElse.pos] = true
				}
			}
		}
		return true
	})
}

type lockInfo struct {
	varName string
	pos     token.Pos
}

// Encuentra los locks en un bloque
func findLocksInBlock(pass *analysis.Pass, block *ast.BlockStmt) []lockInfo {
	var locks []lockInfo
	for _, stmt := range block.List {
		exprStmt, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := exprStmt.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		typ := pass.TypesInfo.TypeOf(sel.X)
		if isMutex(typ) && sel.Sel.Name == "Lock" {
			locks = append(locks, lockInfo{varName: getVarName(sel.X), pos: stmt.Pos()})
		}
	}
	return locks
}

// ¿Hay unlock (o defer unlock) en ese bloque para ese nombre?
func hasUnlockInBlock(block *ast.BlockStmt, name string) bool {
	for _, stmt := range block.List {
		// Direct unlock
		exprStmt, ok := stmt.(*ast.ExprStmt)
		if ok {
			call, ok := exprStmt.X.(*ast.CallExpr)
			if ok {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "Unlock" && getVarName(sel.X) == name {
					return true
				}
			}
		}
		// Defer unlock
		deferStmt, ok := stmt.(*ast.DeferStmt)
		if ok {
			// Direct defer mu.Unlock()
			if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
				if call.Sel.Name == "Unlock" && getVarName(call.X) == name {
					return true
				}
			}
			// defer func() { ... mu.Unlock() ... }()
			if callExpr, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok {
				if hasUnlockInNode(callExpr.Body, name) {
					return true
				}
			}
		}
	}
	return false
}

// Check for unlock without corresponding lock
func checkUnlockWithoutLock(pass *analysis.Pass, fn *ast.FuncDecl) {
	// Check each mutex individually
	mutexNames := make(map[string]bool)

	// First, collect all mutex names used in the function
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

	// For each mutex, check the lock/unlock pattern
	for muName := range mutexNames {
		var deferUnlockPos token.Pos
		foundReachableLock := false

		// Check for defer unlock first
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if deferStmt, ok := n.(*ast.DeferStmt); ok {
				if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
					if isMutex(pass.TypesInfo.TypeOf(call.X)) && call.Sel.Name == "Unlock" && getVarName(call.X) == muName {
						deferUnlockPos = deferStmt.Pos()
					}
				}
				// Also check defer func() { mu.Unlock() }
				if callExpr, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok {
					ast.Inspect(callExpr.Body, func(inner ast.Node) bool {
						if call, ok := inner.(*ast.CallExpr); ok {
							if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
								if isMutex(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Unlock" && getVarName(sel.X) == muName {
									deferUnlockPos = deferStmt.Pos()
								}
							}
						}
						return true
					})
				}
			}
			return true
		})

		// Check for locks and if they're reachable
		if deferUnlockPos != 0 {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if exprStmt, ok := n.(*ast.ExprStmt); ok {
					if call, ok := exprStmt.X.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
							if isMutex(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Lock" && getVarName(sel.X) == muName {
								// Check if this lock is reachable (not after panic, return, etc.)
								if !isAfterUnreachableCode(fn.Body, exprStmt.Pos()) {
									foundReachableLock = true
								}
							}
						}
					}
				}
				return true
			})

			// If there's a defer unlock but no reachable lock, report error
			if !foundReachableLock {
				pass.Reportf(deferUnlockPos, "mutex '%s' has defer unlock but no corresponding lock", muName)
			}
		}
	}
}

// Check if a position is after unreachable code (panic, return, etc.)
func isAfterUnreachableCode(body *ast.BlockStmt, pos token.Pos) bool {
	for _, stmt := range body.List {
		// If we've reached the position we're checking, stop
		if stmt.End() >= pos {
			break
		}

		// Check for panic calls
		if exprStmt, ok := stmt.(*ast.ExprStmt); ok {
			if call, ok := exprStmt.X.(*ast.CallExpr); ok {
				if ident, ok := call.Fun.(*ast.Ident); ok {
					if ident.Name == "panic" {
						return true
					}
				}
			}
		}

		// Check for return statements
		if _, ok := stmt.(*ast.ReturnStmt); ok {
			return true
		}
	}
	return false
}

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

			// Recoge todos los Lock, Unlock, Add, Done
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				// Aquí deberías recolectar los Lock, Unlock, Add, Done en los slices correspondientes
				// Por ejemplo:
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

			// Check for unlock without lock first
			checkUnlockWithoutLock(pass, fn)

			// MUTEX: Analiza Lock/Unlock general con balance
			// Cuenta locks y unlocks para verificar balance
			mutexBalance := make(map[string][]token.Pos) // mutex name -> lock positions

			// Primero, cuenta todos los locks
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
				typ := pass.TypesInfo.TypeOf(sel.X)
				if isMutex(typ) && sel.Sel.Name == "Lock" {
					muName := getVarName(sel.X)
					mutexBalance[muName] = append(mutexBalance[muName], exprStmt.Pos())
				}
				return true
			})

			// Luego, cuenta todos los unlocks (directos y defer)
			for muName, lockPositions := range mutexBalance {
				unlockCount := 0

				// Cuenta unlocks directos
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

				// Cuenta defer unlocks
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
						if hasUnlockInNode(callExpr.Body, muName) {
							deferUnlockCount++
						}
					}
					return true
				})

				totalUnlocks := unlockCount + deferUnlockCount
				totalLocks := len(lockPositions)

				// Si hay más locks que unlocks, reporta los locks excedentes
				if totalLocks > totalUnlocks {
					unbalancedLocks := totalLocks - totalUnlocks
					// Reporta los primeros locks sin unlock correspondiente
					for i := 0; i < unbalancedLocks; i++ {
						if i < len(lockPositions) && !reportedLocks[lockPositions[i]] {
							pass.Reportf(lockPositions[i], "mutex '%s' is locked but not unlocked", muName)
							reportedLocks[lockPositions[i]] = true
						}
					}
				}
			}

			// WAITGROUP: Analiza Add/Done general
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

			// MUTEX: Check special cases
			checkLocksInIfNoElse(pass, fn, reportedLocks)
			checkLocksInIfElse(pass, fn, reportedLocks)
		}
	}
	return nil, nil
}