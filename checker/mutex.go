package checker

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/concurrency-linter/checker/common"
	"github.com/sanbricio/concurrency-linter/checker/report"
)

type mutexStats struct {
	lock, rlock       int
	lockPos, rlockPos []token.Pos
}

type deferErrorCollector struct {
	badDeferUnlock  map[string]bool
	badDeferRUnlock map[string]bool
}

// --- ANÁLISIS DE BLOQUES PARA MUTEX Y RWMutex ---
func AnalyzeMutexBlock(errorCollector *report.ErrorCollector, block *ast.BlockStmt, muNames map[string]bool, rwNames map[string]bool, errs *deferErrorCollector) map[string]*mutexStats {
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
					varName := common.GetVarName(sel.X)
					if muNames[varName] {
						switch sel.Sel.Name {
						case "Lock":
							stats[varName].lock++
							stats[varName].lockPos = append(stats[varName].lockPos, call.Pos())
						case "Unlock":
							if stats[varName].lock == 0 {
								errorCollector.AddError(call.Pos(), "mutex '"+varName+"' is unlocked but not locked")
							} else {
								stats[varName].lock--
								if len(stats[varName].lockPos) > 0 {
									stats[varName].lockPos = stats[varName].lockPos[1:]
								}
							}
						}
					}
					if rwNames[varName] {
						switch sel.Sel.Name {
						case "Lock":
							stats[varName].lock++
							stats[varName].lockPos = append(stats[varName].lockPos, call.Pos())
						case "Unlock":
							if stats[varName].lock == 0 {
								errorCollector.AddError(call.Pos(), "rwmutex '"+varName+"' is unlocked but not locked")
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
								errorCollector.AddError(call.Pos(), "rwmutex '"+varName+"' is runlocked but not rlocked")
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
				varName := common.GetVarName(call.X)
				if muNames[varName] && call.Sel.Name == "Unlock" {
					if stats[varName].lock == 0 {
						errorCollector.AddError(s.Pos(), "mutex '"+varName+"' has defer unlock but no corresponding lock")
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
				if rwNames[varName] {
					if call.Sel.Name == "Unlock" {
						if stats[varName].lock == 0 {
							errorCollector.AddError(s.Pos(), "rwmutex '"+varName+"' has defer unlock but no corresponding lock")
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
							errorCollector.AddError(s.Pos(), "rwmutex '"+varName+"' has defer runlock but no corresponding rlock")
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
			// Análisis de defer en funciones literales, igual que antes...
			if fnlit, ok := s.Call.Fun.(*ast.FuncLit); ok {
				for muName := range muNames {
					unlockedInside := false
					ast.Inspect(fnlit.Body, func(n ast.Node) bool {
						if call, ok := n.(*ast.CallExpr); ok {
							if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
								if sel.Sel.Name == "Unlock" && common.GetVarName(sel.X) == muName {
									unlockedInside = true
									return false
								}
							}
						}
						return true
					})
					if unlockedInside {
						if stats[muName].lock == 0 {
							errorCollector.AddError(s.Pos(), "mutex '"+muName+"' has defer unlock but no corresponding lock")
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
								if sel.Sel.Name == "Unlock" && common.GetVarName(sel.X) == muName {
									unlockedInside = true
								}
								if sel.Sel.Name == "RUnlock" && common.GetVarName(sel.X) == muName {
									runlockedInside = true
								}
							}
						}
						return true
					})
					if unlockedInside {
						if stats[muName].lock == 0 {
							errorCollector.AddError(s.Pos(), "rwmutex '"+muName+"' has defer unlock but no corresponding lock")
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
							errorCollector.AddError(s.Pos(), "rwmutex '"+muName+"' has defer runlock but no corresponding rlock")
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
			thenStats := AnalyzeMutexBlock(errorCollector, s.Body, muNames, rwNames, errs)
			elseStats := map[string]*mutexStats{}
			if s.Else != nil {
				switch b := s.Else.(type) {
				case *ast.BlockStmt:
					elseStats = AnalyzeMutexBlock(errorCollector, b, muNames, rwNames, errs)
				case *ast.IfStmt:
					elseStats = AnalyzeMutexBlock(errorCollector, &ast.BlockStmt{List: []ast.Stmt{b}}, muNames, rwNames, errs)
				default:
					elseStats = map[string]*mutexStats{}
				}
			}
			for mu := range muNames {
				if thenStats[mu].lock > 0 {
					for _, pos := range thenStats[mu].lockPos {
						errorCollector.AddError(pos, "mutex '"+mu+"' is locked but not unlocked")
					}
				}
				if elseStats[mu] != nil && elseStats[mu].lock > 0 {
					for _, pos := range elseStats[mu].lockPos {
						errorCollector.AddError(pos, "mutex '"+mu+"' is locked but not unlocked")
					}
				}
			}
			for mu := range rwNames {
				if thenStats[mu].lock > 0 {
					for _, pos := range thenStats[mu].lockPos {
						errorCollector.AddError(pos, "rwmutex '"+mu+"' is locked but not unlocked")
					}
				}
				if thenStats[mu].rlock > 0 {
					for _, pos := range thenStats[mu].rlockPos {
						errorCollector.AddError(pos, "rwmutex '"+mu+"' is rlocked but not runlocked")
					}
				}
				if elseStats[mu] != nil && elseStats[mu].lock > 0 {
					for _, pos := range elseStats[mu].lockPos {
						errorCollector.AddError(pos, "rwmutex '"+mu+"' is locked but not unlocked")
					}
				}
				if elseStats[mu] != nil && elseStats[mu].rlock > 0 {
					for _, pos := range elseStats[mu].rlockPos {
						errorCollector.AddError(pos, "rwmutex '"+mu+"' is rlocked but not runlocked")
					}
				}
			}
		case *ast.GoStmt:
			if fnLit, ok := s.Call.Fun.(*ast.FuncLit); ok {
				goStats := AnalyzeMutexBlock(errorCollector, fnLit.Body, muNames, rwNames, errs)
				for mu := range muNames {
					if goStats[mu].lock > 0 {
						for _, pos := range goStats[mu].lockPos {
							errorCollector.AddError(pos, "mutex '"+mu+"' is locked but not unlocked")
						}
					}
				}
				for mu := range rwNames {
					if goStats[mu].lock > 0 {
						for _, pos := range goStats[mu].lockPos {
							errorCollector.AddError(pos, "rwmutex '"+mu+"' is locked but not unlocked")
						}
					}
					if goStats[mu].rlock > 0 {
						for _, pos := range goStats[mu].rlockPos {
							errorCollector.AddError(pos, "rwmutex '"+mu+"' is rlocked but not runlocked")
						}
					}
				}
			}
		case *ast.ForStmt:
			forStats := AnalyzeMutexBlock(errorCollector, s.Body, muNames, rwNames, errs)
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
			blockStats := AnalyzeMutexBlock(errorCollector, s, muNames, rwNames, errs)
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
