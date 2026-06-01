package waitgroup

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"sort"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

type balanceValidatorConfig struct {
	function                     *ast.FuncDecl
	waitGroupNames               map[string]bool
	localWaitGroupNames          map[string]bool
	commentFilter                *commentfilter.CommentFilter
	reporter                     report.Reporter
	typesInfo                    *types.Info
	escape                       *escapeAnalyzer
	isInGoroutine                inGoroutineChecker
	isNodeInGoroutine            func(ast.Node) bool
	callInvokesDone              doneCallChecker
	goroutineDoneInfo            goroutineDoneAnalyzer
	isSimpleDeferDone            deferDoneDetector
	findRelatedAddCall           func(*ast.GoStmt, string) token.Pos
	hasUnreachableDone           func(*ast.BlockStmt, string) bool
	waitInEarlyExitBranch        func(token.Pos) bool
	estimateForIterations        func(*ast.ForStmt) int
	estimateForIterationsKnown   func(*ast.ForStmt) (int, bool)
	estimateRangeIterations      func(*ast.RangeStmt) int
	estimateRangeIterationsKnown func(*ast.RangeStmt) (int, bool)
}

// balanceValidator groups checks that compare WaitGroup task starts (Add/Go)
// with releases (Done/defer Done) and waits in the current function.
type balanceValidator struct {
	balanceValidatorConfig
}

func newBalanceValidator(config balanceValidatorConfig) *balanceValidator {
	return &balanceValidator{balanceValidatorConfig: config}
}

func (b *balanceValidator) validateBalance(wgName string, stats *Stats) {
	// Count Done calls from main flow (not in goroutines)
	mainFlowDoneCount := b.countMainFlowDoneCalls(wgName)

	totalDone := mainFlowDoneCount

	for _, deferDonePos := range stats.deferDoneCalls {
		if !b.isInGoroutine(deferDonePos) {
			totalDone++
		}
	}

	// Add guaranteed Done calls from goroutines (but don't double count)
	guaranteedFromGoroutines := b.countGuaranteedDoneInGoroutines(wgName)
	totalDone += guaranteedFromGoroutines

	if stats.totalAdd > totalDone {
		b.reportUnmatchedAdds(wgName, stats, totalDone)
	}

	if totalDone > stats.totalAdd {
		b.reportExcessDones(wgName, stats, totalDone, mainFlowDoneCount)
	}
}

func (b *balanceValidator) countMainFlowDoneCalls(wgName string) int {
	if b.function == nil || b.function.Body == nil {
		return 0
	}
	return b.countMainFlowDoneInStatements(b.function.Body.List, wgName, 1)
}

func (b *balanceValidator) countMainFlowDoneInStatements(stmts []ast.Stmt, wgName string, multiplier int) int {
	count := 0
	for _, stmt := range stmts {
		if stmt == nil || b.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if ok && b.callInvokesDone(call, wgName) {
				count += multiplier
			}
		case *ast.GoStmt:
			continue
		case *ast.BlockStmt:
			count += b.countMainFlowDoneInStatements(s.List, wgName, multiplier)
		case *ast.IfStmt:
			count += b.countMainFlowDoneInStatements(s.Body.List, wgName, multiplier)
			if s.Else != nil {
				count += b.countMainFlowDoneInElse(s.Else, wgName, multiplier)
			}
		case *ast.ForStmt:
			count += b.countMainFlowDoneInStatements(s.Body.List, wgName, multiplier*b.estimateForIterations(s))
		case *ast.RangeStmt:
			count += b.countMainFlowDoneInStatements(s.Body.List, wgName, multiplier*b.estimateRangeIterations(s))
		case *ast.SwitchStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					count += b.countMainFlowDoneInStatements(cc.Body, wgName, multiplier)
				}
			}
		case *ast.TypeSwitchStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					count += b.countMainFlowDoneInStatements(cc.Body, wgName, multiplier)
				}
			}
		case *ast.SelectStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CommClause); ok {
					count += b.countMainFlowDoneInStatements(cc.Body, wgName, multiplier)
				}
			}
		case *ast.LabeledStmt:
			count += b.countMainFlowDoneInStatements([]ast.Stmt{s.Stmt}, wgName, multiplier)
		}
	}
	return count
}

func (b *balanceValidator) countMainFlowDoneInElse(stmt ast.Stmt, wgName string, multiplier int) int {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return b.countMainFlowDoneInStatements(s.List, wgName, multiplier)
	case *ast.IfStmt:
		return b.countMainFlowDoneInStatements([]ast.Stmt{s}, wgName, multiplier)
	default:
		return 0
	}
}

// countGuaranteedDoneInGoroutines counts Done calls that are guaranteed to execute in goroutines
func (b *balanceValidator) countGuaranteedDoneInGoroutines(wgName string) int {
	return b.countGuaranteedDoneInStatements(b.function.Body.List, wgName, 1)
}

// checkWaitGroupBalance validates that Add and Done calls are properly balanced
func (b *balanceValidator) checkWaitGroupBalance(stats map[string]*Stats) {
	for wgName, st := range stats {
		if b.isBorrowedWaitGroupField(wgName, st) {
			continue
		}
		if b.isLikelyExternalLifecycleWaitGroup(wgName, st) {
			continue
		}
		if b.escape != nil && b.escape.isWaitGroupPassedToOtherFunctions(wgName) {
			if len(st.addCalls) > 0 {
				continue
			}
		}
		b.validateBalance(wgName, st)
	}
}

func (b *balanceValidator) isBorrowedWaitGroupField(wgName string, st *Stats) bool {
	return strings.Contains(wgName, ".") && st.totalAdd == 0 && len(st.waitCalls) == 0 && (st.doneCount > 0 || len(st.deferDoneCalls) > 0)
}

func (b *balanceValidator) isLikelyExternalLifecycleWaitGroup(wgName string, st *Stats) bool {
	if !strings.Contains(wgName, ".") {
		return false
	}
	if st.totalAdd == 0 || st.doneCount > 0 || len(st.deferDoneCalls) > 0 || len(st.waitCalls) > 0 || len(st.goCalls) > 0 {
		return false
	}
	return true
}

func (b *balanceValidator) countGuaranteedDoneInStatements(stmts []ast.Stmt, wgName string, multiplier int) int {
	count := 0

	for _, stmt := range stmts {
		if b.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.GoStmt:
			if fnLit, ok := s.Call.Fun.(*ast.FuncLit); ok {
				nestedCount := b.countGuaranteedDoneInStatements(fnLit.Body.List, wgName, multiplier)
				if nestedCount > 0 {
					count += nestedCount
					continue
				}
			}

			doneInfo, related := b.goroutineDoneInfo(s, wgName)
			if !related {
				continue
			}
			if doneInfo.hasGuaranteedDone {
				count += multiplier
				continue
			}
			if b.goroutineOnlyWaitsOnWaitGroup(s, wgName) {
				continue
			}
			if !doneInfo.hasAnyDone {
				relatedAdd := b.findRelatedAddCall(s, wgName)
				if relatedAdd != token.NoPos {
					b.reporter.AddError(relatedAdd, category.AddWithoutDone,
						"waitgroup '"+wgName+"' has Add without corresponding Done")
				}
			}

		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			if fnLit, ok := call.Fun.(*ast.FuncLit); ok && fnLit.Body != nil {
				count += b.countGuaranteedDoneInStatements(fnLit.Body.List, wgName, multiplier)
			}

		case *ast.BlockStmt:
			count += b.countGuaranteedDoneInStatements(s.List, wgName, multiplier)

		case *ast.IfStmt:
			count += b.countGuaranteedDoneInStatements(s.Body.List, wgName, multiplier)
			if elseBlock, ok := s.Else.(*ast.BlockStmt); ok {
				count += b.countGuaranteedDoneInStatements(elseBlock.List, wgName, multiplier)
			} else if elseIf, ok := s.Else.(*ast.IfStmt); ok {
				count += b.countGuaranteedDoneInStatements([]ast.Stmt{elseIf}, wgName, multiplier)
			}

		case *ast.ForStmt:
			factor := multiplier * b.estimateForIterations(s)
			count += b.countGuaranteedDoneInStatements(s.Body.List, wgName, factor)

		case *ast.RangeStmt:
			factor := multiplier * b.estimateRangeIterations(s)
			count += b.countGuaranteedDoneInStatements(s.Body.List, wgName, factor)

		case *ast.SwitchStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					count += b.countGuaranteedDoneInStatements(cc.Body, wgName, multiplier)
				}
			}

		case *ast.TypeSwitchStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					count += b.countGuaranteedDoneInStatements(cc.Body, wgName, multiplier)
				}
			}

		case *ast.SelectStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CommClause); ok {
					count += b.countGuaranteedDoneInStatements(cc.Body, wgName, multiplier)
				}
			}

		case *ast.LabeledStmt:
			count += b.countGuaranteedDoneInStatements([]ast.Stmt{s.Stmt}, wgName, multiplier)
		}
	}

	return count
}

// reportUnmatchedAdds reports Add calls that don't have corresponding Done calls
func (b *balanceValidator) reportUnmatchedAdds(wgName string, stats *Stats, totalExpectedDone int) {
	sort.Slice(stats.addCalls, func(i, j int) bool {
		return stats.addCalls[i].pos < stats.addCalls[j].pos
	})

	remainingDone := totalExpectedDone
	for _, addCall := range stats.addCalls {
		if remainingDone >= addCall.value {
			remainingDone -= addCall.value
		} else if !addCall.known && b.addCoveredByVariableDoneLoop(addCall.pos, wgName) {
			continue
		} else {
			b.reporter.AddError(addCall.pos, category.AddWithoutDone, "waitgroup '"+wgName+"' has Add without corresponding Done")
		}
	}
}

func (b *balanceValidator) addCoveredByVariableDoneLoop(addPos token.Pos, wgName string) bool {
	found := false
	ast.Inspect(b.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch loop := n.(type) {
		case *ast.ForStmt:
			if nodeContainsPos(loop.Body, addPos) && b.loopBodyHasVariableDoneWorker(loop.Body, addPos, wgName) {
				found = true
				return false
			}
		case *ast.RangeStmt:
			if nodeContainsPos(loop.Body, addPos) && b.loopBodyHasVariableDoneWorker(loop.Body, addPos, wgName) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func (b *balanceValidator) loopBodyHasVariableDoneWorker(body *ast.BlockStmt, after token.Pos, wgName string) bool {
	if body == nil {
		return false
	}
	for _, stmt := range body.List {
		if stmt == nil || stmt.Pos() <= after {
			continue
		}
		goStmt, ok := stmt.(*ast.GoStmt)
		if !ok {
			continue
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			continue
		}
		if b.blockHasVariableDoneLoop(fnLit.Body, wgName) {
			return true
		}
	}
	return false
}

func (b *balanceValidator) blockHasVariableDoneLoop(body *ast.BlockStmt, wgName string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch loop := n.(type) {
		case *ast.ForStmt:
			if _, ok := b.estimateForIterationsKnown(loop); ok {
				return true
			}
			if b.containsDoneCall(loop.Body, wgName) {
				found = true
				return false
			}
		case *ast.RangeStmt:
			if _, ok := b.estimateRangeIterationsKnown(loop); ok {
				return true
			}
			if b.containsDoneCall(loop.Body, wgName) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func (b *balanceValidator) containsDoneCall(stmt ast.Stmt, wgName string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if b.callInvokesDone(call, wgName) {
			found = true
			return false
		}
		return true
	})
	return found
}

// reportExcessDones reports Done calls that don't have corresponding Add calls
func (b *balanceValidator) reportExcessDones(wgName string, stats *Stats, totalExpectedDone int, _ int) {
	if totalExpectedDone <= stats.totalAdd {
		return
	}

	// Only report excess for main flow Done calls (not goroutine Done calls)
	var mainFlowDoneCalls []token.Pos
	for _, donePos := range stats.doneCalls {
		if !b.isInGoroutine(donePos) && !b.isInBranchingControlFlow(donePos) {
			mainFlowDoneCalls = append(mainFlowDoneCalls, donePos)
		}
	}

	if len(mainFlowDoneCalls) <= stats.totalAdd {
		return
	}

	slices.Sort(mainFlowDoneCalls)

	excessCount := len(mainFlowDoneCalls) - stats.totalAdd
	startIndex := len(mainFlowDoneCalls) - excessCount

	for i := startIndex; i < len(mainFlowDoneCalls) && i >= 0; i++ {
		b.reporter.AddError(mainFlowDoneCalls[i], category.DoneWithoutAdd, "waitgroup '"+wgName+"' has Done without corresponding Add")
	}
}
