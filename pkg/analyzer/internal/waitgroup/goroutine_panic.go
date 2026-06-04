package waitgroup

import (
	"go/ast"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

func (g *goroutineInspector) checkWaitGroupGoPanic(fn *ast.FuncDecl) {
	if g == nil || fn == nil || fn.Body == nil {
		return
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || g.shouldSkipCall(call) {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Go" {
			return true
		}
		wgName := common.GetVarName(sel.X)
		if !g.waitGroupNames[wgName] || len(call.Args) == 0 {
			return true
		}

		fnLit, ok := call.Args[0].(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}
		if g.functionLiteralMayPanic(fnLit) {
			g.reporter.AddError(call.Pos(), category.GoPanic, "waitgroup '"+wgName+"' Go function may panic")
		}
		return true
	})
}

func (g *goroutineInspector) functionLiteralMayPanic(fnLit *ast.FuncLit) bool {
	if fnLit == nil || fnLit.Body == nil || g.functionLiteralRecovers(fnLit) {
		return false
	}

	found := false
	ast.Inspect(fnLit.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		if nested, ok := n.(*ast.FuncLit); ok && nested != fnLit {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if ok && g.callIsBuiltinPanic(ident) {
			found = true
			return false
		}
		return true
	})
	return found
}

func (g *goroutineInspector) functionLiteralRecovers(fnLit *ast.FuncLit) bool {
	if fnLit == nil || fnLit.Body == nil {
		return false
	}

	found := false
	ast.Inspect(fnLit.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		if nested, ok := n.(*ast.FuncLit); ok && nested != fnLit {
			return false
		}
		deferStmt, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		if g.deferCallRecovers(deferStmt.Call) {
			found = true
			return false
		}
		return true
	})
	return found
}

func (g *goroutineInspector) deferCallRecovers(call *ast.CallExpr) bool {
	if call == nil {
		return false
	}

	if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "recover" {
		return true
	}

	fnLit, ok := call.Fun.(*ast.FuncLit)
	if !ok || fnLit.Body == nil {
		return false
	}

	found := false
	ast.Inspect(fnLit.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		if nested, ok := n.(*ast.FuncLit); ok && nested != fnLit {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if ok && ident.Name == "recover" {
			found = true
			return false
		}
		return true
	})
	return found
}
