// Package driver provides the shared skeleton for sub-analyzer run functions.
//
// Both the mutex and waitgroup sub-analyzers follow the same per-function
// visitation pattern: Preorder over *ast.FuncDecl, skip synthetic/generated
// bodies, build a FunctionResult via primitives.ForFunction, apply a guard to
// decide whether the function is relevant, construct a checker, call
// AnalyzeFunction, and finally return the collected diagnostics. This package
// captures that skeleton so each sub-analyzer can reduce its run function to a
// single call.
package driver

import (
	"go/ast"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/filesetup"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/primitives"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// FunctionChecker is the minimal interface that every per-function checker
// must satisfy. AnalyzeFunction is called once per relevant function
// declaration after the checker has been constructed by Config.NewChecker.
type FunctionChecker interface {
	AnalyzeFunction(fn *ast.FuncDecl)
}

// Config parameterizes the shared run skeleton for a single sub-analyzer.
//
// Guard decides whether a function contains the primitives that the
// sub-analyzer cares about (e.g. HasMutexes or HasWaitGroups). NewChecker
// constructs a fresh checker for the function; the driver supplies the
// ErrorCollector and CommentFilter, and forwards the *analysis.Pass so each
// sub-analyzer can extract the fields it needs (TypesInfo, Files, etc.).
type Config[C FunctionChecker] struct {
	// Guard reports whether the function result contains relevant primitives.
	// The visitation skips functions for which Guard returns false.
	Guard func(fr *primitives.FunctionResult) bool

	// NewChecker constructs a checker for the current function. ec is the
	// shared ErrorCollector for the entire pass; cf is the per-file comment
	// filter for the function's source file.
	NewChecker func(fr *primitives.FunctionResult, ec report.Reporter, cf *commentfilter.CommentFilter, pass *analysis.Pass) C
}

// Run executes the shared per-function visitation described by cfg and returns
// the collected diagnostics. It is intended to be used directly as the body of
// a sub-analyzer's run function:
//
//	func run(pass *analysis.Pass) (any, error) {
//	    return driver.Run(pass, driver.Config[*Checker]{...})
//	}
func Run[C FunctionChecker](pass *analysis.Pass, cfg Config[C]) (any, error) {
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
		if !cfg.Guard(fr) {
			return
		}

		cf := files.FilterFor(tokFile)
		c := cfg.NewChecker(fr, ec, cf, pass)
		c.AnalyzeFunction(fn)
	})

	return ec.Diagnostics(pass, files.IgnoreFunc()), nil
}
