package waitgroup

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

func (g *goroutineInspector) checkNestedWaitGroupDeadlock(fn *ast.FuncDecl) {
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

		outerGroups := g.workerDoneWaitGroups(fnLit.Body)
		if len(outerGroups) == 0 {
			return true
		}
		g.reportNestedWaitsForWorker(fn, goStmt, fnLit.Body, outerGroups)
		return true
	})
}

func (g *goroutineInspector) hasMainFlowWait(fn *ast.FuncDecl, wgName string) bool {
	if fn == nil || fn.Body == nil {
		return false
	}

	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || !g.isMainFlow(call.Pos()) {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Wait" || common.GetVarName(sel.X) != wgName {
			return true
		}
		found = true
		return false
	})
	return found
}

func (g *goroutineInspector) waitGroupIdentDefinedInside(body *ast.BlockStmt, expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok || body == nil || g.typesInfo == nil {
		return false
	}
	obj := g.typesInfo.Uses[ident]
	if obj == nil {
		obj = g.typesInfo.Defs[ident]
	}
	return obj != nil && body.Pos() <= obj.Pos() && obj.Pos() <= body.End()
}

func (g *goroutineInspector) workerDoneWaitGroups(body *ast.BlockStmt) map[string]bool {
	outerGroups := make(map[string]bool)
	if g.analyzeDoneCalls == nil {
		return outerGroups
	}
	for wgName := range g.waitGroupNames {
		if g.analyzeDoneCalls(body, wgName, make(map[token.Pos]bool)).hasAnyDone {
			outerGroups[wgName] = true
		}
	}
	return outerGroups
}

func (g *goroutineInspector) reportNestedWaitsForWorker(fn *ast.FuncDecl, goStmt *ast.GoStmt, body *ast.BlockStmt, outerGroups map[string]bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || g.shouldSkipCall(call) {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Wait" {
			return true
		}
		innerWG := common.GetVarName(sel.X)
		if !g.waitGroupNames[innerWG] {
			return true
		}
		for outerWG := range outerGroups {
			if outerWG == innerWG {
				continue
			}
			if g.workerReleasesWaitGroupBefore(body, outerWG, call.Pos()) {
				continue
			}
			if g.outerWaitBeforeInnerRelease(fn, goStmt.Pos(), outerWG, innerWG) {
				g.reporter.AddError(call.Pos(), category.NestedWaitGroupDeadlock,
					"waitgroup '"+innerWG+"' Wait inside worker for waitgroup '"+outerWG+"' can deadlock")
				return false
			}
		}
		return true
	})
}

func (g *goroutineInspector) outerWaitBeforeInnerRelease(fn *ast.FuncDecl, goPos token.Pos, outerWG, innerWG string) bool {
	if fn == nil || fn.Body == nil {
		return false
	}

	var outerWait token.Pos
	var innerRelease token.Pos
	hasInnerAdd := false

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || g.isInsideGoroutine(call.Pos()) {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		wgName := common.GetVarName(sel.X)
		switch {
		case wgName == innerWG && sel.Sel.Name == "Add" && call.Pos() < goPos:
			if delta, ok := g.addDelta(call, innerWG); ok && delta > 0 {
				hasInnerAdd = true
			}
		case wgName == outerWG && sel.Sel.Name == "Wait" && call.Pos() > goPos && outerWait == token.NoPos:
			outerWait = call.Pos()
		case wgName == innerWG && sel.Sel.Name == "Done" && outerWait != token.NoPos && call.Pos() > outerWait && innerRelease == token.NoPos:
			innerRelease = call.Pos()
		case wgName == innerWG && sel.Sel.Name == "Add" && outerWait != token.NoPos && call.Pos() > outerWait && innerRelease == token.NoPos:
			if delta, ok := g.addDelta(call, innerWG); ok && delta < 0 {
				innerRelease = call.Pos()
			}
		}
		return true
	})

	return hasInnerAdd && outerWait != token.NoPos && innerRelease != token.NoPos
}

func (g *goroutineInspector) workerReleasesWaitGroupBefore(body *ast.BlockStmt, wgName string, before token.Pos) bool {
	if body == nil {
		return false
	}
	for _, stmt := range body.List {
		if stmt == nil || stmt.Pos() >= before {
			break
		}
		if g.shouldSkipStatement(stmt) {
			continue
		}
		if g.topLevelStatementReleasesWaitGroup(stmt, wgName) {
			return true
		}
	}
	return false
}

func (g *goroutineInspector) topLevelStatementReleasesWaitGroup(stmt ast.Stmt, wgName string) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		return ok && g.callReleasesWaitGroup(call, wgName)
	case *ast.LabeledStmt:
		return g.topLevelStatementReleasesWaitGroup(s.Stmt, wgName)
	default:
		return false
	}
}

func (g *goroutineInspector) callReleasesWaitGroup(call *ast.CallExpr, wgName string) bool {
	if call == nil || g.shouldSkipCall(call) {
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
	delta, ok := g.addDelta(call, wgName)
	return ok && delta < 0
}
