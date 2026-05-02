package waitgroup

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
)

func (wga *Analyzer) checkAddInsideGoroutine() {
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || wga.commentFilter.ShouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}

		ast.Inspect(fnLit.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok || wga.commentFilter.ShouldSkipCall(call) {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Add" {
				return true
			}
			wgName := common.GetVarName(sel.X)
			if !wga.waitGroupNames[wgName] || wga.waitGroupIdentDefinedInside(fnLit.Body, sel.X) {
				return true
			}
			wga.errorCollector.AddError(call.Pos(), "waitgroup '"+wgName+"' Add called inside goroutine, may race with Wait")
			return true
		})

		return true
	})
}

func (wga *Analyzer) waitGroupIdentDefinedInside(body *ast.BlockStmt, expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok || body == nil || wga.typesInfo == nil {
		return false
	}
	obj := wga.typesInfo.Uses[ident]
	if obj == nil {
		obj = wga.typesInfo.Defs[ident]
	}
	return obj != nil && body.Pos() <= obj.Pos() && obj.Pos() <= body.End()
}

func (wga *Analyzer) checkDoneNotDeferredInWorker() {
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || wga.commentFilter.ShouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}
		for wgName := range wga.waitGroupNames {
			wga.checkDoneNotDeferredInBlock(fnLit.Body.List, wgName, false)
		}
		return true
	})
}

func (wga *Analyzer) checkDoneNotDeferredInBlock(stmts []ast.Stmt, wgName string, riskyBefore bool) bool {
	risky := riskyBefore
	for _, stmt := range stmts {
		if stmt == nil || wga.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		switch s := stmt.(type) {
		case *ast.DeferStmt:
			if wga.deferInvokesDone(s, wgName) {
				continue
			}
			if wga.statementMayPanic(s, wgName) {
				risky = true
			}
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok && wga.callInvokesDone(call, wgName) {
				if risky {
					wga.errorCollector.AddError(call.Pos(), "waitgroup '"+wgName+"' Done not deferred in goroutine, panic will skip Done and deadlock Wait")
				}
				continue
			}
			if wga.statementMayPanic(s, wgName) {
				risky = true
			}
		case *ast.IfStmt:
			thenRisky := wga.checkDoneNotDeferredInBlock(s.Body.List, wgName, risky)
			elseRisky := risky
			if s.Else != nil {
				elseRisky = wga.checkDoneNotDeferredInElse(s.Else, wgName, risky)
			}
			risky = thenRisky || elseRisky
		case *ast.BlockStmt:
			risky = wga.checkDoneNotDeferredInBlock(s.List, wgName, risky)
		case *ast.LabeledStmt:
			risky = wga.checkDoneNotDeferredInBlock([]ast.Stmt{s.Stmt}, wgName, risky)
		default:
			if wga.statementMayPanic(s, wgName) {
				risky = true
			}
		}
		if wga.isTerminatingStatement(stmt) {
			return risky
		}
	}
	return risky
}

func (wga *Analyzer) checkDoneNotDeferredInElse(stmt ast.Stmt, wgName string, risky bool) bool {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return wga.checkDoneNotDeferredInBlock(s.List, wgName, risky)
	case *ast.IfStmt:
		return wga.checkDoneNotDeferredInBlock([]ast.Stmt{s}, wgName, risky)
	default:
		return risky
	}
}

func (wga *Analyzer) statementMayPanic(stmt ast.Stmt, wgName string) bool {
	if _, ok := stmt.(*ast.GoStmt); ok {
		return false
	}
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || wga.commentFilter.ShouldSkipCall(call) {
			return true
		}
		if wga.isWaitGroupHousekeepingCall(call, wgName) {
			return true
		}
		found = true
		return false
	})
	return found
}

func (wga *Analyzer) isWaitGroupHousekeepingCall(call *ast.CallExpr, wgName string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || common.GetVarName(sel.X) != wgName {
		return false
	}
	switch sel.Sel.Name {
	case "Add", "Done", "Wait", "Go":
		return true
	default:
		return false
	}
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
		wga.errorCollector.AddError(positiveAdds[0].pos,
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
			wga.errorCollector.AddError(waitPos, "waitgroup '"+wgName+"' Wait called without any Add")
		}
	}
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
				if wga.isCopiedWaitGroupExpr(node.Rhs[i]) {
					found = true
					return false
				}
			}
		case *ast.ValueSpec:
			for i, name := range node.Names {
				if name.Name != wgName || i >= len(node.Values) {
					continue
				}
				if wga.isCopiedWaitGroupExpr(node.Values[i]) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func (wga *Analyzer) isCopiedWaitGroupExpr(expr ast.Expr) bool {
	if expr == nil {
		return false
	}
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return false
	case *ast.UnaryExpr:
		return e.Op != token.AND && wga.isCopiedWaitGroupExpr(e.X)
	}
	return common.IsWaitGroup(wga.typesInfo.TypeOf(expr))
}

func (wga *Analyzer) checkMultipleDoneSameWorkerBranch() {
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || wga.commentFilter.ShouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}
		for wgName := range wga.waitGroupNames {
			wga.checkMultipleDoneInBranch(fnLit.Body.List, wgName, 0)
		}
		return true
	})
}

func (wga *Analyzer) checkMultipleDoneInBranch(stmts []ast.Stmt, wgName string, count int) int {
	current := count
	for _, stmt := range stmts {
		if stmt == nil || wga.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		switch s := stmt.(type) {
		case *ast.DeferStmt:
			if wga.deferInvokesDone(s, wgName) {
				current++
				if current > 1 {
					wga.errorCollector.AddError(s.Call.Pos(), "waitgroup '"+wgName+"' Done called multiple times in the same worker branch")
				}
			}
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok && wga.callInvokesDone(call, wgName) {
				current++
				if current > 1 {
					wga.errorCollector.AddError(call.Pos(), "waitgroup '"+wgName+"' Done called multiple times in the same worker branch")
				}
			}
		case *ast.IfStmt:
			wga.checkMultipleDoneInBranch(s.Body.List, wgName, current)
			if s.Else != nil {
				wga.checkMultipleDoneInElse(s.Else, wgName, current)
			}
		case *ast.BlockStmt:
			current = wga.checkMultipleDoneInBranch(s.List, wgName, current)
		case *ast.LabeledStmt:
			current = wga.checkMultipleDoneInBranch([]ast.Stmt{s.Stmt}, wgName, current)
		}
	}
	return current
}

func (wga *Analyzer) checkMultipleDoneInElse(stmt ast.Stmt, wgName string, count int) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		wga.checkMultipleDoneInBranch(s.List, wgName, count)
	case *ast.IfStmt:
		wga.checkMultipleDoneInBranch([]ast.Stmt{s}, wgName, count)
	}
}

func (wga *Analyzer) checkNestedWaitGroupDeadlock() {
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || wga.commentFilter.ShouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}

		outerGroups := wga.workerDoneWaitGroups(fnLit.Body)
		if len(outerGroups) == 0 {
			return true
		}
		wga.reportNestedWaitsForWorker(goStmt, fnLit.Body, outerGroups)
		return true
	})
}

func (wga *Analyzer) workerDoneWaitGroups(body *ast.BlockStmt) map[string]bool {
	outerGroups := make(map[string]bool)
	for wgName := range wga.waitGroupNames {
		if wga.analyzeDoneCallsWithVisited(body, wgName, make(map[token.Pos]bool)).hasAnyDone {
			outerGroups[wgName] = true
		}
	}
	return outerGroups
}

func (wga *Analyzer) reportNestedWaitsForWorker(goStmt *ast.GoStmt, body *ast.BlockStmt, outerGroups map[string]bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || wga.commentFilter.ShouldSkipCall(call) {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Wait" {
			return true
		}
		innerWG := common.GetVarName(sel.X)
		if !wga.waitGroupNames[innerWG] {
			return true
		}
		for outerWG := range outerGroups {
			if outerWG == innerWG {
				continue
			}
			if wga.outerWaitBeforeInnerRelease(goStmt.Pos(), outerWG, innerWG) {
				wga.errorCollector.AddError(call.Pos(),
					"waitgroup '"+innerWG+"' Wait inside worker for waitgroup '"+outerWG+"' can deadlock")
				return false
			}
		}
		return true
	})
}

func (wga *Analyzer) outerWaitBeforeInnerRelease(goPos token.Pos, outerWG, innerWG string) bool {
	var outerWait token.Pos
	var innerRelease token.Pos
	hasInnerAdd := false

	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || wga.isInGoroutine(call.Pos()) {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		wgName := common.GetVarName(sel.X)
		switch {
		case wgName == innerWG && sel.Sel.Name == "Add" && call.Pos() < goPos:
			if delta, ok := wga.addDelta(call, innerWG); ok && delta > 0 {
				hasInnerAdd = true
			}
		case wgName == outerWG && sel.Sel.Name == "Wait" && call.Pos() > goPos && outerWait == token.NoPos:
			outerWait = call.Pos()
		case wgName == innerWG && sel.Sel.Name == "Done" && outerWait != token.NoPos && call.Pos() > outerWait && innerRelease == token.NoPos:
			innerRelease = call.Pos()
		case wgName == innerWG && sel.Sel.Name == "Add" && outerWait != token.NoPos && call.Pos() > outerWait && innerRelease == token.NoPos:
			if delta, ok := wga.addDelta(call, innerWG); ok && delta < 0 {
				innerRelease = call.Pos()
			}
		}
		return true
	})

	return hasInnerAdd && outerWait != token.NoPos && innerRelease != token.NoPos
}
