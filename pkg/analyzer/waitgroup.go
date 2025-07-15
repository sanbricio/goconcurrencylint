package analyzer

import (
	"go/ast"

	commnetfilter "github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/waitgroup"
)

type WaitGroupAnalyzer struct {
	analyzer *waitgroup.Analyzer
}

// NewWaitGroupAnalyzer creates a new WaitGroup analyzer
func NewWaitGroupAnalyzer(waitGroupNames map[string]bool, errorCollector *report.ErrorCollector, cf *commnetfilter.CommentFilter) *WaitGroupAnalyzer {
	return &WaitGroupAnalyzer{
		analyzer: waitgroup.NewAnalyzer(waitGroupNames, errorCollector, cf),
	}
}

// AnalyzeFunction analyzes WaitGroup usage in a function
func (wga *WaitGroupAnalyzer) AnalyzeFunction(fn *ast.FuncDecl) {
	wga.analyzer.AnalyzeFunction(fn)
}
