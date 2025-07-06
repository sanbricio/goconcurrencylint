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

func isMutex(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "sync" && named.Obj().Name() == "Mutex"
}

func isRWMutex(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "sync" && named.Obj().Name() == "RWMutex"
}

// isWaitGroup returns true if the given type is sync.WaitGroup or *sync.WaitGroup.
func isWaitGroup(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "sync" && named.Obj().Name() == "WaitGroup"
}

// getVarName returns the variable name from an ast.Expr, or "?" if not an identifier.
func getVarName(expr ast.Expr) string {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return "?"
}

type mutexStats struct {
	lock, rlock       int
	lockPos, rlockPos []token.Pos
}

type errorCollector struct {
	badDeferUnlock  map[string]bool
	badDeferRUnlock map[string]bool
}

func analyzeBlock(pass *analysis.Pass, block *ast.BlockStmt, muNames map[string]bool, rwNames map[string]bool, errs *errorCollector) map[string]*mutexStats {
	stats := map[string]*mutexStats{}
	for mu := range muNames {
		stats[mu] = &mutexStats{}
	}
	for mu := range rwNames {
		stats[mu] = &mutexStats{}
	}
	for _, stmt := range block.List {
		switch s := stmt.(type) {
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					varName := getVarName(sel.X)
					typ := pass.TypesInfo.TypeOf(sel.X)
					if muNames[varName] && isMutex(typ) {
						switch sel.Sel.Name {
						case "Lock":
							stats[varName].lock++
							stats[varName].lockPos = append(stats[varName].lockPos, call.Pos())
						case "Unlock":
							if stats[varName].lock == 0 {
								pass.Reportf(call.Pos(), "mutex '%s' is unlocked but not locked", varName)
							} else {
								stats[varName].lock--
								// Remove the first lock position (FIFO for proper pairing)
								if len(stats[varName].lockPos) > 0 {
									stats[varName].lockPos = stats[varName].lockPos[1:]
								}
							}
						}
					}
					if rwNames[varName] && isRWMutex(typ) {
						switch sel.Sel.Name {
						case "Lock":
							stats[varName].lock++
							stats[varName].lockPos = append(stats[varName].lockPos, call.Pos())
						case "Unlock":
							if stats[varName].lock == 0 {
								pass.Reportf(call.Pos(), "rwmutex '%s' is unlocked but not locked", varName)
							} else {
								stats[varName].lock--
								if len(stats[varName].lockPos) > 0 {
									stats[varName].lockPos = stats[varName].lockPos[1:]
								}
							}
						case "RLock":
							stats[varName].rlock++
							stats[varName].rlockPos = append(stats[varName].rlockPos, call.Pos())
						case "RUnlock":
							if stats[varName].rlock == 0 {
								pass.Reportf(call.Pos(), "rwmutex '%s' is runlocked but not rlocked", varName)
							} else {
								stats[varName].rlock--
								if len(stats[varName].rlockPos) > 0 {
									stats[varName].rlockPos = stats[varName].rlockPos[1:]
								}
							}
						}
					}
				}
			}
		case *ast.DeferStmt:
			if call, ok := s.Call.Fun.(*ast.SelectorExpr); ok {
				varName := getVarName(call.X)
				typ := pass.TypesInfo.TypeOf(call.X)
				if muNames[varName] && isMutex(typ) && call.Sel.Name == "Unlock" {
					if stats[varName].lock == 0 {
						pass.Reportf(s.Pos(), "mutex '%s' has defer unlock but no corresponding lock", varName)
						if errs != nil {
							errs.badDeferUnlock[varName] = true
						}
					} else {
						stats[varName].lock--
						if len(stats[varName].lockPos) > 0 {
							stats[varName].lockPos = stats[varName].lockPos[1:]
						}
					}
				}
				if rwNames[varName] && isRWMutex(typ) {
					if call.Sel.Name == "Unlock" {
						if stats[varName].lock == 0 {
							pass.Reportf(s.Pos(), "rwmutex '%s' has defer unlock but no corresponding lock", varName)
							if errs != nil {
								errs.badDeferUnlock[varName] = true
							}
						} else {
							stats[varName].lock--
							if len(stats[varName].lockPos) > 0 {
								stats[varName].lockPos = stats[varName].lockPos[1:]
							}
						}
					}
					if call.Sel.Name == "RUnlock" {
						if stats[varName].rlock == 0 {
							pass.Reportf(s.Pos(), "rwmutex '%s' has defer runlock but no corresponding rlock", varName)
							if errs != nil {
								errs.badDeferRUnlock[varName] = true
							}
						} else {
							stats[varName].rlock--
							if len(stats[varName].rlockPos) > 0 {
								stats[varName].rlockPos = stats[varName].rlockPos[1:]
							}
						}
					}
				}
			}
			if fnlit, ok := s.Call.Fun.(*ast.FuncLit); ok {
				for muName := range muNames {
					unlockedInside := false
					ast.Inspect(fnlit.Body, func(n ast.Node) bool {
						if call, ok := n.(*ast.CallExpr); ok {
							if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
								if isMutex(pass.TypesInfo.TypeOf(sel.X)) && sel.Sel.Name == "Unlock" && getVarName(sel.X) == muName {
									unlockedInside = true
									return false
								}
							}
						}
						return true
					})
					if unlockedInside {
						if stats[muName].lock == 0 {
							pass.Reportf(s.Pos(), "mutex '%s' has defer unlock but no corresponding lock", muName)
							if errs != nil {
								errs.badDeferUnlock[muName] = true
							}
						} else {
							stats[muName].lock--
							if len(stats[muName].lockPos) > 0 {
								stats[muName].lockPos = stats[muName].lockPos[1:]
							}
						}
					}
				}
				for muName := range rwNames {
					unlockedInside := false
					runlockedInside := false
					ast.Inspect(fnlit.Body, func(n ast.Node) bool {
						if call, ok := n.(*ast.CallExpr); ok {
							if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
								if isRWMutex(pass.TypesInfo.TypeOf(sel.X)) {
									if sel.Sel.Name == "Unlock" && getVarName(sel.X) == muName {
										unlockedInside = true
									}
									if sel.Sel.Name == "RUnlock" && getVarName(sel.X) == muName {
										runlockedInside = true
									}
								}
							}
						}
						return true
					})
					if unlockedInside {
						if stats[muName].lock == 0 {
							pass.Reportf(s.Pos(), "rwmutex '%s' has defer unlock but no corresponding lock", muName)
							if errs != nil {
								errs.badDeferUnlock[muName] = true
							}
						} else {
							stats[muName].lock--
							if len(stats[muName].lockPos) > 0 {
								stats[muName].lockPos = stats[muName].lockPos[1:]
							}
						}
					}
					if runlockedInside {
						if stats[muName].rlock == 0 {
							pass.Reportf(s.Pos(), "rwmutex '%s' has defer runlock but no corresponding rlock", muName)
							if errs != nil {
								errs.badDeferRUnlock[muName] = true
							}
						} else {
							stats[muName].rlock--
							if len(stats[muName].rlockPos) > 0 {
								stats[muName].rlockPos = stats[muName].rlockPos[1:]
							}
						}
					}
				}
			}
		case *ast.IfStmt:
			thenStats := analyzeBlock(pass, s.Body, muNames, rwNames, errs)
			elseStats := map[string]*mutexStats{}
			if s.Else != nil {
				switch b := s.Else.(type) {
				case *ast.BlockStmt:
					elseStats = analyzeBlock(pass, b, muNames, rwNames, errs)
				case *ast.IfStmt:
					elseStats = analyzeBlock(pass, &ast.BlockStmt{List: []ast.Stmt{b}}, muNames, rwNames, errs)
				default:
					elseStats = map[string]*mutexStats{}
				}
			}
			// Report errors for locks that are not unlocked within branches
			for mu := range muNames {
				if thenStats[mu].lock > 0 {
					for _, pos := range thenStats[mu].lockPos {
						pass.Reportf(pos, "mutex '%s' is locked but not unlocked", mu)
					}
				}
				if elseStats[mu] != nil && elseStats[mu].lock > 0 {
					for _, pos := range elseStats[mu].lockPos {
						pass.Reportf(pos, "mutex '%s' is locked but not unlocked", mu)
					}
				}
			}
			for mu := range rwNames {
				if thenStats[mu].lock > 0 {
					for _, pos := range thenStats[mu].lockPos {
						pass.Reportf(pos, "rwmutex '%s' is locked but not unlocked", mu)
					}
				}
				if thenStats[mu].rlock > 0 {
					for _, pos := range thenStats[mu].rlockPos {
						pass.Reportf(pos, "rwmutex '%s' is rlocked but not runlocked", mu)
					}
				}
				if elseStats[mu] != nil && elseStats[mu].lock > 0 {
					for _, pos := range elseStats[mu].lockPos {
						pass.Reportf(pos, "rwmutex '%s' is locked but not unlocked", mu)
					}
				}
				if elseStats[mu] != nil && elseStats[mu].rlock > 0 {
					for _, pos := range elseStats[mu].rlockPos {
						pass.Reportf(pos, "rwmutex '%s' is rlocked but not runlocked", mu)
					}
				}
			}
		case *ast.GoStmt:
			if fnLit, ok := s.Call.Fun.(*ast.FuncLit); ok {
				goStats := analyzeBlock(pass, fnLit.Body, muNames, rwNames, errs)
				// Report errors for locks that are not unlocked within goroutines
				for mu := range muNames {
					if goStats[mu].lock > 0 {
						for _, pos := range goStats[mu].lockPos {
							pass.Reportf(pos, "mutex '%s' is locked but not unlocked", mu)
						}
					}
				}
				for mu := range rwNames {
					if goStats[mu].lock > 0 {
						for _, pos := range goStats[mu].lockPos {
							pass.Reportf(pos, "rwmutex '%s' is locked but not unlocked", mu)
						}
					}
					if goStats[mu].rlock > 0 {
						for _, pos := range goStats[mu].rlockPos {
							pass.Reportf(pos, "rwmutex '%s' is rlocked but not runlocked", mu)
						}
					}
				}
			}
		case *ast.ForStmt:
			forStats := analyzeBlock(pass, s.Body, muNames, rwNames, errs)
			// For loops, we don't report errors here as they could be intentional
			// (e.g., locking in each iteration)
			// But we can merge the stats back to the parent scope
			for mu := range muNames {
				stats[mu].lock += forStats[mu].lock
				stats[mu].lockPos = append(stats[mu].lockPos, forStats[mu].lockPos...)
			}
			for mu := range rwNames {
				stats[mu].lock += forStats[mu].lock
				stats[mu].rlock += forStats[mu].rlock
				stats[mu].lockPos = append(stats[mu].lockPos, forStats[mu].lockPos...)
				stats[mu].rlockPos = append(stats[mu].rlockPos, forStats[mu].rlockPos...)
			}
		case *ast.BlockStmt:
			blockStats := analyzeBlock(pass, s, muNames, rwNames, errs)
			// Merge block stats back to parent scope
			for mu := range muNames {
				stats[mu].lock += blockStats[mu].lock
				stats[mu].lockPos = append(stats[mu].lockPos, blockStats[mu].lockPos...)
			}
			for mu := range rwNames {
				stats[mu].lock += blockStats[mu].lock
				stats[mu].rlock += blockStats[mu].rlock
				stats[mu].lockPos = append(stats[mu].lockPos, blockStats[mu].lockPos...)
				stats[mu].rlockPos = append(stats[mu].rlockPos, blockStats[mu].rlockPos...)
			}
		}
	}
	return stats
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
		if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
			if ident, ok := call.X.(*ast.Ident); ok && call.Sel.Name == "Done" && ident.Name == wgName {
				found = true
				return false
			}
		}
		if callExpr, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok {
			ast.Inspect(callExpr.Body, func(n ast.Node) bool {
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
		}
		return true
	})
	return found
}

func run(pass *analysis.Pass) (interface{}, error) {
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			muNames := map[string]bool{}
			rwNames := map[string]bool{}
			wgNames := map[string]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				vs, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				for _, name := range vs.Names {
					typ := pass.TypesInfo.TypeOf(vs.Type)
					if typ == nil && len(vs.Values) > 0 {
						typ = pass.TypesInfo.TypeOf(vs.Values[0])
					}
					if typ == nil {
						continue
					}
					if isMutex(typ) {
						muNames[name.Name] = true
					}
					if isRWMutex(typ) {
						rwNames[name.Name] = true
					}
					if isWaitGroup(typ) {
						wgNames[name.Name] = true
					}
				}
				return true
			})

			errs := &errorCollector{
				badDeferUnlock:  map[string]bool{},
				badDeferRUnlock: map[string]bool{},
			}
			blockStats := analyzeBlock(pass, fn.Body, muNames, rwNames, errs)
			for mu, st := range blockStats {
				if errs.badDeferUnlock[mu] {
					continue
				}
				if errs.badDeferRUnlock[mu] {
					continue
				}
				// Report all remaining unmatched locks
				// The remaining lockPos slice contains the positions of locks that don't have unlocks
				for _, pos := range st.lockPos {
					if rwNames[mu] {
						pass.Reportf(pos, "rwmutex '%s' is locked but not unlocked", mu)
					} else {
						pass.Reportf(pos, "mutex '%s' is locked but not unlocked", mu)
					}
				}
				// Report all remaining unmatched rlocks
				for _, pos := range st.rlockPos {
					pass.Reportf(pos, "rwmutex '%s' is rlocked but not runlocked", mu)
				}
			}

			wgAdds := map[string][]token.Pos{}
			wgDones := map[string]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				typ := pass.TypesInfo.TypeOf(sel.X)
				wgName := getVarName(sel.X)
				if isWaitGroup(typ) && sel.Sel.Name == "Add" {
					wgAdds[wgName] = append(wgAdds[wgName], call.Pos())
				}
				if isWaitGroup(typ) && sel.Sel.Name == "Done" {
					wgDones[wgName] = true
				}
				return true
			})
			for wgName, positions := range wgAdds {
				hasDone := wgDones[wgName] || hasDeferDone(fn, wgName)
				if !hasDone {
					for _, pos := range positions {
						pass.Reportf(pos, "waitgroup '%s' has Add without corresponding Done", wgName)
					}
				}
			}
		}
	}
	return nil, nil
}
