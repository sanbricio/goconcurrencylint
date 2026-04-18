package waitgroup

import (
	"go/ast"
	"go/token"
	"sort"
	"strings"

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
	return wga.countGuaranteedDoneInStatements(wga.function.Body.List, wgName, 1)
}

// checkWaitGroupBalance validates that Add and Done calls are properly balanced
func (wga *Analyzer) checkWaitGroupBalance(stats map[string]*Stats) {
	for wgName, st := range stats {
		if wga.isBorrowedWaitGroupField(wgName, st) {
			continue
		}
		if wga.isWaitGroupPassedToOtherFunctions(wgName) {
			if st.doneCount == 0 && !st.hasDeferDone && len(st.addCalls) > 0 {
				continue
			}
		}
		wga.validateBalance(wgName, st)
	}
}

func (wga *Analyzer) isBorrowedWaitGroupField(wgName string, st *Stats) bool {
	return strings.Contains(wgName, ".") && st.totalAdd == 0 && len(st.waitCalls) == 0 && (st.doneCount > 0 || st.hasDeferDone)
}

func (wga *Analyzer) countGuaranteedDoneInStatements(stmts []ast.Stmt, wgName string, multiplier int) int {
	count := 0

	for _, stmt := range stmts {
		if wga.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.GoStmt:
			if fnLit, ok := s.Call.Fun.(*ast.FuncLit); ok {
				nestedCount := wga.countGuaranteedDoneInStatements(fnLit.Body.List, wgName, multiplier)
				if nestedCount > 0 {
					count += nestedCount
					continue
				}
			}

			doneInfo, related := wga.goroutineDoneInfo(s, wgName)
			if !related {
				continue
			}
			if doneInfo.hasGuaranteedDone {
				count += multiplier
				continue
			}
			if wga.goroutineOnlyWaitsOnWaitGroup(s, wgName) {
				continue
			}
			if !doneInfo.hasAnyDone {
				relatedAdd := wga.findRelatedAddCall(s, wgName)
				if relatedAdd != token.NoPos {
					wga.errorCollector.AddError(relatedAdd,
						"waitgroup '"+wgName+"' has Add without corresponding Done")
				}
			}

		case *ast.BlockStmt:
			count += wga.countGuaranteedDoneInStatements(s.List, wgName, multiplier)

		case *ast.IfStmt:
			count += wga.countGuaranteedDoneInStatements(s.Body.List, wgName, multiplier)
			if elseBlock, ok := s.Else.(*ast.BlockStmt); ok {
				count += wga.countGuaranteedDoneInStatements(elseBlock.List, wgName, multiplier)
			} else if elseIf, ok := s.Else.(*ast.IfStmt); ok {
				count += wga.countGuaranteedDoneInStatements([]ast.Stmt{elseIf}, wgName, multiplier)
			}

		case *ast.ForStmt:
			factor := multiplier * wga.estimateForIterations(s)
			count += wga.countGuaranteedDoneInStatements(s.Body.List, wgName, factor)

		case *ast.RangeStmt:
			count += wga.countGuaranteedDoneInStatements(s.Body.List, wgName, multiplier)

		case *ast.SwitchStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					count += wga.countGuaranteedDoneInStatements(cc.Body, wgName, multiplier)
				}
			}

		case *ast.TypeSwitchStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					count += wga.countGuaranteedDoneInStatements(cc.Body, wgName, multiplier)
				}
			}

		case *ast.SelectStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CommClause); ok {
					count += wga.countGuaranteedDoneInStatements(cc.Body, wgName, multiplier)
				}
			}

		case *ast.LabeledStmt:
			count += wga.countGuaranteedDoneInStatements([]ast.Stmt{s.Stmt}, wgName, multiplier)
		}
	}

	return count
}

func (wga *Analyzer) estimateForIterations(forStmt *ast.ForStmt) int {
	if forStmt == nil {
		return 1
	}

	start := 0
	counterName := ""

	if init, ok := forStmt.Init.(*ast.AssignStmt); ok && len(init.Lhs) == 1 && len(init.Rhs) == 1 {
		if ident, ok := init.Lhs[0].(*ast.Ident); ok {
			if lit, ok := init.Rhs[0].(*ast.BasicLit); ok && lit.Kind == token.INT {
				start = parseIntLiteral(lit)
				counterName = ident.Name
			}
		}
	}

	if counterName == "" {
		return 1
	}

	cond, ok := forStmt.Cond.(*ast.BinaryExpr)
	if !ok {
		return 1
	}
	left, ok := cond.X.(*ast.Ident)
	if !ok || left.Name != counterName {
		return 1
	}
	right, ok := cond.Y.(*ast.BasicLit)
	if !ok || right.Kind != token.INT {
		return 1
	}
	limit := parseIntLiteral(right)

	switch post := forStmt.Post.(type) {
	case *ast.IncDecStmt:
		if ident, ok := post.X.(*ast.Ident); !ok || ident.Name != counterName || post.Tok != token.INC {
			return 1
		}
	case *ast.AssignStmt:
		if len(post.Lhs) != 1 || len(post.Rhs) != 1 {
			return 1
		}
		ident, ok := post.Lhs[0].(*ast.Ident)
		if !ok || ident.Name != counterName {
			return 1
		}
		if post.Tok != token.ADD_ASSIGN {
			return 1
		}
		if lit, ok := post.Rhs[0].(*ast.BasicLit); !ok || lit.Kind != token.INT || parseIntLiteral(lit) != 1 {
			return 1
		}
	default:
		return 1
	}

	switch cond.Op {
	case token.LSS:
		if limit <= start {
			return 1
		}
		return limit - start
	case token.LEQ:
		if limit < start {
			return 1
		}
		return limit - start + 1
	default:
		return 1
	}
}

func parseIntLiteral(lit *ast.BasicLit) int {
	if lit == nil {
		return 0
	}
	value := 0
	for _, ch := range lit.Value {
		if ch < '0' || ch > '9' {
			break
		}
		value = value*10 + int(ch-'0')
	}
	return value
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
					if common.GetVarName(sel.X) != wgName {
						return true
					}

					switch sel.Sel.Name {
					case "Add":
						wga.errorCollector.AddError(call.Pos(), "waitgroup '"+wgName+"' Add called after Wait")
					case "Go":
						wga.errorCollector.AddError(call.Pos(), "waitgroup '"+wgName+"' Go called after Wait")
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

		// WaitGroup.Go is also a task-starting operation and must be treated like Add
		// for the "empty Wait followed by new work" pattern.
		if !hasOperationsBefore {
			for _, goPos := range st.goCalls {
				if goPos < wait && !wga.isInGoroutine(goPos) {
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
					if add.value == 1 && wga.hasDeferredDoneAfter(wgName, add.pos) {
						continue
					}
					wga.errorCollector.AddError(add.pos, "waitgroup '"+wgName+"' Add called after Wait")
				}
			}
			for _, goPos := range st.goCalls {
				if goPos > wait && !wga.isInGoroutine(goPos) {
					wga.errorCollector.AddError(goPos, "waitgroup '"+wgName+"' Go called after Wait")
				}
			}
		}
	}
}

func (wga *Analyzer) goroutineOnlyWaitsOnWaitGroup(goStmt *ast.GoStmt, wgName string) bool {
	fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
	if !ok {
		return false
	}

	hasWait := false
	hasTaskLikeUse := false

	ast.Inspect(fnLit.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || common.GetVarName(sel.X) != wgName {
			return true
		}

		switch sel.Sel.Name {
		case "Wait":
			hasWait = true
		case "Add", "Done", "Go":
			hasTaskLikeUse = true
			return false
		}

		return true
	})

	return hasWait && !hasTaskLikeUse
}

func (wga *Analyzer) hasDeferredDoneAfter(wgName string, after token.Pos) bool {
	found := false

	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}

		deferStmt, ok := n.(*ast.DeferStmt)
		if !ok || deferStmt.Pos() <= after {
			return true
		}
		if wga.isNodeInGoroutine(deferStmt) {
			return true
		}
		if wga.isSimpleDeferDone(deferStmt, wgName) {
			found = true
			return false
		}
		return true
	})

	return found
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
