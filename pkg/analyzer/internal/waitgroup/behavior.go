package waitgroup

import "go/ast"

func (wga *Checker) deferInvokesDone(deferStmt *ast.DeferStmt, wgName string) bool {
	if wga.isSimpleDeferDone(deferStmt, wgName) || wga.isCallbackDeferDone(deferStmt, wgName) {
		return true
	}
	if fnLit, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok && fnLit.Body != nil {
		return wga.containsDoneCall(fnLit.Body, wgName)
	}
	return false
}
