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
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/filesetup"
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
	Onces      map[string]bool

	// functionResults is built eagerly by Analyzer so every dependent
	// sub-analyzer shares one body scan per function. The Requires graph may
	// run consumers concurrently, so entries are immutable after run returns.
	functionResults map[*ast.FuncDecl]*FunctionResult
	typeKinds       map[types.Type]primitiveKind
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
	Onces             map[string]bool
	LocalWaitGroups   map[string]bool
	PackageWaitGroups map[string]bool
}

// Analyzer computes the package-scope primitives once per package.
// Consumers declare it in their Requires and retrieve the *Result via
// pass.ResultOf[primitives.Analyzer].
var Analyzer = &analysis.Analyzer{
	Name:       "goconcurrencylint_primitives",
	Doc:        "Collects sync.Mutex/RWMutex/WaitGroup/Once variable names declared at package scope.",
	Run:        run,
	Requires:   []*analysis.Analyzer{filesetup.Analyzer},
	ResultType: reflect.TypeFor[*Result](),
}

func run(pass *analysis.Pass) (any, error) {
	res := &Result{
		Mutexes:         map[string]bool{},
		RWMutexes:       map[string]bool{},
		WaitGroups:      map[string]bool{},
		Onces:           map[string]bool{},
		functionResults: make(map[*ast.FuncDecl]*FunctionResult),
		typeKinds:       make(map[types.Type]primitiveKind),
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
		classify(name, varObj.Type(), res.maps(), res.typeKinds)
	}

	// Build immutable per-function results once. Mutex, WaitGroup and Once
	// analyzers all consume these results; rescanning here avoids three full AST
	// walks of every function body.
	files := pass.ResultOf[filesetup.Analyzer].(*filesetup.Result)
	for _, file := range pass.Files {
		if files.IsGenerated(pass.Fset.File(file.Pos())) {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			res.functionResults[fn] = buildFunctionResult(fn, pass, res)
		}
	}

	return res, nil
}

// maps bundles this Result's per-kind name maps for classify.
func (r *Result) maps() primitiveMaps {
	return primitiveMaps{mu: r.Mutexes, rw: r.RWMutexes, wg: r.WaitGroups, once: r.Onces}
}

func (fr *FunctionResult) maps() primitiveMaps {
	return primitiveMaps{mu: fr.Mutexes, rw: fr.RWMutexes, wg: fr.WaitGroups, once: fr.Onces}
}

// primitiveMaps bundles the per-kind name maps so classify keeps a single
// signature as new primitives are added.
type primitiveMaps struct {
	mu, rw, wg, once map[string]bool
}

type primitiveKind uint8

const (
	notPrimitive primitiveKind = iota
	mutexPrimitive
	rwMutexPrimitive
	waitGroupPrimitive
	oncePrimitive
)

// ForFunction returns the primitives visible inside fn, merging the
// per-function scan with the supplied package-scope Result. The returned
// LocalWaitGroups field captures the function-local waitgroups *before*
// the merge so callers can tell locals apart from package-level vars.
func ForFunction(fn *ast.FuncDecl, pass *analysis.Pass, pkg *Result) *FunctionResult {
	if pkg != nil && pkg.functionResults != nil {
		if cached := pkg.functionResults[fn]; cached != nil {
			return cached
		}
	}
	return buildFunctionResult(fn, pass, pkg)
}

func buildFunctionResult(fn *ast.FuncDecl, pass *analysis.Pass, pkg *Result) *FunctionResult {
	fr := &FunctionResult{
		Mutexes:    map[string]bool{},
		RWMutexes:  map[string]bool{},
		WaitGroups: map[string]bool{},
		Onces:      map[string]bool{},
	}

	// Function parameters: include mutex/rwmutex/once but intentionally not
	// waitgroups (Done-only worker functions would produce false positives).
	if fn.Type != nil && fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			typ := pass.TypesInfo.TypeOf(field.Type)
			if typ == nil {
				continue
			}
			for _, name := range field.Names {
				switch classifyType(typ, typeKindCache(pkg)) {
				case mutexPrimitive:
					fr.Mutexes[name.Name] = true
				case rwMutexPrimitive:
					fr.RWMutexes[name.Name] = true
				case oncePrimitive:
					fr.Onces[name.Name] = true
				}
			}
		}
	}

	// A type that embeds a sync mutex locks itself through the promoted method
	// (`func (s *Server) Get() { s.Lock(); ... }`), so the receiver is a mutex
	// in its own right. classify keeps it out of the maps unless the promoted
	// Lock/Unlock pair really does come from an embedded sync mutex.
	if name := common.ReceiverName(fn); name != "" {
		if typ := pass.TypesInfo.TypeOf(fn.Recv.List[0].Type); typ != nil {
			classify(name, typ, fr.maps(), typeKindCache(pkg))
		}
	}

	scanBody(fn.Body, pass, fr, typeKindCache(pkg))

	// Snapshot locals before merging package-scope.
	localWG := make(map[string]bool, len(fr.WaitGroups))
	maps.Copy(localWG, fr.WaitGroups)

	if pkg != nil {
		maps.Copy(fr.Mutexes, pkg.Mutexes)
		maps.Copy(fr.RWMutexes, pkg.RWMutexes)
		maps.Copy(fr.WaitGroups, pkg.WaitGroups)
		maps.Copy(fr.Onces, pkg.Onces)
		fr.PackageWaitGroups = pkg.WaitGroups
	} else {
		fr.PackageWaitGroups = map[string]bool{}
	}
	fr.LocalWaitGroups = localWG

	return fr
}

func typeKindCache(pkg *Result) map[types.Type]primitiveKind {
	if pkg == nil {
		return nil
	}
	return pkg.typeKinds
}

// HasMutexes reports whether any mutex or rwmutex name is in scope.
func HasMutexes(fr *FunctionResult) bool {
	return len(fr.Mutexes) > 0 || len(fr.RWMutexes) > 0
}

// HasWaitGroups reports whether any waitgroup name is in scope.
func HasWaitGroups(fr *FunctionResult) bool {
	return len(fr.WaitGroups) > 0
}

// HasOnces reports whether any sync.Once name is in scope.
func HasOnces(fr *FunctionResult) bool {
	return len(fr.Onces) > 0
}

func scanBody(body *ast.BlockStmt, pass *analysis.Pass, fr *FunctionResult, cache map[types.Type]primitiveKind) {
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
				classify(name.Name, typ, fr.maps(), cache)
			}

		case *ast.AssignStmt:
			if node.Tok != token.DEFINE && node.Tok != token.ASSIGN {
				break
			}
			for i, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				// A package-level reassignment resets shared state; it does not
				// introduce a function-local primitive.
				if node.Tok == token.ASSIGN && isPackageScopedVar(ident, pass) {
					continue
				}
				if len(node.Lhs) == len(node.Rhs) {
					// Preserve interface aliases such as `var locker sync.Locker;
					// locker = &mu`: the LHS type is only the interface, while the
					// one-to-one RHS reveals which concrete mutex the alias carries.
					if typ := pass.TypesInfo.TypeOf(node.Rhs[i]); typ != nil {
						classify(ident.Name, typ, fr.maps(), cache)
					}
					continue
				}
				// A single multi-result call has one RHS tuple but several
				// independently typed LHS variables (`entry, err := helper()`).
				if typ := pass.TypesInfo.TypeOf(ident); typ != nil {
					classify(ident.Name, typ, fr.maps(), cache)
				}
			}

		case *ast.SelectorExpr:
			if selection, ok := pass.TypesInfo.Selections[node]; ok && selection.Kind() == types.FieldVal {
				fieldType := selection.Type()
				parentName := common.GetVarName(node.X)
				if parentName != "?" {
					compoundName := parentName + "." + node.Sel.Name
					classify(compoundName, fieldType, fr.maps(), cache)
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
// the target maps so the helper can serve both *Result (package scope) and
// *FunctionResult (per-function) without duplication.
func classify(name string, typ types.Type, into primitiveMaps, cache map[types.Type]primitiveKind) {
	switch classifyType(typ, cache) {
	case mutexPrimitive:
		into.mu[name] = true
	case rwMutexPrimitive:
		into.rw[name] = true
	case waitGroupPrimitive:
		into.wg[name] = true
	case oncePrimitive:
		into.once[name] = true
	}
}

func classifyType(typ types.Type, cache map[types.Type]primitiveKind) primitiveKind {
	if typ == nil {
		return notPrimitive
	}
	if cache != nil {
		if kind, ok := cache[typ]; ok {
			return kind
		}
	}

	var kind primitiveKind
	switch common.ClassifyMutexValue(typ) {
	case common.MutexValue:
		kind = mutexPrimitive
	case common.RWMutexValue:
		kind = rwMutexPrimitive
	default:
		switch {
		case common.IsWaitGroup(typ):
			kind = waitGroupPrimitive
		case common.IsOnce(typ):
			kind = oncePrimitive
		default:
			kind = notPrimitive
		}
	}

	if cache != nil {
		cache[typ] = kind
	}
	return kind
}

// isPackageScopedVar reports whether ident is declared at package level.
func isPackageScopedVar(ident *ast.Ident, pass *analysis.Pass) bool {
	if pass == nil || pass.TypesInfo == nil {
		return false
	}

	v, ok := pass.TypesInfo.ObjectOf(ident).(*types.Var)
	if !ok || v.Pkg() == nil {
		return false
	}

	return v.Parent() == v.Pkg().Scope()
}
