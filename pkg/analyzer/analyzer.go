package analyzer

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
	commnetfilter "github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/mutex"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/waitgroup"
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
		switch node := n.(type) {
		case *ast.ValueSpec:
			// Handle var declarations
			for _, name := range node.Names {
				typ := getVariableType(node, pass)
				if typ == nil {
					continue
				}
				classifyAndAddPrimitive(name.Name, typ, primitives)
			}

		case *ast.AssignStmt:
			// Handle short variable declarations
			if node.Tok == token.DEFINE {
				for i, lhs := range node.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && i < len(node.Rhs) {
						if typ := pass.TypesInfo.TypeOf(node.Rhs[i]); typ != nil {
							classifyAndAddPrimitive(ident.Name, typ, primitives)
						}
					}
				}
			}
			// Handle regular assignments
			if node.Tok == token.ASSIGN {
				for i, lhs := range node.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && i < len(node.Rhs) {
						if typ := pass.TypesInfo.TypeOf(node.Rhs[i]); typ != nil {
							classifyAndAddPrimitive(ident.Name, typ, primitives)
						}
					}
				}
			}
		}
		return true
	})

	return primitives
}

// classifyAndAddPrimitive classifies a type and adds it to the appropriate primitive map
func classifyAndAddPrimitive(varName string, typ types.Type, primitives *syncPrimitive) {
	switch {
	case common.IsMutex(typ):
		primitives.mutexes[varName] = true
	case common.IsRWMutex(typ):
		primitives.rwMutexes[varName] = true
	case common.IsWaitGroup(typ):
		primitives.waitGroups[varName] = true
	}
}

// getVariableType extracts the type information for a variable specification
func getVariableType(vs *ast.ValueSpec, pass *analysis.Pass) types.Type {
	// First try to get type from explicit type annotation
	if vs.Type != nil {
		typ := pass.TypesInfo.TypeOf(vs.Type)
		if typ != nil {
			return typ
		}
	}

	// If no explicit type, try to infer from the first value
	if len(vs.Values) > 0 {
		typ := pass.TypesInfo.TypeOf(vs.Values[0])
		if typ != nil {
			return typ
		}
	}

	// Last resort: try to get type info from the first name
	if len(vs.Names) > 0 {
		if obj := pass.TypesInfo.ObjectOf(vs.Names[0]); obj != nil {
			return obj.Type()
		}
	}

	return nil
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
	analyzer := mutex.NewAnalyzer(primitives.mutexes, primitives.rwMutexes, errorCollector, cf)
	analyzer.AnalyzeFunction(fn)
}

// analyzeWaitGroupUsage handles waitgroup analysis
func analyzeWaitGroupUsage(fn *ast.FuncDecl, primitives *syncPrimitive, errorCollector *report.ErrorCollector, cf *commnetfilter.CommentFilter) {
	analyzer := waitgroup.NewAnalyzer(primitives.waitGroups, errorCollector, cf)
	analyzer.AnalyzeFunction(fn)
}
