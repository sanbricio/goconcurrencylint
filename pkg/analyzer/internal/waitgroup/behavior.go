package waitgroup

import "go/ast"

func (w *workerDoneAnalyzer) deferInvokesDone(deferStmt *ast.DeferStmt, wgName string) bool {
	if w.isSimpleDeferDone(deferStmt, wgName) || w.isCallbackDeferDone(deferStmt, wgName) {
		return true
	}
	if fnLit, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok && fnLit.Body != nil {
		return w.containsDoneCall(fnLit.Body, wgName)
	}
	return false
}
