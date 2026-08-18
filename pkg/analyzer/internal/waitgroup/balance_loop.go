package waitgroup

import (
	"go/ast"
	"go/token"
	"strconv"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

// checkLoopAddDoneBalance checks for Add/Done balance issues in loops
func (b *balanceValidator) checkLoopAddDoneBalance() {
	ast.Inspect(b.function.Body, func(n ast.Node) bool {
		if forStmt, ok := n.(*ast.ForStmt); ok {
			b.analyzeLoopBalance(forStmt)
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
func (b *balanceValidator) analyzeLoopBalance(forStmt *ast.ForStmt) {
	loopStats := make(map[string]*loopAnalysis)

	ast.Inspect(forStmt.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.GoStmt:
			b.recordLoopGoroutineDone(node, forStmt.Body, loopStats)
			return false
		case *ast.ExprStmt:
			b.recordLoopExprCall(node, forStmt.Body, loopStats)
		}
		return true
	})

	for wgName, stats := range loopStats {
		if len(stats.addCalls) > 0 {
			if stats.unconditionalDones == 0 && stats.conditionalDones > 0 {
				for _, addPos := range stats.addCalls {
					if b.loopAddCoveredByLaterDoneInSameStatementList(forStmt.Body, addPos, wgName) {
						continue
					}
					if b.addIsHandedOffThroughChannel(wgName, addPos) {
						continue
					}
					b.reporter.AddError(addPos, category.AddWithoutDone,
						"waitgroup '"+wgName+"' has Add without corresponding Done")
				}
			}
		}
	}
}

func (b *balanceValidator) loopAnalysisFor(loopStats map[string]*loopAnalysis, wgName string) *loopAnalysis {
	if loopStats[wgName] == nil {
		loopStats[wgName] = &loopAnalysis{}
	}
	return loopStats[wgName]
}

func (b *balanceValidator) recordLoopExprCall(stmt *ast.ExprStmt, scope ast.Node, loopStats map[string]*loopAnalysis) {
	call, ok := stmt.X.(*ast.CallExpr)
	if !ok {
		return
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	wgName := common.GetVarName(sel.X)
	if !b.waitGroupNames[wgName] {
		return
	}

	stats := b.loopAnalysisFor(loopStats, wgName)
	switch sel.Sel.Name {
	case "Add":
		stats.addCalls = append(stats.addCalls, call.Pos())
	case "Done":
		if b.isInConditional(call, scope) {
			stats.conditionalDones++
		} else {
			stats.unconditionalDones++
		}
	}
}

func (b *balanceValidator) recordLoopGoroutineDone(goStmt *ast.GoStmt, scope ast.Node, loopStats map[string]*loopAnalysis) {
	for wgName := range b.waitGroupNames {
		doneInfo, related := b.goroutineDoneInfo(goStmt, wgName)
		if !related || !doneInfo.hasAnyDone {
			continue
		}
		stats := b.loopAnalysisFor(loopStats, wgName)
		if b.isInConditional(goStmt, scope) {
			stats.conditionalDones++
		} else {
			stats.unconditionalDones++
		}
	}
}

func (b *balanceValidator) loopAddCoveredByLaterDoneInSameStatementList(scope *ast.BlockStmt, addPos token.Pos, wgName string) bool {
	stmts, index, ok := innermostStatementListContaining(scope, addPos)
	if !ok {
		return false
	}
	for _, stmt := range stmts[index+1:] {
		if b.statementProvidesLoopDone(stmt, wgName) {
			return true
		}
	}
	return false
}

func innermostStatementListContaining(scope ast.Node, pos token.Pos) ([]ast.Stmt, int, bool) {
	var (
		foundStmts []ast.Stmt
		foundIndex int
		found      bool
	)

	ast.Inspect(scope, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		stmts := stmtListOf(n)
		for i, stmt := range stmts {
			if nodeContainsPos(stmt, pos) {
				foundStmts = stmts
				foundIndex = i
				found = true
				break
			}
		}
		return true
	})

	return foundStmts, foundIndex, found
}

func (b *balanceValidator) statementProvidesLoopDone(stmt ast.Stmt, wgName string) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		return ok && b.callInvokesDone(call, wgName)
	case *ast.DeferStmt:
		return b.isSimpleDeferDone(s, wgName)
	case *ast.GoStmt:
		doneInfo, related := b.goroutineDoneInfo(s, wgName)
		return related && doneInfo.hasGuaranteedDone
	case *ast.BlockStmt:
		for _, nested := range s.List {
			if b.statementProvidesLoopDone(nested, wgName) {
				return true
			}
		}
	case *ast.LabeledStmt:
		return b.statementProvidesLoopDone(s.Stmt, wgName)
	}
	return false
}

// isInConditional checks if a node is inside an if statement
func (b *balanceValidator) isInConditional(target ast.Node, scope ast.Node) bool {
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

func (b *balanceValidator) isInBranchingControlFlow(pos token.Pos) bool {
	inBranch := false

	ast.Inspect(b.function.Body, func(n ast.Node) bool {
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

func (b *balanceValidator) checkLiteralAddLoopGoroutineMismatch(stats map[string]*Stats) {
	for wgName, st := range stats {
		var positiveAdds []addCall
		for _, add := range st.addCalls {
			// Non-constant Add values fall back to 1; counting them would
			// wrongly flag wg.Add(concurrency) against the launched goroutines.
			if add.known && add.value > 0 && !b.isInGoroutine(add.pos) {
				positiveAdds = append(positiveAdds, add)
			}
		}
		if len(positiveAdds) != 1 {
			continue
		}
		// A Wait() closes the Add…Wait lifecycle: goroutines launched after it
		// belong to a separate cycle and must not be counted against this Add.
		boundary := b.nextWaitAfter(st, positiveAdds[0].pos)
		launched := b.countLoopWorkerGoroutinesBetween(positiveAdds[0].pos, boundary, wgName)
		if launched <= 1 || launched == positiveAdds[0].value {
			continue
		}
		b.reporter.AddError(positiveAdds[0].pos, category.AddLoopCountMismatch,
			"waitgroup '"+wgName+"' Add count "+strconv.Itoa(positiveAdds[0].value)+" does not match "+strconv.Itoa(launched)+" goroutines launched")
	}
}

// nextWaitAfter returns the position of the earliest Wait() on this WaitGroup
// that occurs after `after`, or token.NoPos if there is none. A Wait() ends the
// current Add…Wait lifecycle, so the loop-count check must not look past it.
func (b *balanceValidator) nextWaitAfter(st *Stats, after token.Pos) token.Pos {
	next := token.NoPos
	for _, w := range st.waitCalls {
		if w > after && (next == token.NoPos || w < next) {
			next = w
		}
	}
	return next
}

func (b *balanceValidator) countLoopWorkerGoroutinesBetween(after, boundary token.Pos, wgName string) int {
	total := 0
	ast.Inspect(b.function.Body, func(n ast.Node) bool {
		switch loop := n.(type) {
		case *ast.ForStmt:
			if !b.loopInWindow(loop.Pos(), after, boundary) {
				return true
			}
			iterations := b.estimateForIterations(loop)
			if iterations <= 1 {
				return true
			}
			total += iterations * b.countWorkerGoroutines(loop.Body, wgName)
		case *ast.RangeStmt:
			if !b.loopInWindow(loop.Pos(), after, boundary) {
				return true
			}
			iterations := b.estimateRangeIterationsForMismatch(loop)
			if iterations <= 1 {
				return true
			}
			total += iterations * b.countWorkerGoroutines(loop.Body, wgName)
		}
		return true
	})
	return total
}

// loopInWindow reports whether a loop starting at pos lies strictly after the
// Add (`after`) and before the lifecycle-closing Wait (`boundary`). A NoPos
// boundary means there is no trailing Wait, so the window is open-ended.
func (b *balanceValidator) loopInWindow(pos, after, boundary token.Pos) bool {
	if pos <= after {
		return false
	}
	return boundary == token.NoPos || pos < boundary
}

func (b *balanceValidator) estimateRangeIterationsForMismatch(rangeStmt *ast.RangeStmt) int {
	if rangeStmt == nil || rangeStmt.X == nil {
		return 1
	}
	if lit, ok := rangeStmt.X.(*ast.CompositeLit); ok {
		return len(lit.Elts)
	}
	return b.estimateRangeIterations(rangeStmt)
}

func (b *balanceValidator) countWorkerGoroutines(body *ast.BlockStmt, wgName string) int {
	if body == nil {
		return 0
	}
	count := 0
	ast.Inspect(body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		doneInfo, related := b.goroutineDoneInfo(goStmt, wgName)
		if related && doneInfo.hasAnyDone {
			count++
		}
		return true
	})
	return count
}
