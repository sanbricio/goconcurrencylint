package mutex

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

// SubAnalyzer drives the mutex / rwmutex misuse checks as an independent
// analysis.Analyzer. It does not call pass.Report itself: it returns the
// prepared diagnostic slice as Result so the umbrella Analyzer can
// re-emit them (and so analysistest can observe them through the
// umbrella).
var SubAnalyzer = &analysis.Analyzer{
	Name:       "goconcurrencylint_mutex",
	Doc:        "Detects misuse of sync.Mutex and sync.RWMutex.",
	Run:        run,
	Requires:   []*analysis.Analyzer{inspect.Analyzer, primitives.Analyzer, filesetup.Analyzer},
	ResultType: reflect.TypeFor[[]analysis.Diagnostic](),
}

func run(pass *analysis.Pass) (any, error) {
	return driver.Run(pass, driver.Config[*Checker]{
		Guard: primitives.HasMutexes,
		NewChecker: func(fr *primitives.FunctionResult, ec report.Reporter, cf *commentfilter.CommentFilter, pass *analysis.Pass) *Checker {
			return NewChecker(fr, ec, cf, pass.TypesInfo, pass.Files)
		},
	})
}
