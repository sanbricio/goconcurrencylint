package waitgroup

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
)

// isTerminatingStatement checks if a statement terminates execution flow
func (wga *Analyzer) isTerminatingStatement(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		return s.Tok == token.BREAK || s.Tok == token.GOTO
	case *ast.ExprStmt:
		if call, ok := s.X.(*ast.CallExpr); ok {
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "panic" {
				return true
			}
		}
	}
	return false
}

// hasUnreachableDone checks if a function body has unreachable Done calls
func (wga *Analyzer) hasUnreachableDone(body *ast.BlockStmt, wgName string) bool {
	for i, stmt := range body.List {
		if wga.isTerminatingStatement(stmt) {
			for j := i + 1; j < len(body.List); j++ {
				if wga.containsDoneCall(body.List[j], wgName) {
					return true
				}
			}
		}

		switch s := stmt.(type) {
		case *ast.IfStmt:
			if wga.hasUnreachableDone(s.Body, wgName) {
				return true
			}
			if s.Else != nil {
				if elseBlock, ok := s.Else.(*ast.BlockStmt); ok {
					if wga.hasUnreachableDone(elseBlock, wgName) {
						return true
					}
				}
			}
		case *ast.ForStmt:
			if s.Body != nil && wga.hasUnreachableDone(s.Body, wgName) {
				return true
			}
		case *ast.BlockStmt:
			if wga.hasUnreachableDone(s, wgName) {
				return true
			}
		}
	}

	return false
}

// containsDoneCall checks if a statement contains a Done call for the given WaitGroup
func (wga *Analyzer) containsDoneCall(stmt ast.Stmt, wgName string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "Done" && common.GetVarName(sel.X) == wgName {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// findRelatedAddCall finds an Add call that might be related to this goroutine
func (wga *Analyzer) findRelatedAddCall(goStmt *ast.GoStmt, wgName string) token.Pos {
	// First, try to find an Add call that appears just before this goroutine
	var lastAddBeforeGo token.Pos
	var allAdds []token.Pos

	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "Add" && common.GetVarName(sel.X) == wgName {
					allAdds = append(allAdds, call.Pos())
					if call.Pos() < goStmt.Pos() {
						lastAddBeforeGo = call.Pos()
					}
				}
			}
		}
		return true
	})

	// If we found an Add before this goroutine, return it
	if lastAddBeforeGo != token.NoPos {
		return lastAddBeforeGo
	}

	// Otherwise, return the first Add call we found (if any)
	if len(allAdds) > 0 {
		return allAdds[0]
	}

	return token.NoPos
}
