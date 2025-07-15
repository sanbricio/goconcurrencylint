package waitgroup

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
	commnetfilter "github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/report"
)

// Analyzer handles the analysis of WaitGroup usage
type Analyzer struct {
	waitGroupNames map[string]bool
	errorCollector *report.ErrorCollector
	function       *ast.FuncDecl
	commentFilter  *commnetfilter.CommentFilter
}

// addCall represents an Add() call with its position and value
type addCall struct {
	pos   token.Pos
	value int
}

// Stats tracks the state of a WaitGroup within a function
type Stats struct {
	addCalls     []addCall
	doneCalls    []token.Pos
	waitCalls    []token.Pos
	doneCount    int
	hasDeferDone bool
	totalAdd     int
}

// NewAnalyzer creates a new WaitGroup analyzer
func NewAnalyzer(waitGroupNames map[string]bool, errorCollector *report.ErrorCollector, cf *commnetfilter.CommentFilter) *Analyzer {
	return &Analyzer{
		waitGroupNames: waitGroupNames,
		errorCollector: errorCollector,
		commentFilter:  cf,
	}
}

// AnalyzeFunction analyzes WaitGroup usage in a function
func (wga *Analyzer) AnalyzeFunction(fn *ast.FuncDecl) {
	wga.function = fn
	stats := wga.collectStats()
	wga.validateUsage(stats)
}

// collectStats collects statistics for all WaitGroups in the function
func (wga *Analyzer) collectStats() map[string]*Stats {
	stats := wga.initializeStats()
	wga.findDeferDoneCalls(stats)
	wga.collectCalls(stats)
	return stats
}

// initializeStats creates initial stats for all known WaitGroups
func (wga *Analyzer) initializeStats() map[string]*Stats {
	stats := make(map[string]*Stats)
	for wgName := range wga.waitGroupNames {
		stats[wgName] = &Stats{
			addCalls:  []addCall{},
			doneCalls: []token.Pos{},
			waitCalls: []token.Pos{},
		}
	}
	return stats
}

// handleAddCall processes Add() calls
func (wga *Analyzer) handleAddCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if wga.commentFilter.ShouldSkipCall(call) {
		return
	}

	addValue := common.GetAddValue(call)
	stats[wgName].addCalls = append(stats[wgName].addCalls, addCall{
		pos:   call.Pos(),
		value: addValue,
	})
	stats[wgName].totalAdd += addValue
}

// handleDoneCall processes Done() calls
func (wga *Analyzer) handleDoneCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if wga.commentFilter.ShouldSkipCall(call) {
		return
	}

	stats[wgName].doneCount++
	stats[wgName].doneCalls = append(stats[wgName].doneCalls, call.Pos())
}

// handleWaitCall processes Wait() calls
func (wga *Analyzer) handleWaitCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if wga.commentFilter.ShouldSkipCall(call) {
		return
	}

	stats[wgName].waitCalls = append(stats[wgName].waitCalls, call.Pos())
}

// isWaitGroupArgument checks if an argument represents a WaitGroup being passed
func (wga *Analyzer) isWaitGroupArgument(arg ast.Expr, wgName string) bool {
	if unary, ok := arg.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		if ident, ok := unary.X.(*ast.Ident); ok && ident.Name == wgName {
			return true
		}
	}

	if ident, ok := arg.(*ast.Ident); ok && ident.Name == wgName {
		return true
	}

	if sel, ok := arg.(*ast.SelectorExpr); ok {
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == wgName {
			methodName := sel.Sel.Name
			if methodName == "Done" || methodName == "Add" || methodName == "Wait" {
				return true
			}
		}
	}

	if call, ok := arg.(*ast.CallExpr); ok {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == wgName {
				return true
			}
		}
	}

	return false
}

// isWaitGroupPassedToOtherFunctions checks if a WaitGroup is passed to other functions
func (wga *Analyzer) isWaitGroupPassedToOtherFunctions(wgName string) bool {
	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			for _, arg := range call.Args {
				if wga.isWaitGroupArgument(arg, wgName) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}
