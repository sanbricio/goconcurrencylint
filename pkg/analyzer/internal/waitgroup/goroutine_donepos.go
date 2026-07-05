package waitgroup

import (
	"go/ast"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

func (g *goroutineInspector) checkWaitAndDoneInSameGoroutine(fn *ast.FuncDecl) {
	if g == nil || fn == nil || fn.Body == nil {
		return
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}

		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}

		for wgName := range g.waitGroupNames {
			positions := g.collectWaitDonePositions(fnLit.Body, wgName)
			for _, wait := range positions.waits {
				if waitDependsOnDoneInSameGoroutine(wait, positions) {
					g.reporter.AddError(wait.pos, category.WaitDeadlock, "waitgroup '"+wgName+"' Wait will deadlock: same goroutine has pending Done")
				}
			}
		}

		return true
	})
}

func waitDependsOnDoneInSameGoroutine(wait wgCallSite, positions waitDonePositions) bool {
	for _, done := range positions.dones {
		if done.pos > wait.pos && sameWaitGroupObject(wait.obj, done.obj) {
			return true
		}
	}
	for _, deferDone := range positions.deferDones {
		if sameWaitGroupObject(wait.obj, deferDone.obj) {
			return true
		}
	}
	return false
}

// sameWaitGroupObject reports whether two receiver objects denote the same
// WaitGroup. A nil object (an unresolved receiver, e.g. a field access) falls
// back to "same" so we keep the prior name-based behaviour rather than dropping
// a real deadlock; two resolved objects must be identical to match, which is
// what tells a shadowing inner `var wg` apart from an outer one.
func sameWaitGroupObject(a, b types.Object) bool {
	if a == nil || b == nil {
		return true
	}
	return a == b
}

func (g *goroutineInspector) collectWaitDonePositions(block *ast.BlockStmt, wgName string) waitDonePositions {
	positions := waitDonePositions{}

	var visitStmt func(ast.Stmt)
	var visitStmts func([]ast.Stmt)
	var visitExpr func(ast.Expr)

	visitStmts = func(stmts []ast.Stmt) {
		for _, stmt := range stmts {
			visitStmt(stmt)
		}
	}

	visitExpr = func(expr ast.Expr) {
		switch e := expr.(type) {
		case *ast.CallExpr:
			if g.shouldSkipCall(e) {
				return
			}
			g.recordWaitDoneCall(e, wgName, &positions)
			if fnLit, ok := e.Fun.(*ast.FuncLit); ok && fnLit.Body != nil {
				visitStmts(fnLit.Body.List)
			}
		case *ast.UnaryExpr:
			visitExpr(e.X)
		case *ast.BinaryExpr:
			visitExpr(e.X)
			visitExpr(e.Y)
		case *ast.ParenExpr:
			visitExpr(e.X)
		}
	}

	visitStmt = func(stmt ast.Stmt) {
		if stmt == nil || g.shouldSkipStatement(stmt) {
			return
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			visitExpr(s.X)
		case *ast.DeferStmt:
			if g.shouldSkipCall(s.Call) {
				return
			}
			if g.deferInvokesDone != nil && g.deferInvokesDone(s, wgName) {
				positions.deferDones = append(positions.deferDones, wgCallSite{pos: s.Call.Pos(), obj: g.deferDoneObject(s, wgName)})
			}
		case *ast.AssignStmt:
			for _, expr := range s.Rhs {
				visitExpr(expr)
			}
		case *ast.DeclStmt:
			if gen, ok := s.Decl.(*ast.GenDecl); ok {
				for _, spec := range gen.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, expr := range vs.Values {
							visitExpr(expr)
						}
					}
				}
			}
		case *ast.ReturnStmt:
			for _, expr := range s.Results {
				visitExpr(expr)
			}
		case *ast.IfStmt:
			visitStmt(s.Init)
			visitExpr(s.Cond)
			visitStmts(s.Body.List)
			visitWaitDoneElse(s.Else, visitStmt, visitStmts)
		case *ast.ForStmt:
			visitStmt(s.Init)
			visitExpr(s.Cond)
			visitStmt(s.Post)
			visitStmts(s.Body.List)
		case *ast.RangeStmt:
			visitExpr(s.X)
			visitStmts(s.Body.List)
		case *ast.BlockStmt:
			visitStmts(s.List)
		case *ast.LabeledStmt:
			visitStmt(s.Stmt)
		case *ast.SwitchStmt:
			visitStmt(s.Init)
			visitExpr(s.Tag)
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					for _, expr := range cc.List {
						visitExpr(expr)
					}
					visitStmts(cc.Body)
				}
			}
		case *ast.TypeSwitchStmt:
			visitStmt(s.Init)
			visitStmt(s.Assign)
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					visitStmts(cc.Body)
				}
			}
		case *ast.SelectStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CommClause); ok {
					visitStmt(cc.Comm)
					visitStmts(cc.Body)
				}
			}
		}
	}

	if block != nil {
		visitStmts(block.List)
	}
	return positions
}

func visitWaitDoneElse(stmt ast.Stmt, visitStmt func(ast.Stmt), visitStmts func([]ast.Stmt)) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		visitStmts(s.List)
	case *ast.IfStmt:
		visitStmt(s)
	}
}

func visitMainFlowElse(stmt ast.Stmt, visitStmt func(ast.Stmt), visitStmts func([]ast.Stmt)) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		visitStmts(s.List)
	case *ast.IfStmt:
		visitStmt(s)
	}
}

func (g *goroutineInspector) recordWaitDoneCall(call *ast.CallExpr, wgName string, positions *waitDonePositions) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || common.GetVarName(sel.X) != wgName {
		return
	}

	switch sel.Sel.Name {
	case "Wait":
		positions.waits = append(positions.waits, wgCallSite{pos: call.Pos(), obj: g.wgObject(sel.X)})
	case "Done":
		positions.dones = append(positions.dones, wgCallSite{pos: call.Pos(), obj: g.wgObject(sel.X)})
	}
}

// wgObject resolves the WaitGroup variable a receiver expression refers to.
// Only plain identifiers are resolved; field accesses and other shapes return
// nil so the caller falls back to name-based matching.
func (g *goroutineInspector) wgObject(expr ast.Expr) types.Object {
	if g.typesInfo == nil {
		return nil
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return g.typesInfo.ObjectOf(ident)
	}
	return nil
}

// deferDoneObject resolves the WaitGroup object of a `defer wg.Done()`. Wrapped
// forms (a deferred closure) resolve to nil and fall back to name matching.
func (g *goroutineInspector) deferDoneObject(deferStmt *ast.DeferStmt, wgName string) types.Object {
	if sel, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok && common.GetVarName(sel.X) == wgName {
		return g.wgObject(sel.X)
	}
	return nil
}
