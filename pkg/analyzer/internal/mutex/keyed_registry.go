package mutex

import (
	"go/ast"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// Keyed lockers keep their locks in a registry the receiver owns and reach
// them by lookup, not only by indexing (see isKeyedLocalWrapperPair):
//
//	func (s *KeyLocker) Lock(id string)   { s.mutexFor(id).Lock() }
//	func (s *KeyLocker) Unlock(id string) { l, _ := s.m.Load(id); l.(*sync.Mutex).Unlock() }
//	func (s *KeyLocker) mutexFor(id string) *sync.Mutex {
//		l, _ := s.m.LoadOrStore(id, &sync.Mutex{})
//		return l.(*sync.Mutex)
//	}
//
// The registry is the receiver field the lookup is made on (`m`), whatever
// the lookup is called; a type assertion on what it returns, or a method of
// the same type returning it, keeps pointing at the same registry.

// registrySource returns the receiver field (as a path below receiver) a lock
// expression of fn is looked up from, or "".
func (w *wrapperResolver) registrySource(fn *ast.FuncDecl, expr ast.Expr, depth int) string {
	if fn == nil || fn.Body == nil || depth > 3 {
		return ""
	}
	receiver := common.ReceiverName(fn)
	if receiver == "" {
		return ""
	}
	relative := func(name string) string {
		path, ok := strings.CutPrefix(name, receiver+".")
		if !ok {
			return ""
		}
		return path
	}

	switch node := common.UnwrapParenExpr(expr).(type) {
	case *ast.Ident:
		// Every value the local receives must come out of the same registry:
		// one reassigned from another call may hold a lock that call took.
		if parameterIndex(fn, node.Name) >= 0 {
			return ""
		}
		values, ok := localAssignedValues(fn.Body, node.Name)
		if !ok || len(values) == 0 {
			return ""
		}
		source := ""
		for _, value := range values {
			got := w.registrySource(fn, value, depth+1)
			if got == "" || (source != "" && got != source) {
				return ""
			}
			source = got
		}
		return source
	case *ast.TypeAssertExpr:
		return w.registrySource(fn, node.X, depth+1)
	case *ast.IndexExpr:
		return relative(common.GetVarName(node.X))
	case *ast.CallExpr:
		sel, ok := common.UnwrapParenExpr(node.Fun).(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		if common.GetVarName(sel.X) == receiver {
			// A method of the same type handing out the registry's lock.
			method := w.receiverMethods[common.ReceiverTypeName(fn)][sel.Sel.Name]
			if method == nil || method == fn {
				return ""
			}
			return w.returnedRegistrySource(method, depth+1)
		}
		return relative(common.GetVarName(sel.X))
	}
	return ""
}

// returnedRegistrySource returns the registry every return of method hands a
// lock out of, or "".
func (w *wrapperResolver) returnedRegistrySource(method *ast.FuncDecl, depth int) string {
	source := ""
	for _, ret := range functionReturns(method.Body) {
		if len(ret.Results) != 1 {
			return ""
		}
		got := w.registrySource(method, ret.Results[0], depth)
		if got == "" || (source != "" && got != source) {
			return ""
		}
		source = got
	}
	return source
}

// localAssignedValues returns every value body assigns to name; ok is false
// when one of them cannot be told (a later result of a multi-value call).
func localAssignedValues(body *ast.BlockStmt, name string) ([]ast.Expr, bool) {
	var values []ast.Expr
	ok := true
	ast.Inspect(body, func(n ast.Node) bool {
		assign, isAssign := n.(*ast.AssignStmt)
		if !isAssign || !ok {
			return ok
		}
		for index, lhs := range assign.Lhs {
			ident, isIdent := lhs.(*ast.Ident)
			if !isIdent || ident.Name != name {
				continue
			}
			switch {
			case len(assign.Rhs) == len(assign.Lhs):
				values = append(values, assign.Rhs[index])
			case len(assign.Rhs) == 1 && index == 0:
				// `l, ok := s.m.Load(id)`: the first result is the value.
				values = append(values, assign.Rhs[0])
			default:
				ok = false
			}
		}
		return ok
	})
	return values, ok
}

// isKeyedRegistryPair reports whether the lock operation under analysis acts
// on a lock looked up from a registry of the receiver, and a sibling method
// performs the opposite operation on a lock from the same registry.
func (w *wrapperResolver) isKeyedRegistryPair(varName, methodName string, oppositeMethods []string) bool {
	if strings.Contains(varName, ".") {
		return false
	}
	source := w.registrySource(w.function, ast.NewIdent(varName), 0)
	if source == "" {
		return false
	}
	for name, sibling := range w.receiverMethods[common.ReceiverTypeName(w.function)] {
		if name == w.function.Name.Name || sibling == nil || sibling.Body == nil {
			continue
		}
		found := false
		ast.Inspect(sibling.Body, func(n ast.Node) bool {
			if found {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr)
			if ok && containsMethod(oppositeMethods, sel.Sel.Name) && w.registrySource(sibling, sel.X, 0) == source {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}
