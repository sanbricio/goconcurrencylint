// Package callscan provides the shared skeleton for sub-analyzers that flag
// specific call expressions by callee name: walk every call in the package,
// cheap-reject on the callee selector before any type lookup, skip generated
// files, and hand the survivors to a checker. It mirrors driver.Run's shape
// for the per-function skeleton (mutex, waitgroup, once), but for a
// package-wide call-expression scan (cond's NewCond, once's OnceFunc family).
package callscan

import (
	"go/ast"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/filesetup"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Checker is the minimal interface a call-scan checker must satisfy. Check is
// called once per call expression whose selector passes Config.Accept.
type Checker interface {
	Check(call *ast.CallExpr, sel *ast.SelectorExpr)
}

// Config parameterizes the shared call-scan skeleton.
type Config[C Checker] struct {
	// SelectorOf extracts the callee selector from a call's Fun expression,
	// peeling any shape the sub-analyzer needs to see through (e.g. generic
	// instantiations). A nil return means the call is not one this scan
	// considers at all.
	SelectorOf func(fun ast.Expr) *ast.SelectorExpr

	// Accept is the cheap, name-only reject applied before any type lookup.
	Accept func(name string) bool

	// NewChecker constructs the checker once per pass, wired to the shared
	// ErrorCollector so its diagnostics land in this scan's Result.
	NewChecker func(ec report.Reporter, pass *analysis.Pass) C
}

// Run executes the shared call-scan skeleton described by cfg and returns the
// collected diagnostics. It is intended to be used directly as the body of a
// sub-analyzer's run function:
//
//	func run(pass *analysis.Pass) (any, error) {
//	    return callscan.Run(pass, callscan.Config[*Checker]{...}), nil
//	}
func Run[C Checker](pass *analysis.Pass, cfg Config[C]) []analysis.Diagnostic {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	files := pass.ResultOf[filesetup.Analyzer].(*filesetup.Result)
	ec := &report.ErrorCollector{}
	checker := cfg.NewChecker(ec, pass)

	insp.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node) {
		call := n.(*ast.CallExpr)
		// Cheap reject first: filter on the selector name before any type
		// lookup or file check, since most calls are not ones this scan cares
		// about.
		sel := cfg.SelectorOf(call.Fun)
		if sel == nil || !cfg.Accept(sel.Sel.Name) {
			return
		}
		if files.IsGenerated(pass.Fset.File(call.Pos())) {
			return
		}
		checker.Check(call, sel)
	})

	return ec.Diagnostics(pass, files.IgnoreFunc())
}

// PlainSelector is the common SelectorOf for scans that only care about a
// direct method/function selector (x.Name(...)), not generic instantiations.
func PlainSelector(fun ast.Expr) *ast.SelectorExpr {
	sel, _ := fun.(*ast.SelectorExpr)
	return sel
}
