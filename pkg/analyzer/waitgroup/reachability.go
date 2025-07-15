package waitgroup

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
)

// countReachableDeferDoneInBlock counts defer Done calls that are actually reachable
func (wga *Analyzer) countReachableDeferDoneInBlock(block *ast.BlockStmt, wgName string) int {
	count := 0

mainLoop:
	for _, stmt := range block.List {
		if deferStmt, ok := stmt.(*ast.DeferStmt); ok {
			if wga.commentFilter.ShouldSkipCall(deferStmt.Call) {
				continue
			}

			if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := call.X.(*ast.Ident); ok && call.Sel.Name == "Done" {
					if ident.Name == wgName {
						count++
					}
				}
			}
			continue
		}

		if wga.isTerminatingStatement(stmt) {
			break mainLoop
		}

		switch s := stmt.(type) {
		case *ast.IfStmt:
			if wga.isAlwaysTrueCondition(s.Cond) {
				thenCount := wga.countReachableDeferDoneInBlock(s.Body, wgName)
				count += thenCount

				if wga.blockTerminates(s.Body) {
					break mainLoop
				}
			} else if s.Else != nil {
				thenCount := wga.countReachableDeferDoneInBlock(s.Body, wgName)

				var elseCount int
				if elseBlock, ok := s.Else.(*ast.BlockStmt); ok {
					elseCount = wga.countReachableDeferDoneInBlock(elseBlock, wgName)
				} else if elseIf, ok := s.Else.(*ast.IfStmt); ok {
					elseBlock := &ast.BlockStmt{List: []ast.Stmt{elseIf}}
					elseCount = wga.countReachableDeferDoneInBlock(elseBlock, wgName)
				}

				if thenCount < elseCount {
					count += thenCount
				} else {
					count += elseCount
				}

				if wga.allBranchesTerminate(s) {
					break mainLoop
				}
			}

		case *ast.BlockStmt:
			count += wga.countReachableDeferDoneInBlock(s, wgName)
		case *ast.ForStmt:
			if s.Body != nil {
				count += wga.countReachableDeferDoneInBlock(s.Body, wgName)
			}
		case *ast.RangeStmt:
			if s.Body != nil {
				count += wga.countReachableDeferDoneInBlock(s.Body, wgName)
			}
		case *ast.SwitchStmt:
			minCount := -1
			for _, caseStmt := range s.Body.List {
				if cc, ok := caseStmt.(*ast.CaseClause); ok {
					caseBlock := &ast.BlockStmt{List: cc.Body}
					caseCount := wga.countReachableDeferDoneInBlock(caseBlock, wgName)
					if minCount == -1 || caseCount < minCount {
						minCount = caseCount
					}
				}
			}
			if minCount > 0 {
				count += minCount
			}
		case *ast.TypeSwitchStmt:
			minCount := -1
			for _, caseStmt := range s.Body.List {
				if cc, ok := caseStmt.(*ast.CaseClause); ok {
					caseBlock := &ast.BlockStmt{List: cc.Body}
					caseCount := wga.countReachableDeferDoneInBlock(caseBlock, wgName)
					if minCount == -1 || caseCount < minCount {
						minCount = caseCount
					}
				}
			}
			if minCount > 0 {
				count += minCount
			}
		case *ast.SelectStmt:
			minCount := -1
			for _, commClause := range s.Body.List {
				if cc, ok := commClause.(*ast.CommClause); ok {
					commBlock := &ast.BlockStmt{List: cc.Body}
					commCount := wga.countReachableDeferDoneInBlock(commBlock, wgName)
					if minCount == -1 || commCount < minCount {
						minCount = commCount
					}
				}
			}
			if minCount > 0 {
				count += minCount
			}
		}
	}

	return count
}

// isAlwaysTrueCondition checks if a condition is always true (like "true")
func (wga *Analyzer) isAlwaysTrueCondition(cond ast.Expr) bool {
	if ident, ok := cond.(*ast.Ident); ok {
		return ident.Name == "true"
	}
	return false
}

// allBranchesTerminate checks if all branches of an if statement terminate execution
func (wga *Analyzer) allBranchesTerminate(ifStmt *ast.IfStmt) bool {
	thenTerminates := wga.blockTerminates(ifStmt.Body)

	if ifStmt.Else == nil {
		return false
	}

	var elseTerminates bool
	switch e := ifStmt.Else.(type) {
	case *ast.BlockStmt:
		elseTerminates = wga.blockTerminates(e)
	case *ast.IfStmt:
		elseTerminates = wga.allBranchesTerminate(e)
	default:
		elseTerminates = false
	}

	return thenTerminates && elseTerminates
}

// blockTerminates checks if a block definitely terminates execution
func (wga *Analyzer) blockTerminates(block *ast.BlockStmt) bool {
	for _, stmt := range block.List {
		if wga.isTerminatingStatement(stmt) {
			return true
		}

		switch s := stmt.(type) {
		case *ast.IfStmt:
			if wga.allBranchesTerminate(s) {
				return true
			}
		case *ast.BlockStmt:
			if wga.blockTerminates(s) {
				return true
			}
		}
	}
	return false
}

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
	var lastAddPos token.Pos

	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if n == goStmt {
			return false
		}

		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "Add" && common.GetVarName(sel.X) == wgName {
					lastAddPos = call.Pos()
				}
			}
		}

		return true
	})

	return lastAddPos
}
