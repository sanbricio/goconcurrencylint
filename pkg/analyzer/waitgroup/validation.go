package waitgroup

import (
	"go/ast"
	"go/token"
	"sort"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
)

// validateUsage performs validation checks on collected statistics
func (wga *Analyzer) validateUsage(stats map[string]*Stats) {
	wga.checkAddAfterWait(stats)
	wga.checkLoopAddDoneBalance()
	wga.checkUnreachableDone()
	wga.checkWaitGroupBalance(stats)
}

// validateBalance performs the actual balance validation for a WaitGroup
func (wga *Analyzer) validateBalance(wgName string, stats *Stats) {
	// Count Done calls from main flow (not in goroutines)
	mainFlowDoneCount := 0
	for _, donePos := range stats.doneCalls {
		if !wga.isInGoroutine(donePos) {
			mainFlowDoneCount++
		}
	}

	totalDone := mainFlowDoneCount

	// Add defer Done count if present and not in goroutine
	if stats.hasDeferDone && !wga.isDeferDoneInGoroutine(wgName) {
		totalDone++
	}

	// Add guaranteed Done calls from goroutines (but don't double count)
	guaranteedFromGoroutines := wga.countGuaranteedDoneInGoroutines(wgName)
	totalDone += guaranteedFromGoroutines

	// Check for balance and report errors
	if stats.totalAdd > totalDone {
		wga.reportUnmatchedAdds(wgName, stats, totalDone)
	}

	if totalDone > stats.totalAdd {
		wga.reportExcessDones(wgName, stats, totalDone, mainFlowDoneCount)
	}
}

// countGuaranteedDoneInGoroutines counts Done calls that are guaranteed to execute in goroutines
func (wga *Analyzer) countGuaranteedDoneInGoroutines(wgName string) int {
	count := 0

	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if goStmt, ok := n.(*ast.GoStmt); ok {
			if wga.goroutineRelatedToWaitGroup(goStmt, wgName) {
				if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
					doneInfo := wga.analyzeDoneCalls(fnLit.Body, wgName)
					if doneInfo.hasGuaranteedDone {
						count++
					} else if !doneInfo.hasAnyDone {
						// Report blocking goroutine error here
						relatedAdd := wga.findRelatedAddCall(goStmt, wgName)
						if relatedAdd != token.NoPos {
							wga.errorCollector.AddError(relatedAdd,
								"waitgroup '"+wgName+"' has Add without corresponding Done")
						}
					}
				}
			}
		}
		return true
	})

	return count
}

// checkWaitGroupBalance validates that Add and Done calls are properly balanced
func (wga *Analyzer) checkWaitGroupBalance(stats map[string]*Stats) {
	for wgName, st := range stats {
		if wga.isWaitGroupPassedToOtherFunctions(wgName) {
			if st.doneCount == 0 && !st.hasDeferDone && len(st.addCalls) > 0 {
				continue
			}
		}
		wga.validateBalance(wgName, st)
	}
}

// reportUnmatchedAdds reports Add calls that don't have corresponding Done calls
func (wga *Analyzer) reportUnmatchedAdds(wgName string, stats *Stats, totalExpectedDone int) {
	sort.Slice(stats.addCalls, func(i, j int) bool {
		return stats.addCalls[i].pos < stats.addCalls[j].pos
	})

	remainingDone := totalExpectedDone
	for _, addCall := range stats.addCalls {
		if remainingDone >= addCall.value {
			remainingDone -= addCall.value
		} else {
			wga.errorCollector.AddError(addCall.pos, "waitgroup '"+wgName+"' has Add without corresponding Done")
		}
	}
}

// reportExcessDones reports Done calls that don't have corresponding Add calls
func (wga *Analyzer) reportExcessDones(wgName string, stats *Stats, totalExpectedDone int, mainFlowDoneCount int) {
	if totalExpectedDone <= stats.totalAdd {
		return
	}

	// Only report excess for main flow Done calls (not goroutine Done calls)
	if mainFlowDoneCount > stats.totalAdd {
		// Sort done calls to report the last ones (most likely to be excess)
		var mainFlowDoneCalls []token.Pos
		for _, donePos := range stats.doneCalls {
			if !wga.isInGoroutine(donePos) {
				mainFlowDoneCalls = append(mainFlowDoneCalls, donePos)
			}
		}

		sort.Slice(mainFlowDoneCalls, func(i, j int) bool {
			return mainFlowDoneCalls[i] < mainFlowDoneCalls[j]
		})

		excessCount := mainFlowDoneCount - stats.totalAdd
		startIndex := len(mainFlowDoneCalls) - excessCount

		for i := startIndex; i < len(mainFlowDoneCalls) && i >= 0; i++ {
			wga.errorCollector.AddError(mainFlowDoneCalls[i], "waitgroup '"+wgName+"' has Done without corresponding Add")
		}
	}
}

// checkAddAfterWait detects Add calls that occur after Wait calls
func (wga *Analyzer) checkAddAfterWait(stats map[string]*Stats) {
	for wgName, st := range stats {
		wga.checkAddAfterWaitInGoroutines(wgName, st)
		wga.checkAddAfterWaitInMainFlow(wgName, st)
	}
}

// checkAddAfterWaitInGoroutines checks for Add after Wait in goroutines
func (wga *Analyzer) checkAddAfterWaitInGoroutines(wgName string, st *Stats) {
	for _, waitPos := range st.waitCalls {
		ast.Inspect(wga.function.Body, func(n ast.Node) bool {
			if goStmt, ok := n.(*ast.GoStmt); ok {
				if goStmt.Pos() > waitPos {
					wga.checkAddInGoroutine(goStmt, wgName)
				}
			}
			return true
		})
	}
}

// checkAddInGoroutine checks for Add calls within a specific goroutine
func (wga *Analyzer) checkAddInGoroutine(goStmt *ast.GoStmt, wgName string) {
	if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
		ast.Inspect(fnLit.Body, func(inner ast.Node) bool {
			if call, ok := inner.(*ast.CallExpr); ok {
				if wga.commentFilter.ShouldSkipCall(call) {
					return true
				}

				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if sel.Sel.Name == "Add" && common.GetVarName(sel.X) == wgName {
						wga.errorCollector.AddError(call.Pos(), "waitgroup '"+wgName+"' Add called after Wait")
					}
				}
			}
			return true
		})
	}
}

// checkAddAfterWaitInMainFlow detects Add calls in the main execution flow that occur after Wait
func (wga *Analyzer) checkAddAfterWaitInMainFlow(wgName string, st *Stats) {
	for _, wait := range st.waitCalls {
		// Check if this Wait has any Add or Done operations before it in main flow
		hasOperationsBefore := false

		// Check for Add calls before this Wait (main flow only)
		for _, add := range st.addCalls {
			if add.pos < wait && !wga.isInGoroutine(add.pos) {
				hasOperationsBefore = true
				break
			}
		}

		// Check for Done calls before this Wait (main flow only)
		if !hasOperationsBefore {
			for _, done := range st.doneCalls {
				if done < wait && !wga.isInGoroutine(done) {
					hasOperationsBefore = true
					break
				}
			}
		}

		// If this is an "empty" Wait (no operations before it), flag subsequent Add calls
		if !hasOperationsBefore {
			for _, add := range st.addCalls {
				// Only report Add calls in main flow after this empty Wait
				if add.pos > wait && !wga.isInGoroutine(add.pos) {
					wga.errorCollector.AddError(add.pos, "waitgroup '"+wgName+"' Add called after Wait")
				}
			}
		}
	}
}

// checkLoopAddDoneBalance checks for Add/Done balance issues in loops
func (wga *Analyzer) checkLoopAddDoneBalance() {
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if forStmt, ok := n.(*ast.ForStmt); ok {
			wga.analyzeLoopBalance(forStmt)
		}
		return true
	})
}

// loopAnalysis tracks Add/Done calls within a loop
type loopAnalysis struct {
	addCalls           []token.Pos
	unconditionalDones int
	conditionalDones   int
}

// analyzeLoopBalance analyzes Add/Done balance within a single loop
func (wga *Analyzer) analyzeLoopBalance(forStmt *ast.ForStmt) {
	loopStats := make(map[string]*loopAnalysis)

	ast.Inspect(forStmt.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ExprStmt:
			if call, ok := node.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					wgName := common.GetVarName(sel.X)
					if wga.waitGroupNames[wgName] {
						if loopStats[wgName] == nil {
							loopStats[wgName] = &loopAnalysis{}
						}

						switch sel.Sel.Name {
						case "Add":
							loopStats[wgName].addCalls = append(loopStats[wgName].addCalls, call.Pos())
						case "Done":
							if wga.isInConditional(call, forStmt.Body) {
								loopStats[wgName].conditionalDones++
							} else {
								loopStats[wgName].unconditionalDones++
							}
						}
					}
				}
			}
		}
		return true
	})

	for wgName, stats := range loopStats {
		if len(stats.addCalls) > 0 {
			if stats.unconditionalDones == 0 && stats.conditionalDones > 0 {
				for _, addPos := range stats.addCalls {
					wga.errorCollector.AddError(addPos,
						"waitgroup '"+wgName+"' has Add without corresponding Done")
				}
			}
		}
	}
}

// isInConditional checks if a node is inside an if statement
func (wga *Analyzer) isInConditional(target ast.Node, scope ast.Node) bool {
	inConditional := false

	ast.Inspect(scope, func(n ast.Node) bool {
		if n == target {
			return false
		}

		if ifStmt, ok := n.(*ast.IfStmt); ok {
			ast.Inspect(ifStmt, func(inner ast.Node) bool {
				if inner == target {
					inConditional = true
					return false
				}
				return true
			})
		}

		return !inConditional
	})

	return inConditional
}

// checkUnreachableDone checks for Done calls that are unreachable due to early returns
func (wga *Analyzer) checkUnreachableDone() {
	for wgName := range wga.waitGroupNames {
		ast.Inspect(wga.function.Body, func(n ast.Node) bool {
			goStmt, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}

			if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
				if wga.hasUnreachableDone(fnLit.Body, wgName) {
					addPos := wga.findRelatedAddCall(goStmt, wgName)
					if addPos != token.NoPos {
						wga.errorCollector.AddError(addPos,
							"waitgroup '"+wgName+"' has Add without corresponding Done")
					}
				}
			}

			return true
		})
	}
}
