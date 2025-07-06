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
	"sort"
	"strconv"

	"github.com/sanbricio/concurrency-linter/checker/report"
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

// getAddValue extracts the numeric value from Add() calls, returns 1 as default
func getAddValue(call *ast.CallExpr) int {
	if len(call.Args) == 0 {
		return 1
	}
	if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.INT {
		if val, err := strconv.Atoi(lit.Value); err == nil {
			return val
		}
	}
	// For non-literal arguments, assume 1 for simplicity
	return 1
}

type mutexStats struct {
	lock, rlock       int
	lockPos, rlockPos []token.Pos
}

type deferErrorCollector struct {
	badDeferUnlock  map[string]bool
	badDeferRUnlock map[string]bool
}

type addCall struct {
	pos   token.Pos
	value int
}

type waitGroupStats struct {
	addCalls     []addCall
	doneCount    int
	hasDeferDone bool
	totalAdd     int
}

// ----------------------------
// MUTEX: Main analysis
// ----------------------------

func analyzeBlock(errorCollector *report.ErrorCollector, block *ast.BlockStmt, muNames map[string]bool, rwNames map[string]bool, errs *deferErrorCollector) map[string]*mutexStats {
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
				varName := getVarName(call.X)
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
			if fnlit, ok := s.Call.Fun.(*ast.FuncLit); ok {
				for muName := range muNames {
					unlockedInside := false
					ast.Inspect(fnlit.Body, func(n ast.Node) bool {
						if call, ok := n.(*ast.CallExpr); ok {
							if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
								if sel.Sel.Name == "Unlock" && getVarName(sel.X) == muName {
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
								if sel.Sel.Name == "Unlock" && getVarName(sel.X) == muName {
									unlockedInside = true
								}
								if sel.Sel.Name == "RUnlock" && getVarName(sel.X) == muName {
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
			thenStats := analyzeBlock(errorCollector, s.Body, muNames, rwNames, errs)
			elseStats := map[string]*mutexStats{}
			if s.Else != nil {
				switch b := s.Else.(type) {
				case *ast.BlockStmt:
					elseStats = analyzeBlock(errorCollector, b, muNames, rwNames, errs)
				case *ast.IfStmt:
					elseStats = analyzeBlock(errorCollector, &ast.BlockStmt{List: []ast.Stmt{b}}, muNames, rwNames, errs)
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
				goStats := analyzeBlock(errorCollector, fnLit.Body, muNames, rwNames, errs)
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
			forStats := analyzeBlock(errorCollector, s.Body, muNames, rwNames, errs)
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
			blockStats := analyzeBlock(errorCollector, s, muNames, rwNames, errs)
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

// --------------------------
// WAITGROUP: Main analysis
// --------------------------

func analyzeWaitGroupUsage(errorCollector *report.ErrorCollector, fn *ast.FuncDecl, wgNames map[string]bool) map[string]*waitGroupStats {
	stats := map[string]*waitGroupStats{}
	for wg := range wgNames {
		stats[wg] = &waitGroupStats{
			addCalls: []addCall{},
			// doneCount, hasDeferDone, totalAdd: default 0/false
		}
	}

	// Check for defer Done calls first
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if deferStmt, ok := n.(*ast.DeferStmt); ok {
			if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := call.X.(*ast.Ident); ok && call.Sel.Name == "Done" {
					if wgNames[ident.Name] {
						stats[ident.Name].hasDeferDone = true
					}
				}
			}
			// Check for defer in function literals
			if fnlit, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok {
				ast.Inspect(fnlit.Body, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
							if sel.Sel.Name == "Done" {
								wgName := getVarName(sel.X)
								if wgNames[wgName] {
									stats[wgName].hasDeferDone = true
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

	alreadyReported := map[token.Pos]bool{}
	// Traverse AST with stack to track for loop context
	var visit func(n ast.Node, forStack []*ast.ForStmt)
	visit = func(n ast.Node, forStack []*ast.ForStmt) {
		switch node := n.(type) {
		case *ast.ForStmt:
			reportedAdd := map[string]bool{} // wgName -> bool, to avoid multiple reports per loop
			for _, stmt := range node.Body.List {
				visitWithReportMap(stmt, append(forStack, node), reportedAdd, wgNames, stats, alreadyReported, errorCollector)
			}
		case *ast.BlockStmt:
			for _, stmt := range node.List {
				visit(stmt, forStack)
			}
		case *ast.IfStmt:
			visit(node.Body, forStack)
			if node.Else != nil {
				visit(node.Else, forStack)
			}
		case *ast.ExprStmt:
			if call, ok := node.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					wgName := getVarName(sel.X)
					if wgNames[wgName] && sel.Sel.Name == "Add" {
						addValue := getAddValue(call)
						stats[wgName].addCalls = append(stats[wgName].addCalls, addCall{
							pos:   call.Pos(),
							value: addValue,
						})
						stats[wgName].totalAdd += addValue
						// No reportedAdd here, only in for-loop context
					}
					if wgNames[wgName] && sel.Sel.Name == "Done" {
						stats[wgName].doneCount++
					}
				}
			}
		}
	}
	visit(fn.Body, nil)

	return stats
}

// Helper for for-loops to avoid multiple diagnostics per loop
func visitWithReportMap(n ast.Node, forStack []*ast.ForStmt, reportedAdd map[string]bool, wgNames map[string]bool, stats map[string]*waitGroupStats, alreadyReported map[token.Pos]bool, errorCollector *report.ErrorCollector) {
	switch node := n.(type) {
	case *ast.ForStmt:
		for _, stmt := range node.Body.List {
			visitWithReportMap(stmt, append(forStack, node), reportedAdd, wgNames, stats, alreadyReported, errorCollector)
		}
	case *ast.BlockStmt:
		for _, stmt := range node.List {
			visitWithReportMap(stmt, forStack, reportedAdd, wgNames, stats, alreadyReported, errorCollector)
		}
	case *ast.IfStmt:
		visitWithReportMap(node.Body, forStack, reportedAdd, wgNames, stats, alreadyReported, errorCollector)
		if node.Else != nil {
			visitWithReportMap(node.Else, forStack, reportedAdd, wgNames, stats, alreadyReported, errorCollector)
		}
	case *ast.ExprStmt:
		if call, ok := node.X.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				wgName := getVarName(sel.X)
				if wgNames[wgName] && sel.Sel.Name == "Add" {
					addValue := getAddValue(call)
					stats[wgName].addCalls = append(stats[wgName].addCalls, addCall{
						pos:   call.Pos(),
						value: addValue,
					})
					stats[wgName].totalAdd += addValue
					// No reportedAdd here, only in for-loop context
				}
				if wgNames[wgName] && sel.Sel.Name == "Done" {
					stats[wgName].doneCount++
				}
			}
		}
	}
}

func checkWaitGroupBalance(errorCollector *report.ErrorCollector, wgStats map[string]*waitGroupStats) {
	for wgName, stats := range wgStats {
		totalExpectedDone := stats.doneCount
		if stats.hasDeferDone {
			totalExpectedDone++
		}

		if stats.totalAdd > totalExpectedDone {
			sort.Slice(stats.addCalls, func(i, j int) bool {
				return stats.addCalls[i].pos < stats.addCalls[j].pos
			})
			remainingDone := totalExpectedDone
			for _, addCall := range stats.addCalls {
				if remainingDone >= addCall.value {
					remainingDone -= addCall.value
				} else {
					errorCollector.AddError(addCall.pos, "waitgroup '"+wgName+"' has Add without corresponding Done")
				}
			}
		}
	}
}

// isWaitGroupPassedToOtherFunctions checks if a WaitGroup is passed to other functions
func isWaitGroupPassedToOtherFunctions(fn *ast.FuncDecl, wgName string) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			for _, arg := range call.Args {
				if unary, ok := arg.(*ast.UnaryExpr); ok && unary.Op == token.AND {
					if ident, ok := unary.X.(*ast.Ident); ok && ident.Name == wgName {
						found = true
						return false
					}
				}
				if ident, ok := arg.(*ast.Ident); ok && ident.Name == wgName {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func run(pass *analysis.Pass) (interface{}, error) {
	errorCollector := &report.ErrorCollector{}

	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			muNames := map[string]bool{}
			rwNames := map[string]bool{}
			wgNames := map[string]bool{}

			// Find all mutex and waitgroup declarations
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

			// Analyze mutex usage
			errs := &deferErrorCollector{
				badDeferUnlock:  map[string]bool{},
				badDeferRUnlock: map[string]bool{},
			}
			blockStats := analyzeBlock(errorCollector, fn.Body, muNames, rwNames, errs)
			for mu, st := range blockStats {
				if errs.badDeferUnlock[mu] {
					continue
				}
				if errs.badDeferRUnlock[mu] {
					continue
				}
				for _, pos := range st.lockPos {
					if rwNames[mu] {
						errorCollector.AddError(pos, "rwmutex '"+mu+"' is locked but not unlocked")
					} else {
						errorCollector.AddError(pos, "mutex '"+mu+"' is locked but not unlocked")
					}
				}
				for _, pos := range st.rlockPos {
					errorCollector.AddError(pos, "rwmutex '"+mu+"' is rlocked but not runlocked")
				}
			}

			// Analyze WaitGroup usage
			wgStats := analyzeWaitGroupUsage(errorCollector, fn, wgNames)

			// Check balance for WaitGroups, but be más lenient if they are passed to other functions
			for wgName, stats := range wgStats {
				if isWaitGroupPassedToOtherFunctions(fn, wgName) {
					if stats.doneCount == 0 && !stats.hasDeferDone && len(stats.addCalls) > 0 {
						continue
					}
				}
				checkWaitGroupBalance(errorCollector, map[string]*waitGroupStats{wgName: stats})
			}
		}
	}

	errorCollector.ReportAll(pass)
	return nil, nil
}
