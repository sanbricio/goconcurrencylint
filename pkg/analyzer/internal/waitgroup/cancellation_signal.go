package waitgroup

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

func (wga *Checker) loopHasCancellationDoneExit(body *ast.BlockStmt, wgName string, visited map[token.Pos]bool) bool {
	if body == nil {
		return false
	}

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		selectStmt, ok := n.(*ast.SelectStmt)
		if !ok {
			return true
		}
		for _, stmt := range selectStmt.Body.List {
			cc, ok := stmt.(*ast.CommClause)
			if !ok || !wga.commClauseReceivesDoneSignal(cc) {
				continue
			}
			caseBlock := &ast.BlockStmt{List: cc.Body}
			caseInfo := wga.analyzeDoneCallsWithVisited(caseBlock, wgName, visited)
			if caseInfo.hasGuaranteedDone && wga.worker.blockAlwaysTerminates(caseBlock) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func (wga *Checker) commClauseReceivesDoneSignal(cc *ast.CommClause) bool {
	if cc == nil || cc.Comm == nil {
		return false
	}

	switch comm := cc.Comm.(type) {
	case *ast.ExprStmt:
		return wga.exprReceivesDoneSignal(comm.X)
	case *ast.AssignStmt:
		if slices.ContainsFunc(comm.Rhs, wga.exprReceivesDoneSignal) {
			return true
		}
	}
	return false
}

func (wga *Checker) exprReceivesDoneSignal(expr ast.Expr) bool {
	unary, ok := expr.(*ast.UnaryExpr)
	if !ok || unary.Op != token.ARROW {
		return false
	}
	switch x := unary.X.(type) {
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "Done" && wga.callReturnsContextDoneSignal(sel.X, x)
	case *ast.Ident:
		// `case <-chClose:` where chClose is closed in the enclosing function —
		// the "close to broadcast cancellation" pattern.
		return wga.identIsClosedChannel(x.Name)
	}
	return false
}

func (wga *Checker) identIsClosedChannel(name string) bool {
	if name == "" || wga.function == nil || wga.function.Body == nil {
		return false
	}
	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		callee, ok := call.Fun.(*ast.Ident)
		if !ok || callee.Name != "close" || len(call.Args) != 1 {
			return true
		}
		if arg, ok := call.Args[0].(*ast.Ident); ok && arg.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func (wga *Checker) callReturnsContextDoneSignal(receiver ast.Expr, call *ast.CallExpr) bool {
	if wga.typesInfo == nil {
		return false
	}
	receiverType := types.Unalias(wga.typesInfo.TypeOf(receiver))
	if !common.MatchesPkgAndName(receiverType, "context", "Context") {
		return false
	}
	typ := types.Unalias(wga.typesInfo.TypeOf(call))
	ch, ok := typ.(*types.Chan)
	if !ok || ch.Dir() == types.SendOnly {
		return false
	}
	elem := types.Unalias(ch.Elem()).Underlying()
	st, ok := elem.(*types.Struct)
	return ok && st.NumFields() == 0
}
