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

func (c *Checker) goroutineDoneInfo(goStmt *ast.GoStmt, wgName string) (doneCallInfo, bool) {
	if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
		if !goroutineRelatedToWaitGroup(goStmt, wgName) {
			return doneCallInfo{}, false
		}
		return c.analyzeDoneCallsWithVisited(fnLit.Body, wgName, make(map[token.Pos]bool)), true
	}

	return c.analyzeRelatedCall(goStmt.Call, wgName, make(map[token.Pos]bool))
}

func (c *Checker) analyzeDoneCallsWithVisited(block *ast.BlockStmt, wgName string, visited map[token.Pos]bool) doneCallInfo {
	info := doneCallInfo{}
	// Tracks whether a prior branch could exit early, making subsequent statements conditional
	mightExitEarly := false

	for _, stmt := range block.List {
		if c.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		// If we already found guaranteed Done, we can return early
		if info.hasGuaranteedDone {
			return info
		}

		switch s := stmt.(type) {
		case *ast.DeferStmt:
			if c.worker.isSimpleDeferDone(s, wgName) || c.worker.isCallbackDeferDone(s, wgName) || c.worker.isDeferPanicRecoveryPattern(s, wgName) || c.worker.isDeferFuncWithDone(s, wgName) {
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
					c.worker.isLocallyCreatedChannel(chanName) && !c.worker.hasChannelSends(chanName) {
					mightExitEarly = true
				}
			}

			// Direct Done() call
			if call, ok := s.X.(*ast.CallExpr); ok {
				if c.worker.callInvokesDone(call, wgName) {
					info.hasAnyDone = true
					if !mightExitEarly {
						info.hasGuaranteedDone = true
						return info
					}
				}

				helperInfo, related := c.analyzeRelatedCall(call, wgName, visited)
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
			thenInfo := c.analyzeDoneCallsWithVisited(s.Body, wgName, visited)
			info.hasAnyDone = info.hasAnyDone || thenInfo.hasAnyDone

			thenTerminates := c.worker.blockAlwaysTerminates(s.Body)

			if s.Else != nil {
				var elseInfo doneCallInfo
				var elseTerminates bool
				if elseBlock, ok := s.Else.(*ast.BlockStmt); ok {
					elseInfo = c.analyzeDoneCallsWithVisited(elseBlock, wgName, visited)
					elseTerminates = c.worker.blockAlwaysTerminates(elseBlock)
				} else if elseIf, ok := s.Else.(*ast.IfStmt); ok {
					elseBlock := &ast.BlockStmt{List: []ast.Stmt{elseIf}}
					elseInfo = c.analyzeDoneCallsWithVisited(elseBlock, wgName, visited)
					elseTerminates = c.worker.blockAlwaysTerminates(elseBlock)
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
			switchInfo := c.analyzeSwitchStatementWithVisited(s, wgName, visited)
			info.hasAnyDone = info.hasAnyDone || switchInfo.hasAnyDone
			if switchInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}

		case *ast.TypeSwitchStmt:
			typeSwitchInfo := c.analyzeTypeSwitchStatementWithVisited(s, wgName, visited)
			info.hasAnyDone = info.hasAnyDone || typeSwitchInfo.hasAnyDone
			if typeSwitchInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}

		case *ast.SelectStmt:
			selectInfo := c.analyzeSelectStatementWithVisited(s, wgName, visited)
			info.hasAnyDone = info.hasAnyDone || selectInfo.hasAnyDone
			if selectInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}

		case *ast.GoStmt:
			goInfo, related := c.goroutineDoneInfo(s, wgName)
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
				loopInfo = c.analyzeDoneCallsWithVisited(forStmt.Body, wgName, visited)
				if c.loopHasCancellationDoneExit(forStmt.Body, wgName, visited) && !mightExitEarly {
					info.hasAnyDone = true
					info.hasGuaranteedDone = true
					return info
				}
				// A condition-less `for {}` always enters its body, so a Done the
				// body itself guarantees (before any conditional break/return)
				// runs at least once.
				if forStmt.Cond == nil && loopInfo.hasGuaranteedDone && !mightExitEarly {
					info.hasAnyDone = true
					info.hasGuaranteedDone = true
					return info
				}
			} else if rangeStmt, ok := s.(*ast.RangeStmt); ok && rangeStmt.Body != nil {
				loopInfo = c.analyzeDoneCallsWithVisited(rangeStmt.Body, wgName, visited)
				if c.loopHasCancellationDoneExit(rangeStmt.Body, wgName, visited) && !mightExitEarly {
					info.hasAnyDone = true
					info.hasGuaranteedDone = true
					return info
				}
			}
			info.hasAnyDone = info.hasAnyDone || loopInfo.hasAnyDone
			// Note: We don't set hasGuaranteedDone for loops

		case *ast.BlockStmt:
			// Nested block - analyze recursively
			blockInfo := c.analyzeDoneCallsWithVisited(s, wgName, visited)
			info.hasAnyDone = info.hasAnyDone || blockInfo.hasAnyDone
			if blockInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}

		case *ast.LabeledStmt:
			labeledInfo := c.analyzeDoneCallsWithVisited(&ast.BlockStmt{List: []ast.Stmt{s.Stmt}}, wgName, visited)
			info.hasAnyDone = info.hasAnyDone || labeledInfo.hasAnyDone
			if labeledInfo.hasGuaranteedDone {
				info.hasGuaranteedDone = true
				return info
			}
		}
	}

	return info
}

func (c *Checker) analyzeSwitchStatementWithVisited(switchStmt *ast.SwitchStmt, wgName string, visited map[token.Pos]bool) doneCallInfo {
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
		caseInfo := c.analyzeDoneCallsWithVisited(caseBlock, wgName, visited)

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

func (c *Checker) analyzeTypeSwitchStatementWithVisited(typeSwitchStmt *ast.TypeSwitchStmt, wgName string, visited map[token.Pos]bool) doneCallInfo {
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
		caseInfo := c.analyzeDoneCallsWithVisited(caseBlock, wgName, visited)

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

func (c *Checker) analyzeSelectStatementWithVisited(selectStmt *ast.SelectStmt, wgName string, visited map[token.Pos]bool) doneCallInfo {
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
		caseInfo := c.analyzeDoneCallsWithVisited(caseBlock, wgName, visited)

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

func (c *Checker) analyzeRelatedCall(call *ast.CallExpr, wgName string, visited map[token.Pos]bool) (doneCallInfo, bool) {
	fn, calleeWGName, related := c.relatedWaitGroupForCall(call, wgName)
	if !related || fn == nil || fn.Body == nil || calleeWGName == "" {
		return doneCallInfo{}, false
	}
	if visited[fn.Pos()] {
		return doneCallInfo{}, true
	}

	visited[fn.Pos()] = true
	defer delete(visited, fn.Pos())

	return c.analyzeDoneCallsWithVisited(fn.Body, calleeWGName, visited), true
}
