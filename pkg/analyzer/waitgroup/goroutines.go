package waitgroup

import (
	"fmt"
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

	var hasUnconditionalDone bool
	var hasConditionalDone bool

	ast.Inspect(fnLit.Body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.DeferStmt:
			// Check for defer Done
			if call, ok := stmt.Call.Fun.(*ast.SelectorExpr); ok {
				if call.Sel.Name == "Done" && common.GetVarName(call.X) == wgName {
					// defer is considered unconditional (unless panic)
					hasUnconditionalDone = true
					callsDone = true
					return false
				}
			}
		case *ast.ExprStmt:
			if call, ok := stmt.X.(*ast.CallExpr); ok {
				if wga.commentFilter.ShouldSkipCall(call) {
					return true
				}

				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Done" && common.GetVarName(sel.X) == wgName {
					// Check if this Done is conditional
					if wga.isInConditionalBlock(stmt, fnLit.Body) {
						hasConditionalDone = true
					} else {
						hasUnconditionalDone = true
						callsDone = true
						return false
					}
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

	// If there are only conditional Done calls and no unconditional ones,
	// treat it as if Done is not called
	if hasConditionalDone && !hasUnconditionalDone {
		callsDone = false
	}

	return callsDone, blocked
}

// doneCallInfo contains information about Done calls in a block
type doneCallInfo struct {
	hasAnyDone        bool
	hasGuaranteedDone bool
}

// analyzeDoneCalls analyzes Done calls in a block and returns info about them
func (wga *Analyzer) analyzeDoneCalls(block *ast.BlockStmt, wgName string) doneCallInfo {
	info := doneCallInfo{}

	// DEBUG
	fmt.Printf("=== Analyzing block for WaitGroup '%s' ===\n", wgName)

	for i, stmt := range block.List {
		if wga.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		// DEBUG
		fmt.Printf("Statement %d: %T\n", i, stmt)

		if info.hasGuaranteedDone {
			// DEBUG
			fmt.Printf("Already found guaranteed Done, returning\n")
			return info
		}

		switch s := stmt.(type) {
		case *ast.DeferStmt:
			if wga.isSimpleDeferDone(s, wgName) || wga.isDeferPanicRecoveryPattern(s, wgName) {
				info.hasAnyDone = true
				info.hasGuaranteedDone = true
				// DEBUG
				fmt.Printf("Found defer Done - guaranteed\n")
				return info
			}

		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Done" && common.GetVarName(sel.X) == wgName {
					info.hasAnyDone = true
					info.hasGuaranteedDone = true
					// DEBUG
					// fmt.Printf("Found direct Done call - guaranteed\n")
					return info
				}
			}

		case *ast.SwitchStmt:
			// DEBUG
			fmt.Printf("Analyzing switch statement\n")
			switchInfo := wga.analyzeSwitchStatement(s, wgName)
			// DEBUG
			fmt.Printf("Switch result: hasAnyDone=%v, hasGuaranteedDone=%v\n",
				switchInfo.hasAnyDone, switchInfo.hasGuaranteedDone)
			info.hasAnyDone = info.hasAnyDone || switchInfo.hasAnyDone
			if switchInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}
		}
	}

	// DEBUG
	fmt.Printf("=== Final result: hasAnyDone=%v, hasGuaranteedDone=%v ===\n",
		info.hasAnyDone, info.hasGuaranteedDone)

	return info
}

// analyzeSwitchStatement analyzes Done calls in a switch statement
func (wga *Analyzer) analyzeSwitchStatement(switchStmt *ast.SwitchStmt, wgName string) doneCallInfo {
	info := doneCallInfo{}
	hasDefault := false
	defaultInfo := doneCallInfo{}

	fmt.Printf("  === SWITCH ANALYSIS START ===\n")
	fmt.Printf("  Number of cases: %d\n", len(switchStmt.Body.List))

	for i, stmt := range switchStmt.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			fmt.Printf("  Case %d: Not a CaseClause\n", i)
			continue
		}

		// Check if this is the default case
		isDefaultCase := len(cc.List) == 0

		fmt.Printf("  Case %d: default=%v, expressions=%d, statements=%d\n",
			i, isDefaultCase, len(cc.List), len(cc.Body))

		// Debug what's in the case body
		for j, bodyStmt := range cc.Body {
			fmt.Printf("    Statement %d in case: %T", j, bodyStmt)
			if exprStmt, ok := bodyStmt.(*ast.ExprStmt); ok {
				if call, ok := exprStmt.X.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						fmt.Printf(" -> %s.%s()", common.GetVarName(sel.X), sel.Sel.Name)
					}
				}
			}
			fmt.Printf("\n")
		}

		// Create a block from the case body
		caseBlock := &ast.BlockStmt{List: cc.Body}
		caseInfo := wga.analyzeDoneCalls(caseBlock, wgName)

		fmt.Printf("  Case %d result: hasAnyDone=%v, hasGuaranteedDone=%v\n",
			i, caseInfo.hasAnyDone, caseInfo.hasGuaranteedDone)

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

	fmt.Printf("  === SWITCH ANALYSIS END: hasDefault=%v, defaultHasGuaranteedDone=%v, final.hasGuaranteedDone=%v ===\n",
		hasDefault, defaultInfo.hasGuaranteedDone, info.hasGuaranteedDone)

	return info
}

// isSimpleDeferDone checks if a defer statement is a simple defer wg.Done()
func (wga *Analyzer) isSimpleDeferDone(deferStmt *ast.DeferStmt, wgName string) bool {
	if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
		return call.Sel.Name == "Done" && common.GetVarName(call.X) == wgName
	}
	return false
}

// isDeferPanicRecoveryPattern detects panic recovery pattern (keep existing implementation)
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

// isRecoverCheck checks if an if statement is checking recover() result (keep existing)
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

// isInConditionalBlock checks if a node is inside any conditional block
func (wga *Analyzer) isInConditionalBlock(target ast.Node, scope ast.Node) bool {
	var inConditional bool
	var foundTarget bool

	ast.Inspect(scope, func(n ast.Node) bool {
		if n == target {
			foundTarget = true
			return false
		}

		switch stmt := n.(type) {
		case *ast.IfStmt:
			// Check if target is inside this if statement
			if wga.nodeContains(stmt.Body, target) ||
				(stmt.Else != nil && wga.nodeContains(stmt.Else, target)) {
				inConditional = true
				foundTarget = true
				return false
			}
		case *ast.SwitchStmt:
			// Check if target is inside any case
			for _, caseStmt := range stmt.Body.List {
				if cc, ok := caseStmt.(*ast.CaseClause); ok {
					for _, bodyStmt := range cc.Body {
						if wga.nodeContains(bodyStmt, target) {
							inConditional = true
							foundTarget = true
							return false
						}
					}
				}
			}
		case *ast.TypeSwitchStmt:
			// Check if target is inside any case
			for _, caseStmt := range stmt.Body.List {
				if cc, ok := caseStmt.(*ast.CaseClause); ok {
					for _, bodyStmt := range cc.Body {
						if wga.nodeContains(bodyStmt, target) {
							inConditional = true
							foundTarget = true
							return false
						}
					}
				}
			}
		case *ast.SelectStmt:
			// Check if target is inside any case
			for _, commClause := range stmt.Body.List {
				if cc, ok := commClause.(*ast.CommClause); ok {
					for _, bodyStmt := range cc.Body {
						if wga.nodeContains(bodyStmt, target) {
							inConditional = true
							foundTarget = true
							return false
						}
					}
				}
			}
		}

		return !foundTarget
	})

	return inConditional
}

// nodeContains checks if a node contains another node
func (wga *Analyzer) nodeContains(container, target ast.Node) bool {
	found := false
	ast.Inspect(container, func(n ast.Node) bool {
		if n == target {
			found = true
			return false
		}
		return true
	})
	return found
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
