package waitgroup

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// doneCallInfo contains information about Done calls in a block
type doneCallInfo struct {
	hasAnyDone        bool
	hasGuaranteedDone bool
}

// goroutineRelatedToWaitGroup checks if a goroutine is related to a WaitGroup
func goroutineRelatedToWaitGroup(goStmt *ast.GoStmt, wgName string) bool {
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

func (wga *Checker) goroutineDoneInfo(goStmt *ast.GoStmt, wgName string) (doneCallInfo, bool) {
	if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
		if !goroutineRelatedToWaitGroup(goStmt, wgName) {
			return doneCallInfo{}, false
		}
		return wga.analyzeDoneCallsWithVisited(fnLit.Body, wgName, make(map[token.Pos]bool)), true
	}

	return wga.analyzeRelatedCall(goStmt.Call, wgName, make(map[token.Pos]bool))
}

func (wga *Checker) analyzeDoneCallsWithVisited(block *ast.BlockStmt, wgName string, visited map[token.Pos]bool) doneCallInfo {
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
			if wga.worker.isSimpleDeferDone(s, wgName) || wga.worker.isCallbackDeferDone(s, wgName) || wga.worker.isDeferPanicRecoveryPattern(s, wgName) || wga.worker.isDeferFuncWithDone(s, wgName) {
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
					wga.worker.isLocallyCreatedChannel(chanName) && !wga.worker.hasChannelSends(chanName) {
					mightExitEarly = true
				}
			}

			// Direct Done() call
			if call, ok := s.X.(*ast.CallExpr); ok {
				if wga.worker.callInvokesDone(call, wgName) {
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

			thenTerminates := wga.worker.blockAlwaysTerminates(s.Body)

			if s.Else != nil {
				var elseInfo doneCallInfo
				var elseTerminates bool
				if elseBlock, ok := s.Else.(*ast.BlockStmt); ok {
					elseInfo = wga.analyzeDoneCallsWithVisited(elseBlock, wgName, visited)
					elseTerminates = wga.worker.blockAlwaysTerminates(elseBlock)
				} else if elseIf, ok := s.Else.(*ast.IfStmt); ok {
					elseBlock := &ast.BlockStmt{List: []ast.Stmt{elseIf}}
					elseInfo = wga.analyzeDoneCallsWithVisited(elseBlock, wgName, visited)
					elseTerminates = wga.worker.blockAlwaysTerminates(elseBlock)
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
				if wga.loopHasCancellationDoneExit(forStmt.Body, wgName, visited) && !mightExitEarly {
					info.hasAnyDone = true
					info.hasGuaranteedDone = true
					return info
				}
			} else if rangeStmt, ok := s.(*ast.RangeStmt); ok && rangeStmt.Body != nil {
				loopInfo = wga.analyzeDoneCallsWithVisited(rangeStmt.Body, wgName, visited)
				if wga.loopHasCancellationDoneExit(rangeStmt.Body, wgName, visited) && !mightExitEarly {
					info.hasAnyDone = true
					info.hasGuaranteedDone = true
					return info
				}
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

		case *ast.LabeledStmt:
			labeledInfo := wga.analyzeDoneCallsWithVisited(&ast.BlockStmt{List: []ast.Stmt{s.Stmt}}, wgName, visited)
			info.hasAnyDone = info.hasAnyDone || labeledInfo.hasAnyDone
			if labeledInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}
		}
	}

	return info
}

func (wga *Checker) analyzeSwitchStatementWithVisited(switchStmt *ast.SwitchStmt, wgName string, visited map[token.Pos]bool) doneCallInfo {
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

func (wga *Checker) analyzeTypeSwitchStatementWithVisited(typeSwitchStmt *ast.TypeSwitchStmt, wgName string, visited map[token.Pos]bool) doneCallInfo {
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

func (wga *Checker) analyzeSelectStatementWithVisited(selectStmt *ast.SelectStmt, wgName string, visited map[token.Pos]bool) doneCallInfo {
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

func (wga *Checker) analyzeRelatedCall(call *ast.CallExpr, wgName string, visited map[token.Pos]bool) (doneCallInfo, bool) {
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
