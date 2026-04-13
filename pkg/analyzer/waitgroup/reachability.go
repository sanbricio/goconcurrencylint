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

// blockAlwaysTerminates checks if a block always terminates execution (return, panic, etc.)
func (wga *Analyzer) blockAlwaysTerminates(block *ast.BlockStmt) bool {
	for _, stmt := range block.List {
		if wga.isTerminatingStatement(stmt) {
			return true
		}
		if ifStmt, ok := stmt.(*ast.IfStmt); ok {
			if ifStmt.Else != nil {
				var elseTerminates bool
				if elseBlock, ok := ifStmt.Else.(*ast.BlockStmt); ok {
					elseTerminates = wga.blockAlwaysTerminates(elseBlock)
				} else if elseIf, ok := ifStmt.Else.(*ast.IfStmt); ok {
					elseBlock := &ast.BlockStmt{List: []ast.Stmt{elseIf}}
					elseTerminates = wga.blockAlwaysTerminates(elseBlock)
				}
				if wga.blockAlwaysTerminates(ifStmt.Body) && elseTerminates {
					return true
				}
			}
		}
	}
	return false
}

// hasChannelSends checks if there are any send operations or close calls for the given channel in the function
func (wga *Analyzer) hasChannelSends(chanName string) bool {
	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		if sendStmt, ok := n.(*ast.SendStmt); ok {
			if common.GetVarName(sendStmt.Chan) == chanName {
				found = true
				return false
			}
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "close" {
				if len(call.Args) == 1 && common.GetVarName(call.Args[0]) == chanName {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// isLocallyCreatedChannel checks if a channel was created with make() in the current function
func (wga *Analyzer) isLocallyCreatedChannel(chanName string) bool {
	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		if assign, ok := n.(*ast.AssignStmt); ok {
			for i, lhs := range assign.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && ident.Name == chanName {
					if i < len(assign.Rhs) {
						if call, ok := assign.Rhs[i].(*ast.CallExpr); ok {
							if fnIdent, ok := call.Fun.(*ast.Ident); ok && fnIdent.Name == "make" {
								found = true
								return false
							}
						}
					}
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
