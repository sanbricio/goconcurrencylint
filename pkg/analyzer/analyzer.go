package analyzer

import (
	"go/ast"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
	commnetfilter "github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/report"
	"golang.org/x/tools/go/analysis"
)

// Analyzer is the main entry point for the goconcurrentlint linter.
// It detects common mistakes in the use of sync.Mutex and sync.WaitGroup:
// - Locks without unlocks
// - Add without Done
var Analyzer = &analysis.Analyzer{
	Name:     "goconcurrentlint",
	Doc:      "Detects common mistakes in the use of sync.Mutex and sync.WaitGroup: locks without unlock and Add without Done.",
	Run:      run,
	Requires: []*analysis.Analyzer{},
}

// syncPrimitive holds information about sync primitives found in a function
type syncPrimitive struct {
	mutexes    map[string]bool
	rwMutexes  map[string]bool
	waitGroups map[string]bool
}

func run(pass *analysis.Pass) (interface{}, error) {
	errorCollector := &report.ErrorCollector{}

	for _, file := range pass.Files {
		analyzeFunctions(file, pass, errorCollector)
	}

	errorCollector.ReportAll(pass)
	return nil, nil
}

// analyzeFunctions processes all function declarations in a file
func analyzeFunctions(file *ast.File, pass *analysis.Pass, errorCollector *report.ErrorCollector) {

	commentFilter := commnetfilter.NewCommentFilter(pass.Fset, file)

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		primitives := findSyncPrimitives(fn, pass)

		if hasMutexes(primitives) {
			analyzeMutexUsage(fn, primitives, errorCollector, commentFilter)
		}

		if hasWaitGroups(primitives) {
			analyzeWaitGroupUsage(fn, primitives, errorCollector, commentFilter)
		}
	}
}

// findSyncPrimitives identifies all sync primitives declared in a function
func findSyncPrimitives(fn *ast.FuncDecl, pass *analysis.Pass) *syncPrimitive {
	primitives := &syncPrimitive{
		mutexes:    make(map[string]bool),
		rwMutexes:  make(map[string]bool),
		waitGroups: make(map[string]bool),
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}

		for _, name := range vs.Names {
			typ := getVariableType(vs, pass)
			if typ == nil {
				continue
			}

			varName := name.Name
			switch {
			case common.IsMutex(typ):
				primitives.mutexes[varName] = true
			case common.IsRWMutex(typ):
				primitives.rwMutexes[varName] = true
			case common.IsWaitGroup(typ):
				primitives.waitGroups[varName] = true
			}
		}
		return true
	})

	return primitives
}

// getVariableType extracts the type information for a variable specification
func getVariableType(vs *ast.ValueSpec, pass *analysis.Pass) types.Type {
	typ := pass.TypesInfo.TypeOf(vs.Type)
	if typ == nil && len(vs.Values) > 0 {
		typ = pass.TypesInfo.TypeOf(vs.Values[0])
	}
	return typ
}

// hasMutexes checks if any mutex or rwmutex primitives were found
func hasMutexes(primitives *syncPrimitive) bool {
	return len(primitives.mutexes) > 0 || len(primitives.rwMutexes) > 0
}

// hasWaitGroups checks if any waitgroup primitives were found
func hasWaitGroups(primitives *syncPrimitive) bool {
	return len(primitives.waitGroups) > 0
}

// analyzeMutexUsage handles mutex and rwmutex analysis
func analyzeMutexUsage(fn *ast.FuncDecl, primitives *syncPrimitive, errorCollector *report.ErrorCollector, cf *commnetfilter.CommentFilter) {
	analyzer := NewMutexAnalyzer(primitives.mutexes, primitives.rwMutexes, errorCollector, cf)
	analyzer.AnalyzeFunction(fn)
}

// analyzeWaitGroupUsage handles waitgroup analysis
func analyzeWaitGroupUsage(fn *ast.FuncDecl, primitives *syncPrimitive, errorCollector *report.ErrorCollector, cf *commnetfilter.CommentFilter) {
	analyzer := NewWaitGroupAnalyzer(primitives.waitGroups, errorCollector, cf)
	analyzer.AnalyzeFunction(fn)
}
