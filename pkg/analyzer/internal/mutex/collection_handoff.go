package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/primitives"
)

// A lock taken on an element that the iteration then stores, still held, in a
// collection, or released on the elements of a collection being ranged over,
// is paired through that collection rather than through a name the function
// can follow:
//
//	for _, loc := range s.locations {
//		loc.mu.Lock()
//		locked = append(locked, loc)
//	}
//	defer func() {
//		for _, l := range locked {
//			l.mu.Unlock()
//		}
//	}()
//
// Matching such calls by name would pair the wrong ones, so the function does
// not track those locks.

// withoutCollectionHeldLocks returns fr without the locks fn holds through a
// collection (see collectionHeldLocks).
func withoutCollectionHeldLocks(fn *ast.FuncDecl, fr *primitives.FunctionResult, info *types.Info) *primitives.FunctionResult {
	if fn == nil || fn.Body == nil || info == nil {
		return fr
	}
	names := collectionHeldLocks(fn.Body, info)
	if len(names) == 0 {
		return fr
	}
	clone := *fr
	clone.Mutexes = maps.Clone(fr.Mutexes)
	clone.RWMutexes = maps.Clone(fr.RWMutexes)
	for name := range names {
		delete(clone.Mutexes, name)
		delete(clone.RWMutexes, name)
	}
	return &clone
}

// elementLock names a lock held by the elements of a collection: the
// collection as written, and the lock's path within an element.
type elementLock struct{ collection, path string }

// rangeLockUse is a range loop that takes, or releases, a lock on its value
// without doing the other in its body.
type rangeLockUse struct {
	name string
	key  elementLock
}

// collectionHeldLocks returns the locks body holds through a collection:
//   - taken on a per-iteration value that is stored, still held, in a
//     collection that outlives the iteration;
//   - released on the value of a range loop whose body does not take them,
//     when the collection was filled with held elements here, or is not filled
//     here at all;
//   - taken on the value of a range loop whose body does not release them,
//     when the function returns that collection or releases the same lock in
//     another loop over it.
func collectionHeldLocks(body *ast.BlockStmt, info *types.Info) map[string]bool {
	names := make(map[string]bool)
	filled := make(map[string]bool)
	filledHeld := make(map[elementLock]bool)
	elementTypes := make(map[string]types.Type)
	var takes, releases []rangeLockUse

	ast.Inspect(body, func(n ast.Node) bool {
		var loopBody *ast.BlockStmt
		switch loop := n.(type) {
		case *ast.ForStmt:
			loopBody = loop.Body
		case *ast.RangeStmt:
			loopBody = loop.Body
		default:
			return true
		}
		perIteration := perIterationVars(n.(ast.Stmt), info)
		if len(perIteration) == 0 || loopBody == nil {
			return true
		}
		rangeValue := rangeValueVar(n, info)
		collection := ""
		if rng, ok := n.(*ast.RangeStmt); ok {
			collection = exprString(rng.X)
			if rangeValue != nil {
				elementTypes[collection] = rangeValue.Type()
			}
		}

		acquired := make(map[string]types.Object)
		released := make(map[string]bool)
		var onValueTakes, onValueReleases []string
		forEachLockCall(loopBody, func(sel *ast.SelectorExpr, acquire bool) {
			root := rootIdent(sel.X)
			name := common.GetVarName(sel.X)
			if root == nil || name == "" {
				return
			}
			obj := info.Uses[root]
			onValue := obj != nil && obj == rangeValue
			if acquire {
				acquired[name] = obj
				if onValue {
					onValueTakes = append(onValueTakes, name)
				}
				return
			}
			released[name] = true
			if onValue {
				onValueReleases = append(onValueReleases, name)
			}
		})

		for _, site := range storeSites(n, loopBody, perIteration, info) {
			filled[site.collection] = true
			for name, obj := range acquired {
				if obj == nil || !site.values[obj] || !heldAt(loopBody, site.stmt, name) {
					continue
				}
				names[name] = true
				filledHeld[elementLock{site.collection, elementPath(name)}] = true
			}
		}
		if collection == "" {
			return true
		}
		for _, name := range onValueReleases {
			if _, ok := acquired[name]; !ok {
				releases = append(releases, rangeLockUse{name, elementLock{collection, elementPath(name)}})
			}
		}
		for _, name := range onValueTakes {
			if !released[name] {
				takes = append(takes, rangeLockUse{name, elementLock{collection, elementPath(name)}})
			}
		}
		return true
	})

	releasedOver := make(map[elementLock]bool)
	for _, r := range releases {
		if !filled[r.key.collection] || filledHeld[r.key] {
			names[r.name] = true
			releasedOver[r.key] = true
		}
	}
	returned := returnedExprs(body)
	for _, t := range takes {
		if releasedOver[t.key] || returned[t.key.collection] {
			names[t.name] = true
		}
	}

	// Another element of a collection released that way, named on its own
	// (`mountedOn.mu.Unlock()` beside the loop that releases the others), is
	// one of the same locks: a value of the element type, at the same path.
	if len(releasedOver) > 0 {
		forEachLockCall(body, func(sel *ast.SelectorExpr, _ bool) {
			root := rootIdent(sel.X)
			name := common.GetVarName(sel.X)
			if root == nil || name == "" || names[name] {
				return
			}
			obj := info.Uses[root]
			if obj == nil {
				return
			}
			for key := range releasedOver {
				if typ := elementTypes[key.collection]; typ != nil && elementPath(name) == key.path &&
					types.Identical(obj.Type(), typ) {
					names[name] = true
					return
				}
			}
		})
	}
	return names
}

// elementPath is name without its root variable: "mu" for "v.mu".
func elementPath(name string) string {
	_, path, _ := strings.Cut(name, ".")
	return path
}

// forEachLockCall calls visit for every Lock, RLock, Unlock and RUnlock call
// statement in body, deferred ones included, outside nested function literals.
func forEachLockCall(body *ast.BlockStmt, visit func(sel *ast.SelectorExpr, acquire bool)) {
	ast.Inspect(body, func(n ast.Node) bool {
		var call *ast.CallExpr
		switch x := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ExprStmt:
			call, _ = common.UnwrapParenExpr(x.X).(*ast.CallExpr)
		case *ast.DeferStmt:
			call = x.Call
		}
		if call == nil {
			return true
		}
		sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Lock", "RLock":
			visit(sel, true)
		case "Unlock", "RUnlock":
			visit(sel, false)
		}
		return true
	})
}

// storeSite is a statement keeping per-iteration values beyond the iteration.
type storeSite struct {
	stmt       ast.Stmt
	collection string
	values     map[types.Object]bool
}

// storeSites returns the statements of body that keep a per-iteration value
// beyond the iteration: appending it to, or assigning it into an element of,
// a collection declared outside loop, or sending it on a channel.
func storeSites(loop ast.Node, body *ast.BlockStmt, perIteration map[types.Object]bool, info *types.Info) []storeSite {
	var sites []storeSite
	valuesIn := func(expr ast.Expr) map[types.Object]bool {
		values := make(map[types.Object]bool)
		ast.Inspect(expr, func(n ast.Node) bool {
			if ident, ok := n.(*ast.Ident); ok && perIteration[info.Uses[ident]] {
				values[info.Uses[ident]] = true
			}
			return true
		})
		return values
	}
	outlivesLoop := func(expr ast.Expr) bool {
		root := rootIdent(expr)
		if root == nil {
			return false
		}
		v, ok := info.ObjectOf(root).(*types.Var)
		return ok && (v.Pos() < loop.Pos() || v.Pos() > loop.End())
	}
	add := func(stmt ast.Stmt, collection ast.Expr, value ast.Expr) {
		if values := valuesIn(value); len(values) > 0 {
			sites = append(sites, storeSite{stmt: stmt, collection: exprString(collection), values: values})
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.SendStmt:
			add(x, x.Chan, x.Value)
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				if i >= len(x.Rhs) || !outlivesLoop(lhs) {
					continue
				}
				if call, ok := common.UnwrapParenExpr(x.Rhs[i]).(*ast.CallExpr); ok && isBuiltinAppend(call, info) {
					for _, arg := range call.Args[1:] {
						add(x, lhs, arg)
					}
				} else if index, ok := common.UnwrapParenExpr(lhs).(*ast.IndexExpr); ok && x.Tok == token.ASSIGN {
					add(x, index.X, x.Rhs[i])
				}
			}
		}
		return true
	})
	return sites
}

// heldAt reports whether name is held when stmt runs within body: the last
// lock call on it before stmt, along the statements leading there, takes it.
// Anything less plain answers no.
func heldAt(body *ast.BlockStmt, stmt ast.Stmt, name string) bool {
	path, ok := findStatementPath(body.List, stmt)
	if !ok {
		return false
	}
	for _, frame := range slices.Backward(path) {
		for j := frame.index - 1; j >= 0; j-- {
			prev := frame.list[j]
			if method, direct := directLockEffect(prev, name); direct {
				return method == "Lock" || method == "RLock"
			}
			if statementContainsLockEffect(prev, name) {
				return false
			}
		}
	}
	return false
}

func isBuiltinAppend(call *ast.CallExpr, info *types.Info) bool {
	ident, ok := common.UnwrapParenExpr(call.Fun).(*ast.Ident)
	if !ok || len(call.Args) < 2 {
		return false
	}
	_, builtin := info.Uses[ident].(*types.Builtin)
	return builtin && ident.Name == "append"
}

// rangeValueVar returns the value variable a range loop declares, or nil.
func rangeValueVar(loop ast.Node, info *types.Info) types.Object {
	rng, ok := loop.(*ast.RangeStmt)
	if !ok || rng.Tok != token.DEFINE {
		return nil
	}
	ident, ok := rng.Value.(*ast.Ident)
	if !ok {
		return nil
	}
	return info.Defs[ident]
}

// returnedExprs returns the expressions body's own returns hand back, as
// written.
func returnedExprs(body *ast.BlockStmt) map[string]bool {
	returned := make(map[string]bool)
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			for _, result := range x.Results {
				if text := exprString(result); text != "" {
					returned[text] = true
				}
			}
		}
		return true
	})
	return returned
}
