package waitgroup

import (
	"go/ast"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

func (w *workerDoneAnalyzer) callInvokesDone(call *ast.CallExpr, wgName string) bool {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok &&
		sel.Sel.Name == "Done" && common.GetVarName(sel.X) == wgName {
		return true
	}

	if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == wgName {
		return true
	}

	return w.isSyncOnceDoWithCallback(call, wgName)
}

// isSimpleDeferDone checks if a defer statement is a simple defer wg.Done()
func (w *workerDoneAnalyzer) isSimpleDeferDone(deferStmt *ast.DeferStmt, wgName string) bool {
	if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
		return call.Sel.Name == "Done" && common.GetVarName(call.X) == wgName
	}
	return false
}

func (w *workerDoneAnalyzer) isCallbackDeferDone(deferStmt *ast.DeferStmt, wgName string) bool {
	if ident, ok := deferStmt.Call.Fun.(*ast.Ident); ok {
		return ident.Name == wgName
	}
	return false
}

// deferInvokesDone reports whether a defer statement invokes Done on wgName.
func (w *workerDoneAnalyzer) deferInvokesDone(deferStmt *ast.DeferStmt, wgName string) bool {
	if w.isSimpleDeferDone(deferStmt, wgName) || w.isCallbackDeferDone(deferStmt, wgName) {
		return true
	}
	if fnLit, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok && fnLit.Body != nil {
		return w.containsDoneCall(fnLit.Body, wgName)
	}
	return false
}

// isDeferPanicRecoveryPattern detects panic recovery pattern
func (w *workerDoneAnalyzer) isDeferPanicRecoveryPattern(deferStmt *ast.DeferStmt, wgName string) bool {
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
			if hasPanicRecovery || w.isRecoverCheck(ifStmt) {
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
func (w *workerDoneAnalyzer) isDeferFuncWithDone(deferStmt *ast.DeferStmt, wgName string) bool {
	fnLit, ok := deferStmt.Call.Fun.(*ast.FuncLit)
	if !ok {
		return false
	}
	return w.containsDoneCall(fnLit.Body, wgName)
}

func (w *workerDoneAnalyzer) isSyncOnceDoWithCallback(call *ast.CallExpr, callbackName string) bool {
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

	typ := w.typesInfo.TypeOf(sel.X)
	typ = common.DerefOnce(typ)

	return common.MatchesPkgAndName(typ, "sync", "Once")
}

// isRecoverCheck checks if an if statement is checking recover() result
func (w *workerDoneAnalyzer) isRecoverCheck(ifStmt *ast.IfStmt) bool {
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
