package waitgroup

import (
	"go/ast"
	"go/token"

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
			for _, waitPos := range positions.waits {
				if waitDependsOnDoneInSameGoroutine(waitPos, positions) {
					g.reporter.AddError(waitPos, category.WaitDeadlock, "waitgroup '"+wgName+"' Wait will deadlock: same goroutine has pending Done")
				}
			}
		}

		return true
	})
}

func waitDependsOnDoneInSameGoroutine(waitPos token.Pos, positions waitDonePositions) bool {
	for _, donePos := range positions.dones {
		if donePos > waitPos {
			return true
		}
	}
	return len(positions.deferDones) > 0
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
			recordWaitDoneCall(e, wgName, &positions)
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
				positions.deferDones = append(positions.deferDones, s.Call.Pos())
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

func recordWaitDoneCall(call *ast.CallExpr, wgName string, positions *waitDonePositions) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || common.GetVarName(sel.X) != wgName {
		return
	}

	switch sel.Sel.Name {
	case "Wait":
		positions.waits = append(positions.waits, call.Pos())
	case "Done":
		positions.dones = append(positions.dones, call.Pos())
	}
}
