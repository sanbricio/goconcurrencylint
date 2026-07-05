package pool

import (
	"go/ast"
	"reflect"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/filesetup"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// SubAnalyzer drives the sync.Pool misuse checks as an independent
// analysis.Analyzer. Like the other sub-analyzers it returns its diagnostics as
// Result so the umbrella Analyzer re-emits them (and analysistest observes them
// through the umbrella).
//
// The checks are keyed off Put call sites and Pool.New assignments, so this
// sub-analyzer skips the per-function driver skeleton entirely: it walks the
// relevant nodes directly and cheap-rejects before touching type information,
// keeping it near-free on large packages.
var SubAnalyzer = &analysis.Analyzer{
	Name:       "goconcurrencylint_pool",
	Doc:        "Detects non-pointer values placed in a sync.Pool (Put argument or New return), which box and allocate on every call.",
	Run:        run,
	Requires:   []*analysis.Analyzer{inspect.Analyzer, filesetup.Analyzer},
	ResultType: reflect.TypeFor[[]analysis.Diagnostic](),
}

func run(pass *analysis.Pass) (any, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	files := pass.ResultOf[filesetup.Analyzer].(*filesetup.Result)
	ec := &report.ErrorCollector{}
	checker := NewChecker(ec, pass.TypesInfo)

	nodeFilter := []ast.Node{
		(*ast.CallExpr)(nil),
		(*ast.CompositeLit)(nil),
		(*ast.AssignStmt)(nil),
	}
	insp.Preorder(nodeFilter, func(n ast.Node) {
		if files.IsGenerated(pass.Fset.File(n.Pos())) {
			return
		}
		switch node := n.(type) {
		case *ast.CallExpr:
			// Cheap reject on the selector name before any type lookup, since
			// most calls are not Put.
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Put" {
				checker.CheckCall(node)
			}
		case *ast.CompositeLit:
			checker.CheckCompositeLit(node)
		case *ast.AssignStmt:
			checker.CheckAssign(node)
		}
	})

	return ec.Diagnostics(pass, files.IgnoreFunc()), nil
}
