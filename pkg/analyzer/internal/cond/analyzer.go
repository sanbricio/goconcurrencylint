// Package cond detects misuse of sync.Cond.
package cond

import (
	"go/ast"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// Checker analyzes sync.Cond usage within one function.
type Checker struct {
	errorCollector report.Reporter
	typesInfo      *types.Info
}

// NewChecker creates a sync.Cond checker. typesInfo is the pass-wide type
// information used to confirm that a receiver is really a *sync.Cond and that
// a NewCond call resolves to sync.NewCond.
func NewChecker(errorCollector report.Reporter, typesInfo *types.Info) *Checker {
	return &Checker{
		errorCollector: errorCollector,
		typesInfo:      typesInfo,
	}
}

// AnalyzeFunction walks fn's body once, maintaining the stack of ancestor
// nodes so each c.Wait() call knows whether it sits inside a for/range loop.
// ast.Inspect calls the closure with the node on entry and with nil after its
// children, which is exactly the push/pop boundary the stack needs.
func (c *Checker) AnalyzeFunction(fn *ast.FuncDecl) {
	var stack []ast.Node
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1] // leaving a node: pop it off
			return true
		}
		if call, ok := n.(*ast.CallExpr); ok {
			// stack currently holds the ancestors of call, not call itself.
			c.checkCall(call, stack)
		}
		stack = append(stack, n) // entering a node: push it on
		return true
	})
}

func (c *Checker) checkCall(call *ast.CallExpr, ancestors []ast.Node) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	switch sel.Sel.Name {
	case "Wait":
		c.checkWait(call, sel, ancestors)
	case "NewCond":
		c.checkNewCond(call, sel)
	}
}

// checkWait flags cond.Wait() calls that are not enclosed by a loop (GCL4001).
// The type check on the receiver is what makes this precise: wg.Wait(),
// cmd.Wait() and any other Wait() on a non-Cond type are filtered out here.
func (c *Checker) checkWait(call *ast.CallExpr, sel *ast.SelectorExpr, ancestors []ast.Node) {
	if len(call.Args) != 0 {
		return
	}
	if !common.IsCond(c.typesInfo.TypeOf(sel.X)) {
		return
	}
	if enclosedByLoop(ancestors) {
		return
	}
	c.errorCollector.AddError(call.Pos(), category.CondWaitNotInLoop,
		condLabel(common.GetVarName(sel.X))+
			" Wait called outside a for loop (re-check the condition in a loop to handle spurious wakeups)")
}

// checkNewCond flags sync.NewCond(nil) (GCL4002). Only the literal nil is
// flagged; the object lookup confirms the callee is really sync.NewCond and
// not a same-named helper.
func (c *Checker) checkNewCond(call *ast.CallExpr, sel *ast.SelectorExpr) {
	if len(call.Args) != 1 {
		return
	}
	fn, ok := c.typesInfo.ObjectOf(sel.Sel).(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "sync" {
		return
	}
	if ident, ok := common.UnwrapParenExpr(call.Args[0]).(*ast.Ident); ok && ident.Name == "nil" {
		c.errorCollector.AddError(call.Pos(), category.CondNewNilLocker,
			"sync.NewCond called with nil Locker (Cond.Wait will panic at runtime)")
	}
}

// enclosedByLoop reports whether ancestors contains a for/range loop between
// the call and the innermost enclosing function. A FuncLit boundary stops the
// search: a loop outside a closure does not loop a Wait() inside it.
func enclosedByLoop(ancestors []ast.Node) bool {
	for i := len(ancestors) - 1; i >= 0; i-- {
		switch ancestors[i].(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			return true
		case *ast.FuncLit:
			return false
		}
	}
	return false
}

// condLabel renders the cond's name for a diagnostic, falling back to a
// generic label when the receiver cannot be reduced to a stable name.
func condLabel(name string) string {
	if name == "" || name == "?" {
		return "cond"
	}
	return "cond '" + name + "'"
}
