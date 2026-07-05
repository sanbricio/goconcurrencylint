package once

import (
	"reflect"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/callscan"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/driver"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/filesetup"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/primitives"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
)

// SubAnalyzer drives the sync.Once misuse checks as an independent
// analysis.Analyzer. It does not call pass.Report itself: it returns the
// prepared diagnostic slice as Result so the umbrella Analyzer can re-emit
// them (and so analysistest can observe them through the umbrella).
var SubAnalyzer = &analysis.Analyzer{
	Name:       "goconcurrencylint_once",
	Doc:        "Detects misuse of sync.Once: re-entrant Do that deadlocks, Do(nil), and OnceFunc/OnceValue/OnceValues(nil) that panic.",
	Run:        run,
	Requires:   []*analysis.Analyzer{inspect.Analyzer, primitives.Analyzer, filesetup.Analyzer},
	ResultType: reflect.TypeFor[[]analysis.Diagnostic](),
}

func run(pass *analysis.Pass) (any, error) {
	// scope is built once on first use and shared across every function in
	// the pass (driver.Run visits functions sequentially, so the lazy init
	// needs no synchronization).
	var scope *packageScope
	result, err := driver.Run(pass, driver.Config[*Checker]{
		Guard: primitives.HasOnces,
		NewChecker: func(_ *primitives.FunctionResult, ec report.Reporter, _ *commentfilter.CommentFilter, pass *analysis.Pass) *Checker {
			if scope == nil {
				scope = newPackageScope(pass.Files)
			}
			pkg := pass.ResultOf[primitives.Analyzer].(*primitives.Result)
			return NewChecker(ec, pkg, pass.TypesInfo, scope)
		},
	})
	if err != nil {
		return nil, err
	}

	// The OnceFunc/OnceValue/OnceValues(nil) checks key off package-level sync
	// constructors, not a sync.Once variable, so they run outside the
	// per-function HasOnces guard above, via the shared call-scan skeleton.
	diags := result.([]analysis.Diagnostic)
	constructorDiags := callscan.Run(pass, callscan.Config[*ConstructorChecker]{
		SelectorOf: ConstructorSelector,
		Accept:     IsOnceConstructorName,
		NewChecker: func(ec report.Reporter, pass *analysis.Pass) *ConstructorChecker {
			return NewConstructorChecker(ec, pass.TypesInfo)
		},
	})
	return append(diags, constructorDiags...), nil
}
