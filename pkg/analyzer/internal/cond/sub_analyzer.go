package cond

import (
	"reflect"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/driver"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/filesetup"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/primitives"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
)

// SubAnalyzer drives the sync.Cond misuse checks as an independent
// analysis.Analyzer. Like the other sub-analyzers it returns its diagnostics
// as Result so the umbrella Analyzer re-emits them (and analysistest observes
// them through the umbrella).
//
// Unlike mutex/waitgroup/once, sync.Cond values are almost always created with
// `c := sync.NewCond(...)` rather than a `var` declaration, so there is no
// package-level Cond index to guard on. The Guard is therefore always true and
// the per-call type check (IsCond) is what keeps the checks precise.
var SubAnalyzer = &analysis.Analyzer{
	Name:       "goconcurrencylint_cond",
	Doc:        "Detects misuse of sync.Cond: Wait() outside a loop and NewCond(nil).",
	Run:        run,
	Requires:   []*analysis.Analyzer{inspect.Analyzer, primitives.Analyzer, filesetup.Analyzer},
	ResultType: reflect.TypeFor[[]analysis.Diagnostic](),
}

func run(pass *analysis.Pass) (any, error) {
	return driver.Run(pass, driver.Config[*Checker]{
		Guard: func(*primitives.FunctionResult) bool { return true },
		NewChecker: func(_ *primitives.FunctionResult, ec report.Reporter, _ *commentfilter.CommentFilter, pass *analysis.Pass) *Checker {
			return NewChecker(ec, pass.TypesInfo)
		},
	})
}
