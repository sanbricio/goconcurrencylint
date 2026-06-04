package waitgroup

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

// checkAddAfterWait detects Add calls that occur after Wait calls
func (b *balanceValidator) checkAddAfterWait(stats map[string]*Stats) {
	for wgName, st := range stats {
		b.checkAddAfterWaitInGoroutines(wgName, st)
		b.checkAddAfterWaitInMainFlow(wgName, st)
	}
}

func (b *balanceValidator) checkWaitBeforeDoneSameGoroutine(stats map[string]*Stats) {
	for wgName, st := range stats {
		for _, waitPos := range st.waitCalls {
			if b.isInGoroutine(waitPos) || b.hasRelatedGoroutineBeforeWait(wgName, waitPos) {
				continue
			}
			if b.pendingMainFlowAddsBeforeWait(st, waitPos) > 0 && b.hasMainFlowReleaseAfterWait(st, waitPos) {
				b.reporter.AddError(waitPos, category.WaitDeadlock, "waitgroup '"+wgName+"' waits with pending Add in the same goroutine")
			}
		}
	}
}

func (b *balanceValidator) pendingMainFlowAddsBeforeWait(st *Stats, waitPos token.Pos) int {
	pending := 0
	for _, add := range st.addCalls {
		if add.pos < waitPos && b.isInMainFunctionFlow(add.pos) {
			pending += add.value
		}
	}
	for _, done := range st.doneCalls {
		if done < waitPos && b.isInMainFunctionFlow(done) {
			pending--
		}
	}
	if pending < 0 {
		return 0
	}
	return pending
}

func (b *balanceValidator) hasMainFlowReleaseAfterWait(st *Stats, waitPos token.Pos) bool {
	for _, done := range st.doneCalls {
		if done > waitPos && b.isInMainFunctionFlow(done) {
			return true
		}
	}
	for _, deferDone := range st.deferDoneCalls {
		if deferDone < waitPos && b.isInMainFunctionFlow(deferDone) {
			return true
		}
	}
	return false
}

func (b *balanceValidator) isInMainFunctionFlow(pos token.Pos) bool {
	return !b.isInGoroutine(pos) && !b.isInNestedFunctionLiteral(pos)
}

func (b *balanceValidator) isInNestedFunctionLiteral(pos token.Pos) bool {
	found := false
	ast.Inspect(b.function.Body, func(n ast.Node) bool {
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

func (b *balanceValidator) hasRelatedGoroutineBeforeWait(wgName string, waitPos token.Pos) bool {
	found := false
	ast.Inspect(b.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || goStmt.Pos() > waitPos {
			return true
		}
		doneInfo, related := b.goroutineDoneInfo(goStmt, wgName)
		if related && doneInfo.hasAnyDone {
			found = true
			return false
		}
		return true
	})
	return found
}

// checkAddAfterWaitInGoroutines checks for Add after Wait in goroutines
func (b *balanceValidator) checkAddAfterWaitInGoroutines(wgName string, st *Stats) {
	for _, waitPos := range st.waitCalls {
		ast.Inspect(b.function.Body, func(n ast.Node) bool {
			if goStmt, ok := n.(*ast.GoStmt); ok {
				if goStmt.Pos() > waitPos {
					b.checkAddInGoroutine(goStmt, wgName)
				}
			}
			return true
		})
	}
}

// checkAddInGoroutine checks for Add calls within a specific goroutine
func (b *balanceValidator) checkAddInGoroutine(goStmt *ast.GoStmt, wgName string) {
	if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
		ast.Inspect(fnLit.Body, func(inner ast.Node) bool {
			if call, ok := inner.(*ast.CallExpr); ok {
				if b.commentFilter.ShouldSkipCall(call) {
					return true
				}

				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if common.GetVarName(sel.X) != wgName {
						return true
					}

					switch sel.Sel.Name {
					case "Add":
						b.reporter.AddError(call.Pos(), category.AddAfterWait, "waitgroup '"+wgName+"' Add called after Wait")
					case "Go":
						b.reporter.AddError(call.Pos(), category.GoAfterWait, "waitgroup '"+wgName+"' Go called after Wait")
					}
				}
			}
			return true
		})
	}
}

// checkAddAfterWaitInMainFlow detects Add calls in the main execution flow that occur after Wait
func (b *balanceValidator) checkAddAfterWaitInMainFlow(wgName string, st *Stats) {
	if strings.Contains(wgName, ".") {
		return
	}
	for _, wait := range st.waitCalls {
		// Early-exit Waits don't gate later Adds (their branch never returns
		// to the surrounding flow).
		if b.waitInEarlyExitBranch(wait) {
			continue
		}

		// Check if this Wait has any Add or Done operations before it in main flow
		hasOperationsBefore := false

		// Check for Add calls before this Wait (main flow only)
		for _, add := range st.addCalls {
			if add.pos < wait && !b.isInGoroutine(add.pos) {
				hasOperationsBefore = true
				break
			}
		}

		// Check for Done calls before this Wait (main flow only)
		if !hasOperationsBefore {
			for _, done := range st.doneCalls {
				if done < wait && !b.isInGoroutine(done) {
					hasOperationsBefore = true
					break
				}
			}
		}

		// WaitGroup.Go is also a task-starting operation and must be treated like Add
		// for the "empty Wait followed by new work" pattern.
		if !hasOperationsBefore {
			for _, goPos := range st.goCalls {
				if goPos < wait && !b.isInGoroutine(goPos) {
					hasOperationsBefore = true
					break
				}
			}
		}

		// If this is an "empty" Wait (no operations before it), flag subsequent Add calls
		if !hasOperationsBefore {
			for _, add := range st.addCalls {
				// Only report Add calls in main flow after this empty Wait
				if add.pos > wait && !b.isInGoroutine(add.pos) {
					if add.value == 1 && b.hasDeferredDoneAfter(wgName, add.pos) {
						continue
					}
					b.reporter.AddError(add.pos, category.AddAfterWait, "waitgroup '"+wgName+"' Add called after Wait")
				}
			}
			for _, goPos := range st.goCalls {
				if goPos > wait && !b.isInGoroutine(goPos) {
					b.reporter.AddError(goPos, category.GoAfterWait, "waitgroup '"+wgName+"' Go called after Wait")
				}
			}
		}
	}
}

func (b *balanceValidator) goroutineOnlyWaitsOnWaitGroup(goStmt *ast.GoStmt, wgName string) bool {
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

func (b *balanceValidator) hasDeferredDoneAfter(wgName string, after token.Pos) bool {
	found := false

	ast.Inspect(b.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}

		deferStmt, ok := n.(*ast.DeferStmt)
		if !ok || deferStmt.Pos() <= after {
			return true
		}
		if b.isNodeInGoroutine(deferStmt) {
			return true
		}
		if b.isSimpleDeferDone(deferStmt, wgName) {
			found = true
			return false
		}
		return true
	})

	return found
}
