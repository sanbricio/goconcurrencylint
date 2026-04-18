package waitgroup

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
)

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

func (wga *Analyzer) goroutineDoneInfo(goStmt *ast.GoStmt, wgName string) (doneCallInfo, bool) {
	if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
		if !wga.goroutineRelatedToWaitGroup(goStmt, wgName) {
			return doneCallInfo{}, false
		}
		return wga.analyzeDoneCallsWithVisited(fnLit.Body, wgName, make(map[token.Pos]bool)), true
	}

	return wga.analyzeRelatedCall(goStmt.Call, wgName, make(map[token.Pos]bool))
}

// doneCallInfo contains information about Done calls in a block
type doneCallInfo struct {
	hasAnyDone        bool
	hasGuaranteedDone bool
}

func (wga *Analyzer) analyzeDoneCallsWithVisited(block *ast.BlockStmt, wgName string, visited map[token.Pos]bool) doneCallInfo {
	info := doneCallInfo{}
	// Tracks whether a prior branch could exit early, making subsequent statements conditional
	mightExitEarly := false

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
			if wga.isSimpleDeferDone(s, wgName) || wga.isCallbackDeferDone(s, wgName) || wga.isDeferPanicRecoveryPattern(s, wgName) || wga.isDeferFuncWithDone(s, wgName) {
				info.hasAnyDone = true
				if !mightExitEarly {
					info.hasGuaranteedDone = true
					return info
				}
			}

		case *ast.ExprStmt:
			// Check for channel receive on a local channel with no sends (potentially blocking forever)
			if unary, ok := s.X.(*ast.UnaryExpr); ok && unary.Op == token.ARROW {
				chanName := common.GetVarName(unary.X)
				if chanName != "" && chanName != "?" &&
					wga.isLocallyCreatedChannel(chanName) && !wga.hasChannelSends(chanName) {
					mightExitEarly = true
				}
			}

			// Direct Done() call
			if call, ok := s.X.(*ast.CallExpr); ok {
				if wga.callInvokesDone(call, wgName) {
					info.hasAnyDone = true
					if !mightExitEarly {
						info.hasGuaranteedDone = true
						return info
					}
				}

				helperInfo, related := wga.analyzeRelatedCall(call, wgName, visited)
				if related {
					info.hasAnyDone = info.hasAnyDone || helperInfo.hasAnyDone
					if helperInfo.hasGuaranteedDone && !mightExitEarly {
						info.hasGuaranteedDone = true
						return info
					}
				}
			}

		case *ast.IfStmt:
			// For if statements, Done is guaranteed only if both branches have it
			thenInfo := wga.analyzeDoneCallsWithVisited(s.Body, wgName, visited)
			info.hasAnyDone = info.hasAnyDone || thenInfo.hasAnyDone

			thenTerminates := wga.blockAlwaysTerminates(s.Body)

			if s.Else != nil {
				var elseInfo doneCallInfo
				var elseTerminates bool
				if elseBlock, ok := s.Else.(*ast.BlockStmt); ok {
					elseInfo = wga.analyzeDoneCallsWithVisited(elseBlock, wgName, visited)
					elseTerminates = wga.blockAlwaysTerminates(elseBlock)
				} else if elseIf, ok := s.Else.(*ast.IfStmt); ok {
					elseBlock := &ast.BlockStmt{List: []ast.Stmt{elseIf}}
					elseInfo = wga.analyzeDoneCallsWithVisited(elseBlock, wgName, visited)
					elseTerminates = wga.blockAlwaysTerminates(elseBlock)
				}

				info.hasAnyDone = info.hasAnyDone || elseInfo.hasAnyDone

				// Done is guaranteed only if both then and else have guaranteed Done
				if thenInfo.hasGuaranteedDone && elseInfo.hasGuaranteedDone {
					info.hasGuaranteedDone = true
					return info
				}

				// If both branches terminate, nothing after is reachable
				if thenTerminates && elseTerminates {
					return info
				}

				// If either branch terminates, subsequent code is conditional
				if thenTerminates || elseTerminates {
					mightExitEarly = true
				}
			} else {
				// No else: if the then body terminates, subsequent code is conditional
				if thenTerminates {
					mightExitEarly = true
				}
			}

		case *ast.SwitchStmt:
			switchInfo := wga.analyzeSwitchStatementWithVisited(s, wgName, visited)
			info.hasAnyDone = info.hasAnyDone || switchInfo.hasAnyDone
			if switchInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}

		case *ast.TypeSwitchStmt:
			typeSwitchInfo := wga.analyzeTypeSwitchStatementWithVisited(s, wgName, visited)
			info.hasAnyDone = info.hasAnyDone || typeSwitchInfo.hasAnyDone
			if typeSwitchInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}

		case *ast.SelectStmt:
			selectInfo := wga.analyzeSelectStatementWithVisited(s, wgName, visited)
			info.hasAnyDone = info.hasAnyDone || selectInfo.hasAnyDone
			if selectInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}

		case *ast.GoStmt:
			goInfo, related := wga.goroutineDoneInfo(s, wgName)
			if related {
				info.hasAnyDone = info.hasAnyDone || goInfo.hasAnyDone
				if goInfo.hasGuaranteedDone && !mightExitEarly {
					info.hasGuaranteedDone = true
					return info
				}
			}

		case *ast.ForStmt, *ast.RangeStmt:
			// For loops, we don't guarantee Done execution (loop might not run)
			// But we still track if there's any Done
			var loopInfo doneCallInfo
			if forStmt, ok := s.(*ast.ForStmt); ok && forStmt.Body != nil {
				loopInfo = wga.analyzeDoneCallsWithVisited(forStmt.Body, wgName, visited)
			} else if rangeStmt, ok := s.(*ast.RangeStmt); ok && rangeStmt.Body != nil {
				loopInfo = wga.analyzeDoneCallsWithVisited(rangeStmt.Body, wgName, visited)
			}
			info.hasAnyDone = info.hasAnyDone || loopInfo.hasAnyDone
			// Note: We don't set hasGuaranteedDone for loops

		case *ast.BlockStmt:
			// Nested block - analyze recursively
			blockInfo := wga.analyzeDoneCallsWithVisited(s, wgName, visited)
			info.hasAnyDone = info.hasAnyDone || blockInfo.hasAnyDone
			if blockInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}
		}
	}

	return info
}

func (wga *Analyzer) analyzeSwitchStatementWithVisited(switchStmt *ast.SwitchStmt, wgName string, visited map[token.Pos]bool) doneCallInfo {
	info := doneCallInfo{}
	hasDefault := false
	allCasesGuaranteed := true

	for _, stmt := range switchStmt.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}

		isDefaultCase := len(cc.List) == 0
		caseBlock := &ast.BlockStmt{List: cc.Body}
		caseInfo := wga.analyzeDoneCallsWithVisited(caseBlock, wgName, visited)

		info.hasAnyDone = info.hasAnyDone || caseInfo.hasAnyDone

		if isDefaultCase {
			hasDefault = true
		}

		if !caseInfo.hasGuaranteedDone {
			allCasesGuaranteed = false
		}
	}

	// Done is guaranteed only if there's a default AND all cases have guaranteed Done
	if hasDefault && allCasesGuaranteed {
		info.hasGuaranteedDone = true
	}

	return info
}

func (wga *Analyzer) analyzeTypeSwitchStatementWithVisited(typeSwitchStmt *ast.TypeSwitchStmt, wgName string, visited map[token.Pos]bool) doneCallInfo {
	info := doneCallInfo{}
	hasDefault := false
	allCasesGuaranteed := true

	for _, stmt := range typeSwitchStmt.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}

		isDefaultCase := len(cc.List) == 0
		caseBlock := &ast.BlockStmt{List: cc.Body}
		caseInfo := wga.analyzeDoneCallsWithVisited(caseBlock, wgName, visited)

		info.hasAnyDone = info.hasAnyDone || caseInfo.hasAnyDone

		if isDefaultCase {
			hasDefault = true
		}

		if !caseInfo.hasGuaranteedDone {
			allCasesGuaranteed = false
		}
	}

	if hasDefault && allCasesGuaranteed {
		info.hasGuaranteedDone = true
	}

	return info
}

func (wga *Analyzer) analyzeSelectStatementWithVisited(selectStmt *ast.SelectStmt, wgName string, visited map[token.Pos]bool) doneCallInfo {
	info := doneCallInfo{}
	hasDefault := false
	allCasesGuaranteed := true

	for _, stmt := range selectStmt.Body.List {
		cc, ok := stmt.(*ast.CommClause)
		if !ok {
			continue
		}

		isDefaultCase := cc.Comm == nil
		caseBlock := &ast.BlockStmt{List: cc.Body}
		caseInfo := wga.analyzeDoneCallsWithVisited(caseBlock, wgName, visited)

		info.hasAnyDone = info.hasAnyDone || caseInfo.hasAnyDone

		if isDefaultCase {
			hasDefault = true
		}

		if !caseInfo.hasGuaranteedDone {
			allCasesGuaranteed = false
		}
	}

	if hasDefault && allCasesGuaranteed {
		info.hasGuaranteedDone = true
	}

	return info
}

func (wga *Analyzer) analyzeRelatedCall(call *ast.CallExpr, wgName string, visited map[token.Pos]bool) (doneCallInfo, bool) {
	fn, calleeWGName, related := wga.relatedWaitGroupForCall(call, wgName)
	if !related || fn == nil || fn.Body == nil || calleeWGName == "" {
		return doneCallInfo{}, false
	}
	if visited[fn.Pos()] {
		return doneCallInfo{}, true
	}

	visited[fn.Pos()] = true
	defer delete(visited, fn.Pos())

	return wga.analyzeDoneCallsWithVisited(fn.Body, calleeWGName, visited), true
}

func (wga *Analyzer) callInvokesDone(call *ast.CallExpr, wgName string) bool {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok &&
		sel.Sel.Name == "Done" && common.GetVarName(sel.X) == wgName {
		return true
	}

	if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == wgName {
		return true
	}

	return wga.isSyncOnceDoWithCallback(call, wgName)
}

// isSimpleDeferDone checks if a defer statement is a simple defer wg.Done()
func (wga *Analyzer) isSimpleDeferDone(deferStmt *ast.DeferStmt, wgName string) bool {
	if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
		return call.Sel.Name == "Done" && common.GetVarName(call.X) == wgName
	}
	return false
}

func (wga *Analyzer) isCallbackDeferDone(deferStmt *ast.DeferStmt, wgName string) bool {
	if ident, ok := deferStmt.Call.Fun.(*ast.Ident); ok {
		return ident.Name == wgName
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

// isDeferFuncWithDone checks if a defer has a function literal that calls Done
func (wga *Analyzer) isDeferFuncWithDone(deferStmt *ast.DeferStmt, wgName string) bool {
	fnLit, ok := deferStmt.Call.Fun.(*ast.FuncLit)
	if !ok {
		return false
	}
	return wga.containsDoneCall(fnLit.Body, wgName)
}

func (wga *Analyzer) isSyncOnceDoWithCallback(call *ast.CallExpr, callbackName string) bool {
	if len(call.Args) == 0 {
		return false
	}

	hasCallbackArg := false
	for _, arg := range call.Args {
		if ident, ok := arg.(*ast.Ident); ok && ident.Name == callbackName {
			hasCallbackArg = true
			break
		}
	}
	if !hasCallbackArg {
		return false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Do" {
		return false
	}

	typ := wga.typesInfo.TypeOf(sel.X)
	if typ == nil {
		return false
	}
	if ptr, isPointer := typ.(*types.Pointer); isPointer {
		typ = ptr.Elem()
	}

	named, ok := typ.(*types.Named)
	return ok && named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == "sync" &&
		named.Obj().Name() == "Once"
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
