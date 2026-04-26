package analyzer

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
	commnetfilter "github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/mutex"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/waitgroup"
	"golang.org/x/tools/go/analysis"
)

// Analyzer is the main entry point for the goconcurrencylint linter.
// It detects common mistakes in the use of sync.Mutex and sync.WaitGroup:
// - Locks without unlocks
// - Add without Done
var Analyzer = &analysis.Analyzer{
	Name:     "goconcurrencylint",
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
	pkgPrimitives := collectPackageLevelPrimitives(pass)

	detectCopyByValue(pass, errorCollector)
	for _, file := range pass.Files {
		analyzeFunctions(file, pass, errorCollector, pkgPrimitives)
	}

	errorCollector.ReportAll(pass)
	return nil, nil
}

// analyzeFunctions processes all function declarations in a file
func analyzeFunctions(file *ast.File, pass *analysis.Pass, errorCollector *report.ErrorCollector, pkgPrimitives *syncPrimitive) {
	commentFilter := commnetfilter.NewCommentFilter(pass.Fset, file)

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		primitives := findSyncPrimitives(fn, pass)
		localWaitGroups := copyPrimitiveNames(primitives.waitGroups)
		mergePrimitives(primitives, pkgPrimitives)

		if hasMutexes(primitives) {
			analyzeMutexUsage(fn, primitives, errorCollector, commentFilter, pass)
		}

		if hasWaitGroups(primitives) {
			analyzeWaitGroupUsage(fn, primitives, localWaitGroups, pkgPrimitives.waitGroups, errorCollector, commentFilter, pass)
		}
	}
}

// collectPackageLevelPrimitives scans package-level declarations for sync primitives,
// including declarations that live in other files from the same package.
func collectPackageLevelPrimitives(pass *analysis.Pass) *syncPrimitive {
	primitives := &syncPrimitive{
		mutexes:    make(map[string]bool),
		rwMutexes:  make(map[string]bool),
		waitGroups: make(map[string]bool),
	}

	scope := pass.Pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		varObj, ok := obj.(*types.Var)
		if !ok || varObj.IsField() {
			continue
		}

		if varObj.Pkg() != pass.Pkg {
			continue
		}

		classifyAndAddPrimitive(name, varObj.Type(), primitives)
	}

	return primitives
}

// mergePrimitives merges src primitives into dst
func mergePrimitives(dst, src *syncPrimitive) {
	for k, v := range src.mutexes {
		dst.mutexes[k] = v
	}
	for k, v := range src.rwMutexes {
		dst.rwMutexes[k] = v
	}
	for k, v := range src.waitGroups {
		dst.waitGroups[k] = v
	}
}

func copyPrimitiveNames(src map[string]bool) map[string]bool {
	dst := make(map[string]bool, len(src))
	maps.Copy(dst, src)
	return dst
}

// findSyncPrimitives identifies all sync primitives declared in a function,
// including local variables, function parameters, and struct field accesses.
func findSyncPrimitives(fn *ast.FuncDecl, pass *analysis.Pass) *syncPrimitive {
	primitives := &syncPrimitive{
		mutexes:    make(map[string]bool),
		rwMutexes:  make(map[string]bool),
		waitGroups: make(map[string]bool),
	}

	// Check function parameters for mutex primitives (e.g., func f(mu *sync.Mutex)).
	// WaitGroup parameters are intentionally excluded: Done-only worker functions
	// (e.g., func worker(wg *sync.WaitGroup) { defer wg.Done() }) would produce false positives.
	if fn.Type != nil && fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			typ := pass.TypesInfo.TypeOf(field.Type)
			if typ == nil {
				continue
			}
			for _, name := range field.Names {
				switch {
				case common.IsMutex(typ):
					primitives.mutexes[name.Name] = true
				case common.IsRWMutex(typ):
					primitives.rwMutexes[name.Name] = true
				}
			}
		}
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

		case *ast.SelectorExpr:
			// Handle struct field access (e.g., s.mu where mu is sync.Mutex)
			if selection, ok := pass.TypesInfo.Selections[node]; ok && selection.Kind() == types.FieldVal {
				fieldType := selection.Type()
				parentName := common.GetVarName(node.X)
				if parentName != "?" {
					compoundName := parentName + "." + node.Sel.Name
					classifyAndAddPrimitive(compoundName, fieldType, primitives)
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
func analyzeMutexUsage(fn *ast.FuncDecl, primitives *syncPrimitive, errorCollector *report.ErrorCollector, cf *commnetfilter.CommentFilter, pass *analysis.Pass) {
	analyzer := mutex.NewAnalyzer(primitives.mutexes, primitives.rwMutexes, errorCollector, cf, pass.TypesInfo, pass.Files)
	analyzer.AnalyzeFunction(fn)
}

// analyzeWaitGroupUsage handles waitgroup analysis
func analyzeWaitGroupUsage(fn *ast.FuncDecl, primitives *syncPrimitive, localWaitGroups, packageLevelWaitGroups map[string]bool, errorCollector *report.ErrorCollector, cf *commnetfilter.CommentFilter, pass *analysis.Pass) {
	analyzer := waitgroup.NewAnalyzer(primitives.waitGroups, localWaitGroups, packageLevelWaitGroups, errorCollector, cf, pass)
	analyzer.AnalyzeFunction(fn)
}

func detectCopyByValue(pass *analysis.Pass, errorCollector *report.ErrorCollector) {
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				reportCopyByValueParams(node, pass, errorCollector)
			case *ast.FuncLit:
				reportCopyByValueFuncLitParams(node, pass, errorCollector)
			case *ast.ValueSpec:
				reportCopyByValueValueSpec(node, pass, errorCollector)
			case *ast.AssignStmt:
				reportCopyByValueAssignments(node, pass, errorCollector)
			case *ast.CallExpr:
				reportCopyByValueArgs(node, pass, errorCollector)
			}
			return true
		})
	}
}

func reportCopyByValueFuncLitParams(fn *ast.FuncLit, pass *analysis.Pass, errorCollector *report.ErrorCollector) {
	if fn == nil || fn.Type == nil || fn.Type.Params == nil {
		return
	}

	reportCopyByValueFieldList(fn.Type.Params, pass, errorCollector)
}

func reportCopyByValueParams(fn *ast.FuncDecl, pass *analysis.Pass, errorCollector *report.ErrorCollector) {
	if fn == nil || fn.Type == nil || fn.Type.Params == nil {
		return
	}

	reportCopyByValueFieldList(fn.Type.Params, pass, errorCollector)
}

func reportCopyByValueFieldList(fields *ast.FieldList, pass *analysis.Pass, errorCollector *report.ErrorCollector) {
	for _, field := range fields.List {
		kind := syncPrimitiveValueKind(pass.TypesInfo.TypeOf(field.Type))
		if kind == "" {
			continue
		}
		for _, name := range field.Names {
			errorCollector.AddError(name.Pos(), copyByValueMessage(kind, name.Name))
		}
	}
}

func reportCopyByValueValueSpec(vs *ast.ValueSpec, pass *analysis.Pass, errorCollector *report.ErrorCollector) {
	if vs == nil {
		return
	}

	for _, value := range vs.Values {
		if kind, name, ok := copiedSyncPrimitive(value, pass); ok {
			errorCollector.AddError(value.Pos(), copyByValueMessage(kind, name))
		}
	}
}

func reportCopyByValueAssignments(assign *ast.AssignStmt, pass *analysis.Pass, errorCollector *report.ErrorCollector) {
	if assign == nil || (assign.Tok != token.ASSIGN && assign.Tok != token.DEFINE) {
		return
	}

	for _, rhs := range assign.Rhs {
		if kind, name, ok := copiedSyncPrimitive(rhs, pass); ok {
			errorCollector.AddError(rhs.Pos(), copyByValueMessage(kind, name))
		}
	}
}

func reportCopyByValueArgs(call *ast.CallExpr, pass *analysis.Pass, errorCollector *report.ErrorCollector) {
	if call == nil {
		return
	}

	for _, arg := range call.Args {
		if kind, name, ok := copiedSyncPrimitive(arg, pass); ok {
			errorCollector.AddError(arg.Pos(), copyByValueMessage(kind, name))
		}
	}
}

func copiedSyncPrimitive(expr ast.Expr, pass *analysis.Pass) (string, string, bool) {
	if expr == nil || isFreshSyncPrimitiveValue(expr) {
		return "", "", false
	}

	kind := syncPrimitiveValueKind(pass.TypesInfo.TypeOf(expr))
	if kind == "" {
		return "", "", false
	}

	name := common.GetVarName(expr)
	if name == "" || name == "?" {
		return "", "", false
	}
	return kind, name, true
}

func isFreshSyncPrimitiveValue(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return true
	case *ast.UnaryExpr:
		return e.Op == token.AND || isFreshSyncPrimitiveValue(e.X)
	}
	return false
}

func syncPrimitiveValueKind(typ types.Type) string {
	if typ == nil {
		return ""
	}
	named, ok := types.Unalias(typ).(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "sync" {
		return ""
	}

	switch named.Obj().Name() {
	case "Mutex":
		return "mutex"
	case "RWMutex":
		return "rwmutex"
	case "WaitGroup":
		return "waitgroup"
	default:
		return ""
	}
}

func copyByValueMessage(kind, name string) string {
	return kind + " '" + name + "' is copied by value"
}
