// Package primitives discovers sync primitive declarations in a package
// (package scope) and exposes a helper to compute the set of primitives
// reachable from a single function (locals + params + struct field access
// + package scope).
//
// It is a foundation analyzer in the goconcurrencylint dependency graph:
// the mutex and waitgroup sub-analyzers declare it in their Requires and
// consume its Result to avoid duplicating the package-scope scan.
package primitives

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"reflect"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"golang.org/x/tools/go/analysis"
)

// Result lists sync primitive variable names declared at package scope.
// Map values are always true; the map shape is preserved from the
// pre-refactor implementation for compatibility with downstream callers
// that use len() and key lookup.
type Result struct {
	Mutexes    map[string]bool
	RWMutexes  map[string]bool
	WaitGroups map[string]bool
}

// FunctionResult lists sync primitive names visible inside a function:
// package-scope ∪ locals ∪ parameters ∪ struct field accesses observed
// in the body. LocalWaitGroups holds the function-local subset of
// WaitGroups (before merging the package scope) and PackageWaitGroups
// holds the package-level subset; the waitgroup checker needs both to
// decide whether Wait must be present.
type FunctionResult struct {
	Mutexes           map[string]bool
	RWMutexes         map[string]bool
	WaitGroups        map[string]bool
	LocalWaitGroups   map[string]bool
	PackageWaitGroups map[string]bool
}

// Analyzer computes the package-scope primitives once per package.
// Consumers declare it in their Requires and retrieve the *Result via
// pass.ResultOf[primitives.Analyzer].
var Analyzer = &analysis.Analyzer{
	Name:       "goconcurrencylint_primitives",
	Doc:        "Collects sync.Mutex/RWMutex/WaitGroup variable names declared at package scope.",
	Run:        run,
	ResultType: reflect.TypeFor[*Result](),
}

func run(pass *analysis.Pass) (any, error) {
	res := &Result{
		Mutexes:    map[string]bool{},
		RWMutexes:  map[string]bool{},
		WaitGroups: map[string]bool{},
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
		classify(name, varObj.Type(), res.Mutexes, res.RWMutexes, res.WaitGroups)
	}

	return res, nil
}

// ForFunction returns the primitives visible inside fn, merging the
// per-function scan with the supplied package-scope Result. The returned
// LocalWaitGroups field captures the function-local waitgroups *before*
// the merge so callers can tell locals apart from package-level vars.
func ForFunction(fn *ast.FuncDecl, pass *analysis.Pass, pkg *Result) *FunctionResult {
	fr := &FunctionResult{
		Mutexes:    map[string]bool{},
		RWMutexes:  map[string]bool{},
		WaitGroups: map[string]bool{},
	}

	// Function parameters: include mutex/rwmutex but intentionally not
	// waitgroups (Done-only worker functions would produce false positives).
	if fn.Type != nil && fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			typ := pass.TypesInfo.TypeOf(field.Type)
			if typ == nil {
				continue
			}
			for _, name := range field.Names {
				switch {
				case common.IsMutex(typ):
					fr.Mutexes[name.Name] = true
				case common.IsRWMutex(typ):
					fr.RWMutexes[name.Name] = true
				}
			}
		}
	}

	scanBody(fn.Body, pass, fr)

	// Snapshot locals before merging package-scope.
	localWG := make(map[string]bool, len(fr.WaitGroups))
	maps.Copy(localWG, fr.WaitGroups)

	maps.Copy(fr.Mutexes, pkg.Mutexes)
	maps.Copy(fr.RWMutexes, pkg.RWMutexes)
	maps.Copy(fr.WaitGroups, pkg.WaitGroups)
	fr.LocalWaitGroups = localWG
	fr.PackageWaitGroups = pkg.WaitGroups

	return fr
}

// HasMutexes reports whether any mutex or rwmutex name is in scope.
func HasMutexes(fr *FunctionResult) bool {
	return len(fr.Mutexes) > 0 || len(fr.RWMutexes) > 0
}

// HasWaitGroups reports whether any waitgroup name is in scope.
func HasWaitGroups(fr *FunctionResult) bool {
	return len(fr.WaitGroups) > 0
}

func scanBody(body *ast.BlockStmt, pass *analysis.Pass, fr *FunctionResult) {
	if body == nil {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ValueSpec:
			for _, name := range node.Names {
				typ := variableType(node, pass)
				if typ == nil {
					continue
				}
				classify(name.Name, typ, fr.Mutexes, fr.RWMutexes, fr.WaitGroups)
			}

		case *ast.AssignStmt:
			if node.Tok != token.DEFINE && node.Tok != token.ASSIGN {
				break
			}
			for i, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || i >= len(node.Rhs) {
					continue
				}
				if typ := pass.TypesInfo.TypeOf(node.Rhs[i]); typ != nil {
					classify(ident.Name, typ, fr.Mutexes, fr.RWMutexes, fr.WaitGroups)
				}
			}

		case *ast.SelectorExpr:
			if selection, ok := pass.TypesInfo.Selections[node]; ok && selection.Kind() == types.FieldVal {
				fieldType := selection.Type()
				parentName := common.GetVarName(node.X)
				if parentName != "?" {
					compoundName := parentName + "." + node.Sel.Name
					classify(compoundName, fieldType, fr.Mutexes, fr.RWMutexes, fr.WaitGroups)
				}
			}
		}
		return true
	})
}

// variableType extracts the type information for a variable specification.
func variableType(vs *ast.ValueSpec, pass *analysis.Pass) types.Type {
	if vs.Type != nil {
		if typ := pass.TypesInfo.TypeOf(vs.Type); typ != nil {
			return typ
		}
	}
	if len(vs.Values) > 0 {
		if typ := pass.TypesInfo.TypeOf(vs.Values[0]); typ != nil {
			return typ
		}
	}
	if len(vs.Names) > 0 {
		if obj := pass.TypesInfo.ObjectOf(vs.Names[0]); obj != nil {
			return obj.Type()
		}
	}
	return nil
}

// classify routes name into the matching primitive map. The caller supplies
// the three target maps so the helper can serve both *Result (package
// scope) and *FunctionResult (per-function) without duplication.
func classify(name string, typ types.Type, mu, rw, wg map[string]bool) {
	switch {
	case common.IsMutex(typ):
		mu[name] = true
	case common.IsRWMutex(typ):
		rw[name] = true
	case common.IsWaitGroup(typ):
		wg[name] = true
	}
}
