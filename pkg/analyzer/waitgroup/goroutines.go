package waitgroup

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
)

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

// doneCallInfo contains information about Done calls in a block
type doneCallInfo struct {
	hasAnyDone        bool
	hasGuaranteedDone bool
}

// analyzeDoneCalls analyzes Done calls in a block and returns info about them
func (wga *Analyzer) analyzeDoneCalls(block *ast.BlockStmt, wgName string) doneCallInfo {
	info := doneCallInfo{}

	for _, stmt := range block.List {
		if wga.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		// If we already found guaranteed Done, we can return early
		if info.hasGuaranteedDone {
			return info
		}

		switch s := stmt.(type) {
		case *ast.DeferStmt:
			// Defer calls are always guaranteed (executed regardless of panic)
			if wga.isSimpleDeferDone(s, wgName) || wga.isDeferPanicRecoveryPattern(s, wgName) {
				info.hasAnyDone = true
				info.hasGuaranteedDone = true
				return info
			}

		case *ast.ExprStmt:
			// Direct Done() call - this is guaranteed at this execution level
			if call, ok := s.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok &&
					sel.Sel.Name == "Done" && common.GetVarName(sel.X) == wgName {
					info.hasAnyDone = true
					info.hasGuaranteedDone = true
					return info
				}
			}

		case *ast.IfStmt:
			// For if statements, Done is guaranteed only if both branches have it
			thenInfo := wga.analyzeDoneCalls(s.Body, wgName)
			info.hasAnyDone = info.hasAnyDone || thenInfo.hasAnyDone

			if s.Else != nil {
				var elseInfo doneCallInfo
				if elseBlock, ok := s.Else.(*ast.BlockStmt); ok {
					elseInfo = wga.analyzeDoneCalls(elseBlock, wgName)
				} else if elseIf, ok := s.Else.(*ast.IfStmt); ok {
					// Handle else if as a nested if
					elseBlock := &ast.BlockStmt{List: []ast.Stmt{elseIf}}
					elseInfo = wga.analyzeDoneCalls(elseBlock, wgName)
				}

				info.hasAnyDone = info.hasAnyDone || elseInfo.hasAnyDone

				// Done is guaranteed only if both then and else have guaranteed Done
				if thenInfo.hasGuaranteedDone && elseInfo.hasGuaranteedDone {
					info.hasGuaranteedDone = true
					return info
				}
			}
			// If there's no else branch, Done is not guaranteed

		case *ast.SwitchStmt:
			switchInfo := wga.analyzeSwitchStatement(s, wgName)
			info.hasAnyDone = info.hasAnyDone || switchInfo.hasAnyDone
			if switchInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}

		case *ast.TypeSwitchStmt:
			typeSwitchInfo := wga.analyzeTypeSwitchStatement(s, wgName)
			info.hasAnyDone = info.hasAnyDone || typeSwitchInfo.hasAnyDone
			if typeSwitchInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}

		case *ast.SelectStmt:
			selectInfo := wga.analyzeSelectStatement(s, wgName)
			info.hasAnyDone = info.hasAnyDone || selectInfo.hasAnyDone
			if selectInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}

		case *ast.ForStmt, *ast.RangeStmt:
			// For loops, we don't guarantee Done execution (loop might not run)
			// But we still track if there's any Done
			var loopInfo doneCallInfo
			if forStmt, ok := s.(*ast.ForStmt); ok && forStmt.Body != nil {
				loopInfo = wga.analyzeDoneCalls(forStmt.Body, wgName)
			} else if rangeStmt, ok := s.(*ast.RangeStmt); ok && rangeStmt.Body != nil {
				loopInfo = wga.analyzeDoneCalls(rangeStmt.Body, wgName)
			}
			info.hasAnyDone = info.hasAnyDone || loopInfo.hasAnyDone
			// Note: We don't set hasGuaranteedDone for loops

		case *ast.BlockStmt:
			// Nested block - analyze recursively
			blockInfo := wga.analyzeDoneCalls(s, wgName)
			info.hasAnyDone = info.hasAnyDone || blockInfo.hasAnyDone
			if blockInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}
		}
	}

	return info
}

// analyzeSwitchStatement analyzes Done calls in a switch statement
func (wga *Analyzer) analyzeSwitchStatement(switchStmt *ast.SwitchStmt, wgName string) doneCallInfo {
	info := doneCallInfo{}
	hasDefault := false
	defaultInfo := doneCallInfo{}

	for _, stmt := range switchStmt.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}

		// Check if this is the default case (no case expressions)
		isDefaultCase := len(cc.List) == 0

		// Create a block from the case body
		caseBlock := &ast.BlockStmt{List: cc.Body}
		caseInfo := wga.analyzeDoneCalls(caseBlock, wgName)

		// Track if any case has Done
		info.hasAnyDone = info.hasAnyDone || caseInfo.hasAnyDone

		if isDefaultCase {
			hasDefault = true
			defaultInfo = caseInfo
		}
	}

	// Done is guaranteed only if there's a default case with guaranteed Done
	if hasDefault && defaultInfo.hasGuaranteedDone {
		info.hasGuaranteedDone = true
	}

	return info
}

// analyzeTypeSwitchStatement analyzes Done calls in a type switch statement
func (wga *Analyzer) analyzeTypeSwitchStatement(typeSwitchStmt *ast.TypeSwitchStmt, wgName string) doneCallInfo {
	info := doneCallInfo{}
	hasDefault := false
	defaultInfo := doneCallInfo{}

	for _, stmt := range typeSwitchStmt.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}

		// Check if this is the default case
		isDefaultCase := len(cc.List) == 0

		caseBlock := &ast.BlockStmt{List: cc.Body}
		caseInfo := wga.analyzeDoneCalls(caseBlock, wgName)

		info.hasAnyDone = info.hasAnyDone || caseInfo.hasAnyDone

		if isDefaultCase {
			hasDefault = true
			defaultInfo = caseInfo
		}
	}

	if hasDefault && defaultInfo.hasGuaranteedDone {
		info.hasGuaranteedDone = true
	}

	return info
}

// analyzeSelectStatement analyzes Done calls in a select statement
func (wga *Analyzer) analyzeSelectStatement(selectStmt *ast.SelectStmt, wgName string) doneCallInfo {
	info := doneCallInfo{}
	hasDefault := false
	defaultInfo := doneCallInfo{}

	for _, stmt := range selectStmt.Body.List {
		cc, ok := stmt.(*ast.CommClause)
		if !ok {
			continue
		}

		// Check if this is the default case
		isDefaultCase := cc.Comm == nil

		caseBlock := &ast.BlockStmt{List: cc.Body}
		caseInfo := wga.analyzeDoneCalls(caseBlock, wgName)

		info.hasAnyDone = info.hasAnyDone || caseInfo.hasAnyDone

		if isDefaultCase {
			hasDefault = true
			defaultInfo = caseInfo
		}
	}

	if hasDefault && defaultInfo.hasGuaranteedDone {
		info.hasGuaranteedDone = true
	}

	return info
}

// isSimpleDeferDone checks if a defer statement is a simple defer wg.Done()
func (wga *Analyzer) isSimpleDeferDone(deferStmt *ast.DeferStmt, wgName string) bool {
	if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
		return call.Sel.Name == "Done" && common.GetVarName(call.X) == wgName
	}
	return false
}

// isDeferPanicRecoveryPattern detects panic recovery pattern
func (wga *Analyzer) isDeferPanicRecoveryPattern(deferStmt *ast.DeferStmt, wgName string) bool {
	// Check if the defer has a function literal
	fnLit, ok := deferStmt.Call.Fun.(*ast.FuncLit)
	if !ok {
		return false
	}

	hasPanicRecovery := false
	hasDoneInRecovery := false

	ast.Inspect(fnLit.Body, func(n ast.Node) bool {
		// Look for recover() call
		if call, ok := n.(*ast.CallExpr); ok {
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "recover" {
				hasPanicRecovery = true
			}
		}

		// Look for if statement that checks recover result
		if ifStmt, ok := n.(*ast.IfStmt); ok {
			// Check if it's a pattern like: if r := recover(); r != nil
			if hasPanicRecovery || wga.isRecoverCheck(ifStmt) {
				// Check if Done is called in the if body
				ast.Inspect(ifStmt.Body, func(inner ast.Node) bool {
					if call, ok := inner.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
							if sel.Sel.Name == "Done" && common.GetVarName(sel.X) == wgName {
								hasDoneInRecovery = true
								return false
							}
						}
					}
					return true
				})
			}
		}
		return true
	})

	return hasPanicRecovery && hasDoneInRecovery
}

// isRecoverCheck checks if an if statement is checking recover() result
func (wga *Analyzer) isRecoverCheck(ifStmt *ast.IfStmt) bool {
	// Check for pattern: if r := recover(); r != nil
	if ifStmt.Init != nil {
		if assign, ok := ifStmt.Init.(*ast.AssignStmt); ok {
			if len(assign.Rhs) == 1 {
				if call, ok := assign.Rhs[0].(*ast.CallExpr); ok {
					if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "recover" {
						return true
					}
				}
			}
		}
	}
	return false
}
