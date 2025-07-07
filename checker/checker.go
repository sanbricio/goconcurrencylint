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
	"slices"
	"sort"

	"github.com/sanbricio/concurrency-linter/checker/common"
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

type addCall struct {
	pos   token.Pos
	value int
}

type waitGroupStats struct {
	addCalls     []addCall
	doneCalls    []token.Pos
	waitCalls    []token.Pos
	doneCount    int
	hasDeferDone bool
	totalAdd     int
}

// --------------------------
// WAITGROUP: Main analysis
// --------------------------

func analyzeWaitGroupUsage(errorCollector *report.ErrorCollector, fn *ast.FuncDecl, wgNames map[string]bool) map[string]*waitGroupStats {
	stats := map[string]*waitGroupStats{}
	for wg := range wgNames {
		stats[wg] = &waitGroupStats{
			addCalls:  []addCall{},
			doneCalls: []token.Pos{},
			waitCalls: []token.Pos{},
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
								wgName := common.GetVarName(sel.X)
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

	// Traverse AST with stack to track for loop context (MANTENER LÓGICA ORIGINAL)
	var visit func(n ast.Node, forStack []*ast.ForStmt)
	visit = func(n ast.Node, forStack []*ast.ForStmt) {
		switch node := n.(type) {
		case *ast.ForStmt:
			reportedAdd := map[string]bool{}
			for _, stmt := range node.Body.List {
				visitWithReportMap(stmt, append(forStack, node), reportedAdd, wgNames, stats, alreadyReported, errorCollector)
			}
		case *ast.GoStmt:
			reportedAdd := map[string]bool{}
			if fnLit, ok := node.Call.Fun.(*ast.FuncLit); ok {
				for _, stmt := range fnLit.Body.List {
					visitWithReportMap(stmt, forStack, reportedAdd, wgNames, stats, alreadyReported, errorCollector)
				}
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
					wgName := common.GetVarName(sel.X)
					if wgNames[wgName] && sel.Sel.Name == "Add" {
						addValue := common.GetAddValue(call)
						stats[wgName].addCalls = append(stats[wgName].addCalls, addCall{
							pos:   call.Pos(),
							value: addValue,
						})
						stats[wgName].totalAdd += addValue
					}
					if wgNames[wgName] && sel.Sel.Name == "Done" {
						stats[wgName].doneCount++
						stats[wgName].doneCalls = append(stats[wgName].doneCalls, call.Pos())
					}
					if wgNames[wgName] && sel.Sel.Name == "Wait" {
						stats[wgName].waitCalls = append(stats[wgName].waitCalls, call.Pos())
					}
				}
			}
		}
	}
	visit(fn.Body, nil)

	// NUEVA LÓGICA: Detectar Add después de Wait específicamente
	// Buscar patrones donde Wait está en el flujo principal y Add está en goroutines
	for wgName, st := range stats {
		for _, waitPos := range st.waitCalls {
			// Buscar goroutines que contengan Add y que estén después de Wait
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if goStmt, ok := n.(*ast.GoStmt); ok {
					// Si la goroutine está después de Wait en el código fuente
					if goStmt.Pos() > waitPos {
						// Buscar Add dentro de esta goroutine
						if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
							ast.Inspect(fnLit.Body, func(inner ast.Node) bool {
								if call, ok := inner.(*ast.CallExpr); ok {
									if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
										if sel.Sel.Name == "Add" && common.GetVarName(sel.X) == wgName {
											errorCollector.AddError(call.Pos(), "waitgroup '"+wgName+"' Add called after Wait")
										}
									}
								}
								return true
							})
						}
					}
				}
				return true
			})
		}
	}

	// LÓGICA ORIGINAL: Verificar Add vs Wait en el mismo flujo
	for wgName, st := range stats {
		for _, add := range st.addCalls {
			for _, wait := range st.waitCalls {
				// Solo reportar si Add está después de Wait en el mismo flujo (no en goroutines)
				if add.pos > wait {
					// Verificar que Add no esté dentro de una goroutine
					isInGoroutine := false
					ast.Inspect(fn.Body, func(n ast.Node) bool {
						if goStmt, ok := n.(*ast.GoStmt); ok {
							if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
								ast.Inspect(fnLit.Body, func(inner ast.Node) bool {
									if call, ok := inner.(*ast.CallExpr); ok {
										if call.Pos() == add.pos {
											isInGoroutine = true
											return false
										}
									}
									return true
								})
							}
						}
						return !isInGoroutine
					})

					if !isInGoroutine {
						errorCollector.AddError(add.pos, "waitgroup '"+wgName+"' Add called after Wait")
					}
				}
			}
		}
	}

	for wgName := range wgNames {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			goStmt, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}

			callsDone, blocked := goroutineCallsDoneOrBlocks(goStmt, wgName, fn)

			if blocked && !callsDone {
				errorCollector.AddError(goStmt.Pos(), "waitgroup '"+wgName+"' has Add without corresponding Done (goroutine blocks indefinitely before calling Done)")
			}

			return true
		})
	}

	return stats
}

func channelHasSender(fn *ast.FuncDecl, chanName string) bool {
	hasSender := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if send, ok := n.(*ast.SendStmt); ok {
			if ident, ok := send.Chan.(*ast.Ident); ok && ident.Name == chanName {
				hasSender = true
				return false
			}
		}
		return true
	})
	return hasSender
}

func goroutineCallsDoneOrBlocks(goStmt *ast.GoStmt, wgName string, fn *ast.FuncDecl) (callsDone bool, blocked bool) {
	fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
	if !ok {
		return false, false
	}

	callsDone = false
	blocked = false

	ast.Inspect(fnLit.Body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.ExprStmt:
			if call, ok := stmt.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Done" && common.GetVarName(sel.X) == wgName {
					callsDone = true
					return false
				}
			}
			if unary, ok := stmt.X.(*ast.UnaryExpr); ok && unary.Op == token.ARROW {
				if chanIdent, ok := unary.X.(*ast.Ident); ok {
					if !channelHasSender(fn, chanIdent.Name) {
						blocked = true
						return false
					}
				}
			}
		case *ast.SelectStmt:
			blocked = true
			return false
		}
		return true
	})

	return callsDone, blocked
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
				wgName := common.GetVarName(sel.X)
				if wgNames[wgName] && sel.Sel.Name == "Add" {
					addValue := common.GetAddValue(call)
					stats[wgName].addCalls = append(stats[wgName].addCalls, addCall{
						pos:   call.Pos(),
						value: addValue,
					})
					stats[wgName].totalAdd += addValue
				}
				if wgNames[wgName] && sel.Sel.Name == "Done" {
					stats[wgName].doneCount++
					stats[wgName].doneCalls = append(stats[wgName].doneCalls, call.Pos())
				}

				if wgNames[wgName] && sel.Sel.Name == "Wait" {
					stats[wgName].waitCalls = append(stats[wgName].waitCalls, call.Pos())
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

		// Verificar Add sin Done correspondiente
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

		// NUEVO: Verificar Done sin Add correspondiente
		if totalExpectedDone > stats.totalAdd {
			// Ordenar las llamadas Done por posición
			slices.Sort(stats.doneCalls)

			// Determinar cuántas Done están de más
			excessDone := totalExpectedDone - stats.totalAdd

			// Reportar las últimas Done que están de más
			if excessDone > 0 && len(stats.doneCalls) > 0 {
				// Si hay defer Done, no reportar esa posición
				startIndex := len(stats.doneCalls) - excessDone
				if stats.hasDeferDone {
					// Si hay defer Done, ajustar para no reportar una Done normal de más
					if excessDone > 1 {
						startIndex = len(stats.doneCalls) - (excessDone - 1)
					}
				}

				for i := startIndex; i < len(stats.doneCalls); i++ {
					if i >= 0 {
						errorCollector.AddError(stats.doneCalls[i], "waitgroup '"+wgName+"' has Done without corresponding Add")
					}
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
					if common.IsMutex(typ) {
						muNames[name.Name] = true
					}
					if common.IsRWMutex(typ) {
						rwNames[name.Name] = true
					}
					if common.IsWaitGroup(typ) {
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
			blockStats := AnalyzeMutexBlock(errorCollector, fn.Body, muNames, rwNames, errs)
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
