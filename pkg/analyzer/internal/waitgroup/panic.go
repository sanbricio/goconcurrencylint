package waitgroup

import (
	"go/ast"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

func (w *workerDoneAnalyzer) checkDoneNotDeferredInWorker() {
	ast.Inspect(w.function.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || w.commentFilter.ShouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}
		for wgName := range w.waitGroupNames {
			w.checkDoneNotDeferredInBlock(fnLit.Body.List, wgName, false)
		}
		return true
	})
}

func (w *workerDoneAnalyzer) checkDoneNotDeferredInBlock(stmts []ast.Stmt, wgName string, riskyBefore bool) bool {
	risky := riskyBefore
	for _, stmt := range stmts {
		if stmt == nil || w.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		switch s := stmt.(type) {
		case *ast.DeferStmt:
			if w.deferInvokesDone(s, wgName) {
				continue
			}
			// A defer registers the call for execution at function exit; the
			// deferred call itself does not run inline. Only the argument
			// expressions are evaluated synchronously, so only those can mark
			// subsequent code as risky.
			if w.deferArgsMayAbortWorker(s, wgName) {
				risky = true
			}
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok && w.callInvokesDone(call, wgName) {
				if risky {
					w.errorCollector.AddError(call.Pos(), category.DoneNotDeferred, "waitgroup '"+wgName+"' Done should be deferred so it runs on panic or runtime.Goexit")
				}
				continue
			}
			if w.statementMayAbortWorker(s, wgName) {
				risky = true
			}
		case *ast.IfStmt:
			thenRisky := w.checkDoneNotDeferredInBlock(s.Body.List, wgName, risky)
			elseRisky := risky
			if s.Else != nil {
				elseRisky = w.checkDoneNotDeferredInElse(s.Else, wgName, risky)
			}
			risky = thenRisky || elseRisky
		case *ast.BlockStmt:
			risky = w.checkDoneNotDeferredInBlock(s.List, wgName, risky)
		case *ast.LabeledStmt:
			risky = w.checkDoneNotDeferredInBlock([]ast.Stmt{s.Stmt}, wgName, risky)
		default:
			if w.statementMayAbortWorker(s, wgName) {
				risky = true
			}
		}
		if w.isTerminatingStatement(stmt) {
			return risky
		}
	}
	return risky
}

func (w *workerDoneAnalyzer) checkDoneNotDeferredInElse(stmt ast.Stmt, wgName string, risky bool) bool {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return w.checkDoneNotDeferredInBlock(s.List, wgName, risky)
	case *ast.IfStmt:
		return w.checkDoneNotDeferredInBlock([]ast.Stmt{s}, wgName, risky)
	default:
		return risky
	}
}

// deferArgsMayAbortWorker reports whether evaluating the arguments of a
// deferred call can explicitly abort the worker. The deferred call itself runs
// at function exit, so its body cannot interrupt the ongoing flow; only the
// argument expressions evaluated at the defer point can.
func (w *workerDoneAnalyzer) deferArgsMayAbortWorker(stmt *ast.DeferStmt, wgName string) bool {
	if stmt == nil || stmt.Call == nil {
		return false
	}
	for _, arg := range stmt.Call.Args {
		if w.exprMayAbortWorker(arg, wgName) {
			return true
		}
	}
	return false
}

func (w *workerDoneAnalyzer) exprMayAbortWorker(expr ast.Expr, wgName string) bool {
	if expr == nil {
		return false
	}
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || w.commentFilter.ShouldSkipCall(call) {
			return true
		}
		if w.isWaitGroupHousekeepingCall(call, wgName) {
			return true
		}
		if w.callAbortsWorker(call) {
			found = true
			return false
		}
		// Plain helper calls are not risky by themselves, but keep walking
		// their receiver and arguments so explicit aborts in expressions are
		// still discovered.
		return true
	})
	return found
}

func (w *workerDoneAnalyzer) statementMayAbortWorker(stmt ast.Stmt, wgName string) bool {
	if _, ok := stmt.(*ast.GoStmt); ok {
		return false
	}
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || w.commentFilter.ShouldSkipCall(call) {
			return true
		}
		if w.isWaitGroupHousekeepingCall(call, wgName) {
			return true
		}
		if w.callAbortsWorker(call) {
			found = true
			return false
		}
		// Plain helper calls are not risky by themselves, but keep walking
		// their receiver and arguments so explicit aborts in expressions are
		// still discovered.
		return true
	})
	return found
}

func (w *workerDoneAnalyzer) callAbortsWorker(call *ast.CallExpr) bool {
	if call == nil {
		return false
	}

	if ident, ok := call.Fun.(*ast.Ident); ok {
		return w.isBuiltinPanic(ident)
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Goexit" {
		return false
	}

	if w.typesInfo != nil {
		if obj := w.typesInfo.ObjectOf(sel.Sel); obj != nil && obj.Pkg() != nil {
			return obj.Pkg().Path() == "runtime"
		}
	}

	return common.GetVarName(sel.X) == "runtime"
}

func (w *workerDoneAnalyzer) isBuiltinPanic(ident *ast.Ident) bool {
	if ident == nil || ident.Name != "panic" {
		return false
	}
	if w.typesInfo == nil {
		return true
	}
	obj := w.typesInfo.ObjectOf(ident)
	if obj == nil {
		return true
	}
	_, ok := obj.(*types.Builtin)
	return ok
}

func (w *workerDoneAnalyzer) isWaitGroupHousekeepingCall(call *ast.CallExpr, wgName string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || common.GetVarName(sel.X) != wgName {
		return false
	}
	switch sel.Sel.Name {
	case "Add", "Done", "Wait", "Go":
		return true
	default:
		return false
	}
}
