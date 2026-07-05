package cond

import (
	"reflect"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/callscan"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/filesetup"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
)

// SubAnalyzer drives the sync.Cond misuse checks as an independent
// analysis.Analyzer. Like the other sub-analyzers it returns its diagnostics
// as Result so the umbrella Analyzer re-emits them (and analysistest observes
// them through the umbrella).
//
// The only check left is NewCond(nil), which is keyed off rare NewCond call
// sites, so this sub-analyzer delegates to callscan.Run instead of the
// per-function driver skeleton: it walks call expressions directly and
// cheap-rejects on the callee name before touching type information, keeping
// it near-free on large packages.
var SubAnalyzer = &analysis.Analyzer{
	Name:       "goconcurrencylint_cond",
	Doc:        "Detects misuse of sync.Cond: NewCond(nil).",
	Run:        run,
	Requires:   []*analysis.Analyzer{inspect.Analyzer, filesetup.Analyzer},
	ResultType: reflect.TypeFor[[]analysis.Diagnostic](),
}

func run(pass *analysis.Pass) (any, error) {
	return callscan.Run(pass, callscan.Config[*Checker]{
		SelectorOf: callscan.PlainSelector,
		Accept:     func(name string) bool { return name == "NewCond" },
		NewChecker: func(ec report.Reporter, pass *analysis.Pass) *Checker {
			return NewChecker(ec, pass.TypesInfo)
		},
	}), nil
}
