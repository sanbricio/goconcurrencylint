package channel

import (
	"go/ast"
	"reflect"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/filesetup"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// SubAnalyzer drives the channel misuse checks as an independent
// analysis.Analyzer. Like the other sub-analyzers it returns its diagnostics as
// Result so the umbrella Analyzer re-emits them (and analysistest observes them
// through the umbrella).
//
// It visits every function scope — top-level declarations and function literals
// alike — and analyzes each body on its own, tracking only the channel
// variables declared inside that scope. A function literal is therefore checked
// as its own scope; the enclosing body does not descend into it, but it does
// consider variables the literal reassigns when deciding what to track (see
// poisonedVars).
var SubAnalyzer = &analysis.Analyzer{
	Name:       "goconcurrencylint_channel",
	Doc:        "Detects channel misuse: close of a nil or closed channel, send on a closed channel, and send/receive on a nil channel.",
	Run:        run,
	Requires:   []*analysis.Analyzer{inspect.Analyzer, filesetup.Analyzer},
	ResultType: reflect.TypeFor[[]analysis.Diagnostic](),
}

func run(pass *analysis.Pass) (any, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	files := pass.ResultOf[filesetup.Analyzer].(*filesetup.Result)
	ec := &report.ErrorCollector{}

	nodeFilter := []ast.Node{(*ast.FuncDecl)(nil), (*ast.FuncLit)(nil)}
	insp.Preorder(nodeFilter, func(n ast.Node) {
		var body *ast.BlockStmt
		switch fn := n.(type) {
		case *ast.FuncDecl:
			body = fn.Body
		case *ast.FuncLit:
			body = fn.Body
		}
		if body == nil {
			return
		}
		if files.IsGenerated(pass.Fset.File(body.Pos())) {
			return
		}
		newChecker(ec, pass.TypesInfo).analyzeBody(body)
	})

	return ec.Diagnostics(pass, files.IgnoreFunc()), nil
}
