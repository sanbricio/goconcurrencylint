package waitgroup

import (
	"go/ast"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/category"
)

func (wga *Analyzer) checkDoneNotDeferredInWorker() {
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || wga.commentFilter.ShouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}
		for wgName := range wga.waitGroupNames {
			wga.checkDoneNotDeferredInBlock(fnLit.Body.List, wgName, false)
		}
		return true
	})
}

func (wga *Analyzer) checkDoneNotDeferredInBlock(stmts []ast.Stmt, wgName string, riskyBefore bool) bool {
	risky := riskyBefore
	for _, stmt := range stmts {
		if stmt == nil || wga.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		switch s := stmt.(type) {
		case *ast.DeferStmt:
			if wga.deferInvokesDone(s, wgName) {
				continue
			}
			// A defer registers the call for execution at function exit; the
			// deferred call itself does not run inline. Only the argument
			// expressions are evaluated synchronously, so only those can mark
			// subsequent code as risky.
			if wga.deferArgsMayAbortWorker(s, wgName) {
				risky = true
			}
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok && wga.callInvokesDone(call, wgName) {
				if risky {
					wga.errorCollector.AddError(call.Pos(), category.DoneNotDeferred, "waitgroup '"+wgName+"' Done should be deferred so it runs on panic or runtime.Goexit")
				}
				continue
			}
			if wga.statementMayAbortWorker(s, wgName) {
				risky = true
			}
		case *ast.IfStmt:
			thenRisky := wga.checkDoneNotDeferredInBlock(s.Body.List, wgName, risky)
			elseRisky := risky
			if s.Else != nil {
				elseRisky = wga.checkDoneNotDeferredInElse(s.Else, wgName, risky)
			}
			risky = thenRisky || elseRisky
		case *ast.BlockStmt:
			risky = wga.checkDoneNotDeferredInBlock(s.List, wgName, risky)
		case *ast.LabeledStmt:
			risky = wga.checkDoneNotDeferredInBlock([]ast.Stmt{s.Stmt}, wgName, risky)
		default:
			if wga.statementMayAbortWorker(s, wgName) {
				risky = true
			}
		}
		if wga.isTerminatingStatement(stmt) {
			return risky
		}
	}
	return risky
}

func (wga *Analyzer) checkDoneNotDeferredInElse(stmt ast.Stmt, wgName string, risky bool) bool {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return wga.checkDoneNotDeferredInBlock(s.List, wgName, risky)
	case *ast.IfStmt:
		return wga.checkDoneNotDeferredInBlock([]ast.Stmt{s}, wgName, risky)
	default:
		return risky
	}
}

// deferArgsMayAbortWorker reports whether evaluating the arguments of a
// deferred call can explicitly abort the worker. The deferred call itself runs
// at function exit, so its body cannot interrupt the ongoing flow; only the
// argument expressions evaluated at the defer point can.
func (wga *Analyzer) deferArgsMayAbortWorker(stmt *ast.DeferStmt, wgName string) bool {
	if stmt == nil || stmt.Call == nil {
		return false
	}
	for _, arg := range stmt.Call.Args {
		if wga.exprMayAbortWorker(arg, wgName) {
			return true
		}
	}
	return false
}

func (wga *Analyzer) exprMayAbortWorker(expr ast.Expr, wgName string) bool {
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
		if !ok || wga.commentFilter.ShouldSkipCall(call) {
			return true
		}
		if wga.isWaitGroupHousekeepingCall(call, wgName) {
			return true
		}
		if wga.callAbortsWorker(call) {
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

func (wga *Analyzer) statementMayAbortWorker(stmt ast.Stmt, wgName string) bool {
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
		if !ok || wga.commentFilter.ShouldSkipCall(call) {
			return true
		}
		if wga.isWaitGroupHousekeepingCall(call, wgName) {
			return true
		}
		if wga.callAbortsWorker(call) {
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

func (wga *Analyzer) callAbortsWorker(call *ast.CallExpr) bool {
	if call == nil {
		return false
	}

	if ident, ok := call.Fun.(*ast.Ident); ok {
		return wga.isBuiltinPanic(ident)
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Goexit" {
		return false
	}

	if wga.typesInfo != nil {
		if obj := wga.typesInfo.ObjectOf(sel.Sel); obj != nil && obj.Pkg() != nil {
			return obj.Pkg().Path() == "runtime"
		}
	}

	return common.GetVarName(sel.X) == "runtime"
}

func (wga *Analyzer) isBuiltinPanic(ident *ast.Ident) bool {
	if ident == nil || ident.Name != "panic" {
		return false
	}
	if wga.typesInfo == nil {
		return true
	}
	obj := wga.typesInfo.ObjectOf(ident)
	if obj == nil {
		return true
	}
	_, ok := obj.(*types.Builtin)
	return ok
}

func (wga *Analyzer) isWaitGroupHousekeepingCall(call *ast.CallExpr, wgName string) bool {
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
