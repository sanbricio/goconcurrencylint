package waitgroup

import (
	"go/ast"
	"reflect"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/filesetup"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/primitives"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// SubAnalyzer drives the WaitGroup misuse checks as an independent
// analysis.Analyzer. It returns its diagnostic slice as Result; the
// umbrella analyzer re-emits them via pass.Report.
var SubAnalyzer = &analysis.Analyzer{
	Name:       "goconcurrencylint_waitgroup",
	Doc:        "Detects misuse of sync.WaitGroup (Add/Done/Wait imbalance, Add after Wait, etc.).",
	Run:        run,
	Requires:   []*analysis.Analyzer{inspect.Analyzer, primitives.Analyzer, filesetup.Analyzer},
	ResultType: reflect.TypeOf([]analysis.Diagnostic{}),
}

func run(pass *analysis.Pass) (any, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	pkg := pass.ResultOf[primitives.Analyzer].(*primitives.Result)
	files := pass.ResultOf[filesetup.Analyzer].(*filesetup.Result)
	ec := &report.ErrorCollector{}

	insp.Preorder([]ast.Node{(*ast.FuncDecl)(nil)}, func(n ast.Node) {
		fn := n.(*ast.FuncDecl)
		if fn.Body == nil {
			return
		}
		tokFile := pass.Fset.File(fn.Pos())
		if files.IsGenerated(tokFile) {
			return
		}

		fr := primitives.ForFunction(fn, pass, pkg)
		if !primitives.HasWaitGroups(fr) {
			return
		}

		cf := files.FilterFor(tokFile)
		c := NewChecker(fr, ec, cf, pass)
		c.AnalyzeFunction(fn)
	})

	return ec.Diagnostics(pass, files.IgnoreFunc()), nil
}
