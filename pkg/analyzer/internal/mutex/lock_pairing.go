package mutex

import "go/ast"

// everyAcquisitionPairsWith reports whether a body acquires a lock at least
// once and every acquisition is immediately followed by the statement recording
// it (`held = true`, `locker = &mu`). A deferred release only fires for
// acquisitions the bookkeeping knows about, so one that skips it stays
// unreleased. Function literals count as part of the same body.
func everyAcquisitionPairsWith(body *ast.BlockStmt, acquires, records func(ast.Stmt) bool) bool {
	if body == nil {
		return false
	}

	foundAcquisition := false
	paired := true

	var visit func(stmts []ast.Stmt)
	var visitCall func(call *ast.CallExpr)

	visit = func(stmts []ast.Stmt) {
		for i, stmt := range stmts {
			if acquires(stmt) {
				foundAcquisition = true
				if i+1 >= len(stmts) || !records(stmts[i+1]) {
					paired = false
				}
				continue
			}

			switch s := stmt.(type) {
			case *ast.BlockStmt:
				visit(s.List)
			case *ast.AssignStmt:
				for _, rhs := range s.Rhs {
					if call, ok := rhs.(*ast.CallExpr); ok {
						visitCall(call)
					}
				}
			case *ast.ExprStmt:
				if call, ok := s.X.(*ast.CallExpr); ok {
					visitCall(call)
				}
			case *ast.ReturnStmt:
				for _, result := range s.Results {
					if call, ok := result.(*ast.CallExpr); ok {
						visitCall(call)
					}
				}
			case *ast.GoStmt:
				visitCall(s.Call)
			case *ast.DeferStmt:
				visitCall(s.Call)
			case *ast.IfStmt:
				if s.Init != nil {
					visit([]ast.Stmt{s.Init})
				}
				if s.Body != nil {
					visit(s.Body.List)
				}
				if s.Else != nil {
					visit([]ast.Stmt{s.Else})
				}
			case *ast.ForStmt:
				if s.Body != nil {
					visit(s.Body.List)
				}
			case *ast.RangeStmt:
				if s.Body != nil {
					visit(s.Body.List)
				}
			case *ast.SwitchStmt:
				if s.Body != nil {
					visit(s.Body.List)
				}
			case *ast.TypeSwitchStmt:
				if s.Body != nil {
					visit(s.Body.List)
				}
			case *ast.SelectStmt:
				if s.Body != nil {
					visit(s.Body.List)
				}
			case *ast.CaseClause:
				visit(s.Body)
			case *ast.CommClause:
				visit(s.Body)
			case *ast.LabeledStmt:
				visit([]ast.Stmt{s.Stmt})
			}
		}
	}

	// Reached once per literal, through the call carrying it: a deep search
	// would re-enter nested literals on every level.
	visitCall = func(call *ast.CallExpr) {
		if call == nil {
			return
		}
		if fnlit, ok := call.Fun.(*ast.FuncLit); ok && fnlit.Body != nil {
			visit(fnlit.Body.List)
		}
		for _, arg := range call.Args {
			if fnlit, ok := arg.(*ast.FuncLit); ok && fnlit.Body != nil {
				visit(fnlit.Body.List)
			}
		}
	}

	visit(body.List)
	return foundAcquisition && paired
}
