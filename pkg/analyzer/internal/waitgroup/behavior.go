package waitgroup

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

func (wga *Checker) deferInvokesDone(deferStmt *ast.DeferStmt, wgName string) bool {
	if wga.isSimpleDeferDone(deferStmt, wgName) || wga.isCallbackDeferDone(deferStmt, wgName) {
		return true
	}
	if fnLit, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok && fnLit.Body != nil {
		return wga.containsDoneCall(fnLit.Body, wgName)
	}
	return false
}

func (wga *Checker) checkDoneOutsideWorkerGoroutine() {
	for wgName := range wga.waitGroupNames {
		wga.checkDoneOutsideWorkerForWaitGroup(wgName)
	}
}

func (wga *Checker) checkDoneOutsideWorkerForWaitGroup(wgName string) {
	pendingAdds := 0
	// Keep this intentionally narrow: only an Add immediately followed by a
	// worker goroutine is treated as an ownership handoff.
	recentPositiveAdd := false
	workerGoroutinesWithoutDone := 0

	var (
		visitStmt  func(ast.Stmt)
		visitStmts func([]ast.Stmt)
	)

	visitStmts = func(stmts []ast.Stmt) {
		for _, stmt := range stmts {
			visitStmt(stmt)
		}
	}

	visitStmt = func(stmt ast.Stmt) {
		if stmt == nil || wga.commentFilter.ShouldSkipStatement(stmt) {
			return
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || wga.commentFilter.ShouldSkipCall(call) {
				return
			}
			if delta, ok := wga.addDelta(call, wgName); ok {
				if delta > 0 {
					pendingAdds += delta
					recentPositiveAdd = true
				}
				return
			}
			if wga.callInvokesDone(call, wgName) {
				recentPositiveAdd = false
				if workerGoroutinesWithoutDone > 0 {
					wga.errorCollector.AddError(call.Pos(), category.DoneOutsideGoroutine, "waitgroup '"+wgName+"' Done called outside worker goroutine")
					workerGoroutinesWithoutDone--
				}
				if pendingAdds > 0 {
					pendingAdds--
				}
			}
		case *ast.DeferStmt:
			recentPositiveAdd = false
			if wga.deferInvokesDone(s, wgName) {
				if workerGoroutinesWithoutDone > 0 {
					wga.errorCollector.AddError(s.Call.Pos(), category.DoneOutsideGoroutine, "waitgroup '"+wgName+"' Done called outside worker goroutine")
					workerGoroutinesWithoutDone--
				}
				if pendingAdds > 0 {
					pendingAdds--
				}
			}
		case *ast.GoStmt:
			if pendingAdds <= 0 || !recentPositiveAdd {
				recentPositiveAdd = false
				return
			}
			recentPositiveAdd = false
			doneInfo, related := wga.goroutineDoneInfo(s, wgName)
			if related && doneInfo.hasAnyDone {
				return
			}
			if wga.goroutineOnlyWaitsOnWaitGroup(s, wgName) {
				return
			}
			workerGoroutinesWithoutDone++
		case *ast.AssignStmt, *ast.DeclStmt, *ast.ReturnStmt:
			recentPositiveAdd = false
		case *ast.IfStmt:
			visitStmt(s.Init)
			visitStmts(s.Body.List)
			wga.visitMainFlowElse(s.Else, visitStmt, visitStmts)
			recentPositiveAdd = false
		case *ast.ForStmt:
			visitStmt(s.Init)
			visitStmts(s.Body.List)
			visitStmt(s.Post)
			recentPositiveAdd = false
		case *ast.RangeStmt:
			visitStmts(s.Body.List)
			recentPositiveAdd = false
		case *ast.BlockStmt:
			visitStmts(s.List)
		case *ast.LabeledStmt:
			visitStmt(s.Stmt)
		case *ast.SwitchStmt:
			visitStmt(s.Init)
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					visitStmts(cc.Body)
				}
			}
			recentPositiveAdd = false
		case *ast.TypeSwitchStmt:
			visitStmt(s.Init)
			visitStmt(s.Assign)
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					visitStmts(cc.Body)
				}
			}
			recentPositiveAdd = false
		case *ast.SelectStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CommClause); ok {
					visitStmt(cc.Comm)
					visitStmts(cc.Body)
				}
			}
			recentPositiveAdd = false
		}
	}

	if wga.function.Body != nil {
		visitStmts(wga.function.Body.List)
	}
}

func (wga *Checker) visitMainFlowElse(stmt ast.Stmt, visitStmt func(ast.Stmt), visitStmts func([]ast.Stmt)) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		visitStmts(s.List)
	case *ast.IfStmt:
		visitStmt(s)
	}
}

func (wga *Checker) addDelta(call *ast.CallExpr, wgName string) (int, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Add" || common.GetVarName(sel.X) != wgName {
		return 0, false
	}

	addValue := common.GetAddValue(call)
	if len(call.Args) > 0 {
		if constantValue, ok := common.ConstantIntValue(call.Args[0], wga.typesInfo); ok {
			addValue = constantValue
		}
	}
	return addValue, true
}

func (wga *Checker) checkMultipleDoneSameWorkerBranch() {
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

func (wga *Checker) checkMultipleDoneInBranch(stmts []ast.Stmt, wgName string, count int) int {
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
					wga.errorCollector.AddError(s.Call.Pos(), category.MultipleDoneWorker, "waitgroup '"+wgName+"' Done called multiple times in the same worker branch")
				}
			}
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok && wga.callInvokesDone(call, wgName) {
				current++
				if current > 1 {
					wga.errorCollector.AddError(call.Pos(), category.MultipleDoneWorker, "waitgroup '"+wgName+"' Done called multiple times in the same worker branch")
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

func (wga *Checker) checkMultipleDoneInElse(stmt ast.Stmt, wgName string, count int) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		wga.checkMultipleDoneInBranch(s.List, wgName, count)
	case *ast.IfStmt:
		wga.checkMultipleDoneInBranch([]ast.Stmt{s}, wgName, count)
	}
}

func (wga *Checker) checkNestedWaitGroupDeadlock() {
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

func (wga *Checker) workerDoneWaitGroups(body *ast.BlockStmt) map[string]bool {
	outerGroups := make(map[string]bool)
	for wgName := range wga.waitGroupNames {
		if wga.analyzeDoneCallsWithVisited(body, wgName, make(map[token.Pos]bool)).hasAnyDone {
			outerGroups[wgName] = true
		}
	}
	return outerGroups
}

func (wga *Checker) reportNestedWaitsForWorker(goStmt *ast.GoStmt, body *ast.BlockStmt, outerGroups map[string]bool) {
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
			if wga.workerReleasesWaitGroupBefore(body, outerWG, call.Pos()) {
				continue
			}
			if wga.outerWaitBeforeInnerRelease(goStmt.Pos(), outerWG, innerWG) {
				wga.errorCollector.AddError(call.Pos(), category.NestedWaitGroupDeadlock,
					"waitgroup '"+innerWG+"' Wait inside worker for waitgroup '"+outerWG+"' can deadlock")
				return false
			}
		}
		return true
	})
}

func (wga *Checker) workerReleasesWaitGroupBefore(body *ast.BlockStmt, wgName string, before token.Pos) bool {
	if body == nil {
		return false
	}
	for _, stmt := range body.List {
		if stmt == nil || stmt.Pos() >= before {
			break
		}
		if wga.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		if wga.topLevelStatementReleasesWaitGroup(stmt, wgName) {
			return true
		}
	}
	return false
}

func (wga *Checker) topLevelStatementReleasesWaitGroup(stmt ast.Stmt, wgName string) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		return ok && wga.callReleasesWaitGroup(call, wgName)
	case *ast.LabeledStmt:
		return wga.topLevelStatementReleasesWaitGroup(s.Stmt, wgName)
	default:
		return false
	}
}

func (wga *Checker) callReleasesWaitGroup(call *ast.CallExpr, wgName string) bool {
	if call == nil || wga.commentFilter.ShouldSkipCall(call) {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || common.GetVarName(sel.X) != wgName {
		return false
	}
	if sel.Sel.Name == "Done" {
		return true
	}
	if sel.Sel.Name != "Add" {
		return false
	}
	delta, ok := wga.addDelta(call, wgName)
	return ok && delta < 0
}

func (wga *Checker) outerWaitBeforeInnerRelease(goPos token.Pos, outerWG, innerWG string) bool {
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
