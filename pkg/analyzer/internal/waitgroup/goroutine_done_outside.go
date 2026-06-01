package waitgroup

import (
	"go/ast"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

func (g *goroutineInspector) checkDoneOutsideWorkerGoroutine(fn *ast.FuncDecl) {
	if g == nil || fn == nil || fn.Body == nil {
		return
	}

	for wgName := range g.waitGroupNames {
		g.checkDoneOutsideWorkerForWaitGroup(fn, wgName)
	}
}

func (g *goroutineInspector) checkDoneOutsideWorkerForWaitGroup(fn *ast.FuncDecl, wgName string) {
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
		if stmt == nil || g.shouldSkipStatement(stmt) {
			return
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || g.shouldSkipCall(call) {
				return
			}
			if delta, ok := g.addDelta(call, wgName); ok {
				if delta > 0 {
					pendingAdds += delta
					recentPositiveAdd = true
				}
				return
			}
			if g.callInvokesDone != nil && g.callInvokesDone(call, wgName) {
				recentPositiveAdd = false
				if workerGoroutinesWithoutDone > 0 {
					g.reporter.AddError(call.Pos(), category.DoneOutsideGoroutine, "waitgroup '"+wgName+"' Done called outside worker goroutine")
					workerGoroutinesWithoutDone--
				}
				if pendingAdds > 0 {
					pendingAdds--
				}
			}
		case *ast.DeferStmt:
			recentPositiveAdd = false
			if g.deferInvokesDone != nil && g.deferInvokesDone(s, wgName) {
				if workerGoroutinesWithoutDone > 0 {
					g.reporter.AddError(s.Call.Pos(), category.DoneOutsideGoroutine, "waitgroup '"+wgName+"' Done called outside worker goroutine")
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
			if g.goroutineDoneInfo != nil {
				doneInfo, related := g.goroutineDoneInfo(s, wgName)
				if related && doneInfo.hasAnyDone {
					return
				}
			}
			if g.goroutineOnlyWaits != nil && g.goroutineOnlyWaits(s, wgName) {
				return
			}
			workerGoroutinesWithoutDone++
		case *ast.AssignStmt, *ast.DeclStmt, *ast.ReturnStmt:
			recentPositiveAdd = false
		case *ast.IfStmt:
			visitStmt(s.Init)
			visitStmts(s.Body.List)
			visitMainFlowElse(s.Else, visitStmt, visitStmts)
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

	visitStmts(fn.Body.List)
}

func (g *goroutineInspector) checkAddInsideGoroutine(fn *ast.FuncDecl) {
	if g == nil || fn == nil || fn.Body == nil {
		return
	}

	mainFlowWaits := make(map[string]bool)
	hasMainFlowWait := func(wgName string) bool {
		if cached, ok := mainFlowWaits[wgName]; ok {
			return cached
		}
		found := g.hasMainFlowWait(fn, wgName)
		mainFlowWaits[wgName] = found
		return found
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || g.shouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}

		ast.Inspect(fnLit.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok || g.shouldSkipCall(call) {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Add" {
				return true
			}
			wgName := common.GetVarName(sel.X)
			if !g.waitGroupNames[wgName] || g.waitGroupIdentDefinedInside(fnLit.Body, sel.X) {
				return true
			}
			if !hasMainFlowWait(wgName) {
				return true
			}
			g.reporter.AddError(call.Pos(), category.AddInsideGoroutine, "waitgroup '"+wgName+"' Add called inside goroutine, may race with Wait")
			return true
		})

		return true
	})
}

func (g *goroutineInspector) addDelta(call *ast.CallExpr, wgName string) (int, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Add" || common.GetVarName(sel.X) != wgName {
		return 0, false
	}

	addValue := common.GetAddValue(call)
	if len(call.Args) > 0 {
		if constantValue, ok := common.ConstantIntValue(call.Args[0], g.typesInfo); ok {
			addValue = constantValue
		}
	}
	return addValue, true
}

func (g *goroutineInspector) checkMultipleDoneSameWorkerBranch(fn *ast.FuncDecl) {
	if g == nil || fn == nil || fn.Body == nil {
		return
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || g.shouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}
		for wgName := range g.waitGroupNames {
			g.checkMultipleDoneInBranch(fnLit.Body.List, wgName, 0)
		}
		return true
	})
}

func (g *goroutineInspector) checkMultipleDoneInBranch(stmts []ast.Stmt, wgName string, count int) int {
	current := count
	for _, stmt := range stmts {
		if stmt == nil || g.shouldSkipStatement(stmt) {
			continue
		}
		switch s := stmt.(type) {
		case *ast.DeferStmt:
			if g.deferInvokesDone != nil && g.deferInvokesDone(s, wgName) {
				current++
				if current > 1 {
					g.reporter.AddError(s.Call.Pos(), category.MultipleDoneWorker, "waitgroup '"+wgName+"' Done called multiple times in the same worker branch")
				}
			}
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok && g.callInvokesDone != nil && g.callInvokesDone(call, wgName) {
				current++
				if current > 1 {
					g.reporter.AddError(call.Pos(), category.MultipleDoneWorker, "waitgroup '"+wgName+"' Done called multiple times in the same worker branch")
				}
			}
		case *ast.IfStmt:
			g.checkMultipleDoneInBranch(s.Body.List, wgName, current)
			if s.Else != nil {
				g.checkMultipleDoneInElse(s.Else, wgName, current)
			}
		case *ast.BlockStmt:
			current = g.checkMultipleDoneInBranch(s.List, wgName, current)
		case *ast.LabeledStmt:
			current = g.checkMultipleDoneInBranch([]ast.Stmt{s.Stmt}, wgName, current)
		}
	}
	return current
}

func (g *goroutineInspector) checkMultipleDoneInElse(stmt ast.Stmt, wgName string, count int) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		g.checkMultipleDoneInBranch(s.List, wgName, count)
	case *ast.IfStmt:
		g.checkMultipleDoneInBranch([]ast.Stmt{s}, wgName, count)
	}
}
