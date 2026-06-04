package waitgroup

import (
	"go/ast"
	"go/token"
)

// isNodeInGoroutine checks if a node is inside a goroutine
func (wga *Checker) isNodeInGoroutine(targetNode ast.Node) bool {
	inGoroutine := false

	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if goStmt, ok := n.(*ast.GoStmt); ok {
			if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
				ast.Inspect(fnLit.Body, func(inner ast.Node) bool {
					if inner == targetNode {
						inGoroutine = true
						return false
					}
					return true
				})
			}
		}
		return !inGoroutine
	})

	return inGoroutine
}

// isInGoroutine checks if a position is within a goroutine
func (wga *Checker) isInGoroutine(pos token.Pos) bool {
	isInGoroutine := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if goStmt, ok := n.(*ast.GoStmt); ok {
			if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
				ast.Inspect(fnLit.Body, func(inner ast.Node) bool {
					if call, ok := inner.(*ast.CallExpr); ok {
						if call.Pos() == pos {
							isInGoroutine = true
							return false
						}
					}
					return true
				})
			}
		}
		return !isInGoroutine
	})
	return isInGoroutine
}
