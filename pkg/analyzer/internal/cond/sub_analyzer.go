package cond

import (
	"go/ast"
	"reflect"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/filesetup"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// SubAnalyzer drives the sync.Cond misuse checks as an independent
// analysis.Analyzer. Like the other sub-analyzers it returns its diagnostics
// as Result so the umbrella Analyzer re-emits them (and analysistest observes
// them through the umbrella).
//
// The only check left is NewCond(nil), which is keyed off rare NewCond call
// sites, so this sub-analyzer skips the per-function driver skeleton entirely:
// it walks call expressions directly and cheap-rejects on the callee name
// before touching type information. That keeps it near-free on large packages.
var SubAnalyzer = &analysis.Analyzer{
	Name:       "goconcurrencylint_cond",
	Doc:        "Detects misuse of sync.Cond: NewCond(nil).",
	Run:        run,
	Requires:   []*analysis.Analyzer{inspect.Analyzer, filesetup.Analyzer},
	ResultType: reflect.TypeFor[[]analysis.Diagnostic](),
}

func run(pass *analysis.Pass) (any, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	files := pass.ResultOf[filesetup.Analyzer].(*filesetup.Result)
	ec := &report.ErrorCollector{}
	checker := NewChecker(ec, pass.TypesInfo)

	insp.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node) {
		call := n.(*ast.CallExpr)
		// Cheap reject first: the vast majority of calls are not NewCond, so
		// filter on the selector name before any type lookup or file check.
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "NewCond" {
			return
		}
		if files.IsGenerated(pass.Fset.File(call.Pos())) {
			return
		}
		checker.CheckCall(call)
	})

	return ec.Diagnostics(pass, files.IgnoreFunc()), nil
}
