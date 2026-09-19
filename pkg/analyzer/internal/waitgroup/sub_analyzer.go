package waitgroup

import (
	"go/ast"
	"go/token"
	"reflect"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/driver"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/filesetup"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/primitives"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
)

// SubAnalyzer drives the WaitGroup misuse checks as an independent
// analysis.Analyzer. It returns its diagnostic slice as Result; the
// umbrella analyzer re-emits them via pass.Report.
var SubAnalyzer = &analysis.Analyzer{
	Name:       "goconcurrencylint_waitgroup",
	Doc:        "Detects misuse of sync.WaitGroup (Add/Done/Wait imbalance, Add after Wait, etc.).",
	Run:        run,
	Requires:   []*analysis.Analyzer{inspect.Analyzer, primitives.Analyzer, filesetup.Analyzer},
	ResultType: reflect.TypeFor[[]analysis.Diagnostic](),
}

func run(pass *analysis.Pass) (any, error) {
	// Package-wide indexes are built on first use and shared by every function
	// in the pass. driver.Run visits functions sequentially, so the lazy init
	// needs no synchronization.
	var counters *packageCounterIndex
	var functionDecls map[token.Pos]*ast.FuncDecl
	return driver.Run(pass, driver.Config[*Checker]{
		Guard: primitives.HasWaitGroups,
		NewChecker: func(fr *primitives.FunctionResult, ec report.Reporter, cf *commentfilter.CommentFilter, pass *analysis.Pass) *Checker {
			if counters == nil {
				counters = newPackageCounterIndex(pass.Files)
			}
			if functionDecls == nil {
				functionDecls = buildFunctionDeclMap(pass.Files)
			}
			checker := newCheckerWithFunctionDecls(fr, ec, cf, pass, functionDecls)
			checker.packageCounters = counters
			return checker
		},
	})
}
