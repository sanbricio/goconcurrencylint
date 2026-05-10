package waitgroup

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/category"
	"golang.org/x/tools/go/ast/astutil"
)

// validateUsage performs validation checks on collected statistics
func (wga *Analyzer) validateUsage(stats map[string]*Stats) {
	wga.checkAddInsideGoroutine()
	wga.checkDoneNotDeferredInWorker()
	wga.checkLiteralAddLoopGoroutineMismatch(stats)
	wga.checkWaitWithoutAdd(stats)
	wga.checkMultipleDoneSameWorkerBranch()
	wga.checkNestedWaitGroupDeadlock()
	wga.checkAddAfterWait(stats)
	wga.checkWaitBeforeDoneSameGoroutine(stats)
	wga.checkWaitAndDoneInSameGoroutine()
	wga.checkDoneOutsideWorkerGoroutine()
	wga.checkWaitGroupGoPanic()
	wga.checkLoopAddDoneBalance()
	wga.checkUnreachableDone()
	wga.checkWaitGroupBalance(stats)
}

// validateBalance performs the actual balance validation for a WaitGroup
func (wga *Analyzer) validateBalance(wgName string, stats *Stats) {
	// Count Done calls from main flow (not in goroutines)
	mainFlowDoneCount := wga.countMainFlowDoneCalls(wgName)

	totalDone := mainFlowDoneCount

	for _, deferDonePos := range stats.deferDoneCalls {
		if !wga.isInGoroutine(deferDonePos) {
			totalDone++
		}
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

func (wga *Analyzer) countMainFlowDoneCalls(wgName string) int {
	if wga.function == nil || wga.function.Body == nil {
		return 0
	}
	return wga.countMainFlowDoneInStatements(wga.function.Body.List, wgName, 1)
}

func (wga *Analyzer) countMainFlowDoneInStatements(stmts []ast.Stmt, wgName string, multiplier int) int {
	count := 0
	for _, stmt := range stmts {
		if stmt == nil || wga.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if ok && wga.callInvokesDone(call, wgName) {
				count += multiplier
			}
		case *ast.GoStmt:
			continue
		case *ast.BlockStmt:
			count += wga.countMainFlowDoneInStatements(s.List, wgName, multiplier)
		case *ast.IfStmt:
			count += wga.countMainFlowDoneInStatements(s.Body.List, wgName, multiplier)
			if s.Else != nil {
				count += wga.countMainFlowDoneInElse(s.Else, wgName, multiplier)
			}
		case *ast.ForStmt:
			count += wga.countMainFlowDoneInStatements(s.Body.List, wgName, multiplier*wga.estimateForIterations(s))
		case *ast.RangeStmt:
			count += wga.countMainFlowDoneInStatements(s.Body.List, wgName, multiplier*wga.estimateRangeIterations(s))
		case *ast.SwitchStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					count += wga.countMainFlowDoneInStatements(cc.Body, wgName, multiplier)
				}
			}
		case *ast.TypeSwitchStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					count += wga.countMainFlowDoneInStatements(cc.Body, wgName, multiplier)
				}
			}
		case *ast.SelectStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CommClause); ok {
					count += wga.countMainFlowDoneInStatements(cc.Body, wgName, multiplier)
				}
			}
		case *ast.LabeledStmt:
			count += wga.countMainFlowDoneInStatements([]ast.Stmt{s.Stmt}, wgName, multiplier)
		}
	}
	return count
}

func (wga *Analyzer) countMainFlowDoneInElse(stmt ast.Stmt, wgName string, multiplier int) int {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return wga.countMainFlowDoneInStatements(s.List, wgName, multiplier)
	case *ast.IfStmt:
		return wga.countMainFlowDoneInStatements([]ast.Stmt{s}, wgName, multiplier)
	default:
		return 0
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
		if wga.isLikelyExternalLifecycleWaitGroup(wgName, st) {
			continue
		}
		if wga.isWaitGroupPassedToOtherFunctions(wgName) {
			if len(st.addCalls) > 0 {
				continue
			}
		}
		wga.validateBalance(wgName, st)
	}
}

func (wga *Analyzer) isBorrowedWaitGroupField(wgName string, st *Stats) bool {
	return strings.Contains(wgName, ".") && st.totalAdd == 0 && len(st.waitCalls) == 0 && (st.doneCount > 0 || len(st.deferDoneCalls) > 0)
}

func (wga *Analyzer) isLikelyExternalLifecycleWaitGroup(wgName string, st *Stats) bool {
	if !strings.Contains(wgName, ".") {
		return false
	}
	if st.totalAdd == 0 || st.doneCount > 0 || len(st.deferDoneCalls) > 0 || len(st.waitCalls) > 0 || len(st.goCalls) > 0 {
		return false
	}
	return true
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
					wga.errorCollector.AddError(relatedAdd, category.AddWithoutDone,
						"waitgroup '"+wgName+"' has Add without corresponding Done")
				}
			}

		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			if fnLit, ok := call.Fun.(*ast.FuncLit); ok && fnLit.Body != nil {
				count += wga.countGuaranteedDoneInStatements(fnLit.Body.List, wgName, multiplier)
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
			factor := multiplier * wga.estimateRangeIterations(s)
			count += wga.countGuaranteedDoneInStatements(s.Body.List, wgName, factor)

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
			if value, ok := common.ConstantIntValue(init.Rhs[0], wga.typesInfo); ok {
				start = value
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
	limit, ok := common.ConstantIntValue(cond.Y, wga.typesInfo)
	if !ok {
		return 1
	}

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

func (wga *Analyzer) estimateRangeIterations(rangeStmt *ast.RangeStmt) int {
	if rangeStmt == nil || rangeStmt.X == nil {
		return 1
	}

	if tv, ok := wga.typesInfo.Types[rangeStmt.X]; ok && tv.Value != nil {
		if value, ok := constant.Int64Val(tv.Value); ok && value > 0 {
			return int(value)
		}
	}

	return 1
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
			wga.errorCollector.AddError(addCall.pos, category.AddWithoutDone, "waitgroup '"+wgName+"' has Add without corresponding Done")
		}
	}
}

// reportExcessDones reports Done calls that don't have corresponding Add calls
func (wga *Analyzer) reportExcessDones(wgName string, stats *Stats, totalExpectedDone int, _ int) {
	if totalExpectedDone <= stats.totalAdd {
		return
	}

	// Only report excess for main flow Done calls (not goroutine Done calls)
	var mainFlowDoneCalls []token.Pos
	for _, donePos := range stats.doneCalls {
		if !wga.isInGoroutine(donePos) && !wga.isInBranchingControlFlow(donePos) {
			mainFlowDoneCalls = append(mainFlowDoneCalls, donePos)
		}
	}

	if len(mainFlowDoneCalls) <= stats.totalAdd {
		return
	}

	sort.Slice(mainFlowDoneCalls, func(i, j int) bool {
		return mainFlowDoneCalls[i] < mainFlowDoneCalls[j]
	})

	excessCount := len(mainFlowDoneCalls) - stats.totalAdd
	startIndex := len(mainFlowDoneCalls) - excessCount

	for i := startIndex; i < len(mainFlowDoneCalls) && i >= 0; i++ {
		wga.errorCollector.AddError(mainFlowDoneCalls[i], category.DoneWithoutAdd, "waitgroup '"+wgName+"' has Done without corresponding Add")
	}
}

// checkAddAfterWait detects Add calls that occur after Wait calls
func (wga *Analyzer) checkAddAfterWait(stats map[string]*Stats) {
	for wgName, st := range stats {
		wga.checkAddAfterWaitInGoroutines(wgName, st)
		wga.checkAddAfterWaitInMainFlow(wgName, st)
	}
}

func (wga *Analyzer) checkWaitBeforeDoneSameGoroutine(stats map[string]*Stats) {
	for wgName, st := range stats {
		for _, waitPos := range st.waitCalls {
			if wga.isInGoroutine(waitPos) || wga.hasRelatedGoroutineBeforeWait(wgName, waitPos) {
				continue
			}
			if wga.pendingMainFlowAddsBeforeWait(st, waitPos) > 0 && wga.hasMainFlowReleaseAfterWait(st, waitPos) {
				wga.errorCollector.AddError(waitPos, category.WaitDeadlock, "waitgroup '"+wgName+"' waits with pending Add in the same goroutine")
			}
		}
	}
}

func (wga *Analyzer) pendingMainFlowAddsBeforeWait(st *Stats, waitPos token.Pos) int {
	pending := 0
	for _, add := range st.addCalls {
		if add.pos < waitPos && wga.isInMainFunctionFlow(add.pos) {
			pending += add.value
		}
	}
	for _, done := range st.doneCalls {
		if done < waitPos && wga.isInMainFunctionFlow(done) {
			pending--
		}
	}
	if pending < 0 {
		return 0
	}
	return pending
}

func (wga *Analyzer) hasMainFlowReleaseAfterWait(st *Stats, waitPos token.Pos) bool {
	for _, done := range st.doneCalls {
		if done > waitPos && wga.isInMainFunctionFlow(done) {
			return true
		}
	}
	for _, deferDone := range st.deferDoneCalls {
		if deferDone < waitPos && wga.isInMainFunctionFlow(deferDone) {
			return true
		}
	}
	return false
}

func (wga *Analyzer) isInMainFunctionFlow(pos token.Pos) bool {
	return !wga.isInGoroutine(pos) && !wga.isInNestedFunctionLiteral(pos)
}

func (wga *Analyzer) isInNestedFunctionLiteral(pos token.Pos) bool {
	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		fnLit, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		if nodeContainsPos(fnLit.Body, pos) {
			found = true
			return false
		}
		return true
	})
	return found
}

func (wga *Analyzer) hasRelatedGoroutineBeforeWait(wgName string, waitPos token.Pos) bool {
	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || goStmt.Pos() > waitPos {
			return true
		}
		doneInfo, related := wga.goroutineDoneInfo(goStmt, wgName)
		if related && doneInfo.hasAnyDone {
			found = true
			return false
		}
		return true
	})
	return found
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
						wga.errorCollector.AddError(call.Pos(), category.AddAfterWait, "waitgroup '"+wgName+"' Add called after Wait")
					case "Go":
						wga.errorCollector.AddError(call.Pos(), category.GoAfterWait, "waitgroup '"+wgName+"' Go called after Wait")
					}
				}
			}
			return true
		})
	}
}

// checkAddAfterWaitInMainFlow detects Add calls in the main execution flow that occur after Wait
func (wga *Analyzer) checkAddAfterWaitInMainFlow(wgName string, st *Stats) {
	if strings.Contains(wgName, ".") {
		return
	}
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
					wga.errorCollector.AddError(add.pos, category.AddAfterWait, "waitgroup '"+wgName+"' Add called after Wait")
				}
			}
			for _, goPos := range st.goCalls {
				if goPos > wait && !wga.isInGoroutine(goPos) {
					wga.errorCollector.AddError(goPos, category.GoAfterWait, "waitgroup '"+wgName+"' Go called after Wait")
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
					wga.errorCollector.AddError(addPos, category.AddWithoutDone,
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

func (wga *Analyzer) isInBranchingControlFlow(pos token.Pos) bool {
	inBranch := false

	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if inBranch || n == nil {
			return false
		}

		switch node := n.(type) {
		case *ast.IfStmt:
			if nodeContainsPos(node.Body, pos) || nodeContainsPos(node.Else, pos) {
				inBranch = true
				return false
			}
		case *ast.SwitchStmt:
			if nodeContainsPos(node.Body, pos) {
				inBranch = true
				return false
			}
		case *ast.TypeSwitchStmt:
			if nodeContainsPos(node.Body, pos) {
				inBranch = true
				return false
			}
		case *ast.SelectStmt:
			if nodeContainsPos(node.Body, pos) {
				inBranch = true
				return false
			}
		}

		return true
	})

	return inBranch
}

func nodeContainsPos(n ast.Node, pos token.Pos) bool {
	if n == nil {
		return false
	}
	return n.Pos() <= pos && pos <= n.End()
}

func (wga *Analyzer) checkLiteralAddLoopGoroutineMismatch(stats map[string]*Stats) {
	for wgName, st := range stats {
		var positiveAdds []addCall
		for _, add := range st.addCalls {
			if add.value > 0 && !wga.isInGoroutine(add.pos) {
				positiveAdds = append(positiveAdds, add)
			}
		}
		if len(positiveAdds) != 1 {
			continue
		}
		launched := wga.countLoopWorkerGoroutinesAfter(positiveAdds[0].pos, wgName)
		if launched <= 1 || launched == positiveAdds[0].value {
			continue
		}
		wga.errorCollector.AddError(positiveAdds[0].pos, category.AddLoopCountMismatch,
			"waitgroup '"+wgName+"' Add count "+strconv.Itoa(positiveAdds[0].value)+" does not match "+strconv.Itoa(launched)+" goroutines launched")
	}
}

func (wga *Analyzer) countLoopWorkerGoroutinesAfter(after token.Pos, wgName string) int {
	total := 0
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		switch loop := n.(type) {
		case *ast.ForStmt:
			if loop.Pos() <= after {
				return true
			}
			iterations := wga.estimateForIterations(loop)
			if iterations <= 1 {
				return true
			}
			total += iterations * wga.countWorkerGoroutines(loop.Body, wgName)
		case *ast.RangeStmt:
			if loop.Pos() <= after {
				return true
			}
			iterations := wga.estimateRangeIterationsForMismatch(loop)
			if iterations <= 1 {
				return true
			}
			total += iterations * wga.countWorkerGoroutines(loop.Body, wgName)
		}
		return true
	})
	return total
}

func (wga *Analyzer) estimateRangeIterationsForMismatch(rangeStmt *ast.RangeStmt) int {
	if rangeStmt == nil || rangeStmt.X == nil {
		return 1
	}
	if lit, ok := rangeStmt.X.(*ast.CompositeLit); ok {
		return len(lit.Elts)
	}
	return wga.estimateRangeIterations(rangeStmt)
}

func (wga *Analyzer) countWorkerGoroutines(body *ast.BlockStmt, wgName string) int {
	if body == nil {
		return 0
	}
	count := 0
	ast.Inspect(body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		doneInfo, related := wga.goroutineDoneInfo(goStmt, wgName)
		if related && doneInfo.hasAnyDone {
			count++
		}
		return true
	})
	return count
}

func (wga *Analyzer) checkWaitWithoutAdd(stats map[string]*Stats) {
	for wgName, st := range stats {
		if !wga.localWaitGroupNames[wgName] || strings.Contains(wgName, ".") ||
			len(st.addCalls) > 0 || len(st.goCalls) > 0 || wga.waitGroupInitializedFromAnother(wgName) {
			continue
		}
		for _, waitPos := range st.waitCalls {
			targetObj := wga.waitGroupReceiverObjectAt(wgName, "Wait", waitPos)
			// The Add may live in a helper function the WaitGroup is passed to,
			// or in a closure assigned to a local variable that is invoked later.
			// Both checks must consider only references that appear before the
			// Wait, since later code cannot supply the missing Add.
			if targetObj != nil &&
				(wga.isWaitGroupPassedToOtherFunctionsForWait(targetObj, waitPos) ||
					wga.hasAddInLocalClosure(targetObj, waitPos)) {
				continue
			}
			wga.errorCollector.AddError(waitPos, category.WaitWithoutAdd, "waitgroup '"+wgName+"' Wait called without any Add")
		}
	}
}

// hasAddInLocalClosure reports whether a WaitGroup has Add called inside a
// function literal assigned to a local variable. This is intentionally
// permissive: it does not prove the closure is invoked. Only closures whose
// definition appears before waitPos are considered, since a closure defined
// later cannot have run before the Wait.
func (wga *Analyzer) hasAddInLocalClosure(target types.Object, waitPos token.Pos) bool {
	if wga.function == nil || wga.function.Body == nil || target == nil {
		return false
	}

	found := false
	funcLitDepth := 0
	astutil.Apply(wga.function.Body, func(c *astutil.Cursor) bool {
		if found {
			return false
		}
		node := c.Node()
		if node == nil {
			return true
		}
		if fnLit, ok := node.(*ast.FuncLit); ok {
			if fnLit.Pos() >= waitPos {
				return false
			}
			funcLitDepth++
			return true
		}
		call, ok := node.(*ast.CallExpr)
		if !ok || funcLitDepth == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Add" {
			return true
		}
		if wga.exprReferencesObject(sel.X, target) {
			found = true
			return false
		}
		return true
	}, func(c *astutil.Cursor) bool {
		if _, ok := c.Node().(*ast.FuncLit); ok {
			funcLitDepth--
		}
		return true
	})
	return found
}

// isWaitGroupPassedToOtherFunctionsForWait reports whether the WaitGroup
// referred to by target is referenced (passed, assigned, returned, etc.)
// somewhere in the enclosing function before waitPos. References after the
// Wait cannot supply its missing Add.
func (wga *Analyzer) isWaitGroupPassedToOtherFunctionsForWait(target types.Object, waitPos token.Pos) bool {
	if wga.function == nil || wga.function.Body == nil || target == nil {
		return false
	}

	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found || n == nil || n.Pos() >= waitPos {
			return false
		}
		switch node := n.(type) {
		case *ast.CallExpr:
			for _, arg := range node.Args {
				if wga.exprReferencesObject(arg, target) {
					found = true
					return false
				}
			}
		case *ast.AssignStmt:
			for _, rhs := range node.Rhs {
				if wga.exprReferencesObject(rhs, target) {
					found = true
					return false
				}
			}
		case *ast.ValueSpec:
			for _, value := range node.Values {
				if wga.exprReferencesObject(value, target) {
					found = true
					return false
				}
			}
		case *ast.ReturnStmt:
			for _, result := range node.Results {
				if wga.exprReferencesObject(result, target) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func (wga *Analyzer) waitGroupReceiverObjectAt(wgName, method string, pos token.Pos) types.Object {
	if wga.function == nil || wga.function.Body == nil {
		return nil
	}
	var obj types.Object
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if obj != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Pos() != pos {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != method || common.GetVarName(sel.X) != wgName {
			return true
		}
		obj = wga.receiverObject(sel.X)
		return false
	})
	return obj
}

func (wga *Analyzer) exprReferencesObject(expr ast.Expr, target types.Object) bool {
	if expr == nil || target == nil {
		return false
	}
	if obj := wga.receiverObject(expr); obj != nil {
		return obj == target
	}

	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		exprNode, ok := n.(ast.Expr)
		if !ok {
			return true
		}
		if obj := wga.receiverObject(exprNode); obj != nil && obj == target {
			found = true
			return false
		}
		return true
	})
	return found
}

func (wga *Analyzer) receiverObject(expr ast.Expr) types.Object {
	switch e := expr.(type) {
	case *ast.Ident:
		if wga.typesInfo == nil {
			return nil
		}
		return wga.typesInfo.ObjectOf(e)
	case *ast.ParenExpr:
		return wga.receiverObject(e.X)
	case *ast.UnaryExpr:
		if e.Op == token.AND || e.Op == token.MUL {
			return wga.receiverObject(e.X)
		}
	}
	return nil
}

func (wga *Analyzer) waitGroupInitializedFromAnother(wgName string) bool {
	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name != wgName || i >= len(node.Rhs) {
					continue
				}
				if wga.isWaitGroupAliasedOrCopiedExpr(node.Rhs[i]) {
					found = true
					return false
				}
			}
		case *ast.ValueSpec:
			for i, name := range node.Names {
				if name.Name != wgName || i >= len(node.Values) {
					continue
				}
				if wga.isWaitGroupAliasedOrCopiedExpr(node.Values[i]) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// isWaitGroupAliasedOrCopiedExpr reports whether expr initializes a local
// WaitGroup handle from another WaitGroup, either by value or by address.
func (wga *Analyzer) isWaitGroupAliasedOrCopiedExpr(expr ast.Expr) bool {
	if expr == nil {
		return false
	}
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return false
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return wga.isWaitGroupFieldExpr(e.X)
		}
		return wga.isWaitGroupAliasedOrCopiedExpr(e.X)
	}
	return common.IsWaitGroup(wga.typesInfo.TypeOf(expr))
}

func (wga *Analyzer) isWaitGroupFieldExpr(expr ast.Expr) bool {
	_, ok := expr.(*ast.SelectorExpr)
	return ok && common.IsWaitGroup(wga.typesInfo.TypeOf(expr))
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
						wga.errorCollector.AddError(addPos, category.AddWithoutDone,
							"waitgroup '"+wgName+"' has Add without corresponding Done")
					}
				}
			}

			return true
		})
	}
}
