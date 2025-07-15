package waitgroup

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
)

// countDeferDoneInGoroutines counts defer Done calls specifically in goroutines
func (wga *Analyzer) countDeferDoneInGoroutines(wgName string) int {
	count := 0
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if goStmt, ok := n.(*ast.GoStmt); ok {
			if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
				deferCount := wga.countReachableDeferDoneInBlock(fnLit.Body, wgName)
				count += deferCount
			}
		}
		return true
	})
	return count
}

// isDeferDoneInGoroutine checks if hasDeferDone flag was set due to defer in goroutines
func (wga *Analyzer) isDeferDoneInGoroutine(wgName string) bool {
	foundInGoroutine := false
	foundOutsideGoroutine := false

	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if deferStmt, ok := n.(*ast.DeferStmt); ok {
			if wga.commentFilter.ShouldSkipCall(deferStmt.Call) {
				return true
			}

			if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := call.X.(*ast.Ident); ok && call.Sel.Name == "Done" {
					if ident.Name == wgName {
						if wga.isNodeInGoroutine(deferStmt) {
							foundInGoroutine = true
						} else {
							foundOutsideGoroutine = true
						}
					}
				}
			}
		}
		return true
	})

	return foundInGoroutine && !foundOutsideGoroutine
}

// isNodeInGoroutine checks if a node is inside a goroutine
func (wga *Analyzer) isNodeInGoroutine(targetNode ast.Node) bool {
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
func (wga *Analyzer) isInGoroutine(pos token.Pos) bool {
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

// isInBlockedGoroutine checks if a Done call is in a goroutine that will be blocked
func (wga *Analyzer) isInBlockedGoroutine(pos token.Pos, wgName string) bool {
	blocked := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if goStmt, ok := n.(*ast.GoStmt); ok {
			if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
				ast.Inspect(fnLit.Body, func(inner ast.Node) bool {
					if call, ok := inner.(*ast.CallExpr); ok {
						if call.Pos() == pos {
							_, isBlocked := wga.goroutineCallsDoneOrBlocks(goStmt, wgName)
							blocked = isBlocked
							return false
						}
					}
					return true
				})
			}
		}
		return !blocked
	})
	return blocked
}

// goroutineRelatedToWaitGroup checks if a goroutine is related to a WaitGroup
func (wga *Analyzer) goroutineRelatedToWaitGroup(goStmt *ast.GoStmt, wgName string) bool {
	if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
		found := false
		ast.Inspect(fnLit.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if common.GetVarName(sel.X) == wgName {
						found = true
						return false
					}
				}
			}
			return true
		})
		return found
	}
	return false
}

// goroutineCallsDoneOrBlocks analyzes if a goroutine calls Done or blocks indefinitely
func (wga *Analyzer) goroutineCallsDoneOrBlocks(goStmt *ast.GoStmt, wgName string) (callsDone bool, blocked bool) {
	fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
	if !ok {
		return false, false
	}

	ast.Inspect(fnLit.Body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.ExprStmt:
			if call, ok := stmt.X.(*ast.CallExpr); ok {
				if wga.commentFilter.ShouldSkipCall(call) {
					return true
				}

				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Done" && common.GetVarName(sel.X) == wgName {
					callsDone = true
					return false
				}
			}
			if unary, ok := stmt.X.(*ast.UnaryExpr); ok && unary.Op == token.ARROW {
				if chanIdent, ok := unary.X.(*ast.Ident); ok {
					if !wga.channelHasSender(chanIdent.Name) {
						blocked = true
						return false
					}
				}
			}
		case *ast.SelectStmt:
			blocked = true
			return false
		}
		return true
	})

	return callsDone, blocked
}

// channelHasSender checks if a channel has any sender in the function
func (wga *Analyzer) channelHasSender(chanName string) bool {
	hasSender := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if send, ok := n.(*ast.SendStmt); ok {
			if ident, ok := send.Chan.(*ast.Ident); ok && ident.Name == chanName {
				hasSender = true
				return false
			}
		}
		return true
	})
	return hasSender
}
