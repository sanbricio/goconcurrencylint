// Package cond detects misuse of sync.Cond.
package cond

import (
	"go/ast"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// Checker reports sync.Cond misuse for one pass. It currently flags a single
// pattern: sync.NewCond(nil), whose nil Locker panics the first time the Cond
// is used.
//
// A "Wait outside a loop" check was intentionally dropped: in Go, Cond.Wait
// does not wake spuriously, so calling Wait once under a condition guard is a
// legitimate and common idiom (one-shot signals, Cond wrappers, test helpers).
// staticcheck and go vet omit the same check for that reason.
type Checker struct {
	errorCollector report.Reporter
	typesInfo      *types.Info
}

// NewChecker creates a sync.Cond checker. typesInfo is the pass-wide type
// information used to confirm a NewCond call resolves to sync.NewCond.
func NewChecker(errorCollector report.Reporter, typesInfo *types.Info) *Checker {
	return &Checker{
		errorCollector: errorCollector,
		typesInfo:      typesInfo,
	}
}

// CheckCall flags sync.NewCond(nil) (GCL4001). Only the literal nil is flagged;
// the object lookup confirms the callee is really sync.NewCond and not a
// same-named helper.
func (c *Checker) CheckCall(call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "NewCond" || len(call.Args) != 1 {
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
