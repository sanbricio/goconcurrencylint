package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A function that takes a lock and returns a handle hands the release to the
// handle's methods. The handle is not always the returned value itself: it
// may be built inside the return expression and wrapped on its way out, with
// the locked value stored through an embedded struct.
//
//	func (s *store) Read() TxnRead {
//		s.mu.RLock()
//		return newMetricsTxnRead(&storeTxnRead{storeTxnCommon{s, tx}, tx})
//	}
//
//	func (tr *storeTxnRead) End() { tr.s.mu.RUnlock() }
//
// A handleBinding records one such stored value: a composite literal of
// typeName, built in a return expression of fn, whose field (a path through
// embedded structs, as the handle's methods spell it) holds source.
type handleBinding struct {
	typeName string
	field    string
	source   string
	ret      *ast.ReturnStmt
	fn       *ast.FuncDecl
}

// returnedHandleBindings lists the bindings of the handles fn builds in its
// return expressions: the literal itself, one reached through a local variable,
// or one passed to the function that wraps it.
func (l *lifecycleResolver) returnedHandleBindings(fn *ast.FuncDecl) []handleBinding {
	if fn == nil || fn.Body == nil || l.typesInfo == nil {
		return nil
	}
	if cached, ok := l.scanCache.handleBindings[fn]; ok {
		return cached
	}

	localValues := make(map[string]ast.Expr)
	var bindings []handleBinding
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			if len(node.Lhs) == len(node.Rhs) {
				for i, lhs := range node.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok {
						localValues[ident.Name] = node.Rhs[i]
					}
				}
			}
		case *ast.ReturnStmt:
			seen := make(map[string]bool)
			var collect func(expr ast.Expr)
			collect = func(expr ast.Expr) {
				switch e := common.UnwrapParenExpr(expr).(type) {
				case *ast.CompositeLit:
					for _, b := range l.literalBindings(e) {
						b.ret, b.fn = node, fn
						bindings = append(bindings, b)
					}
				case *ast.UnaryExpr:
					if e.Op == token.AND {
						collect(e.X)
					}
				case *ast.CallExpr:
					for _, arg := range e.Args {
						collect(arg)
					}
				case *ast.Ident:
					if value, ok := localValues[e.Name]; ok && !seen[e.Name] {
						seen[e.Name] = true
						collect(value)
					}
				}
			}
			for _, result := range node.Results {
				collect(result)
			}
		}
		return true
	})

	l.scanCache.handleBindings[fn] = bindings
	return bindings
}

// literalBindings lists the values lit stores, by the field path its type's
// methods reach them through. A struct literal for an embedded field contributes
// its own bindings twice: under its own type, and promoted into lit's type.
func (l *lifecycleResolver) literalBindings(lit *ast.CompositeLit) []handleBinding {
	typ := l.typesInfo.TypeOf(lit)
	typeName := common.BaseTypeNameFromType(typ)
	if typ == nil || typeName == "" {
		return nil
	}
	st, ok := typ.Underlying().(*types.Struct)
	if !ok {
		return nil
	}

	var bindings []handleBinding
	for i, elt := range lit.Elts {
		var field *types.Var
		value := elt
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			field = structFieldNamed(st, key.Name)
			value = kv.Value
		} else if i < st.NumFields() {
			field = st.Field(i)
		}
		if field == nil {
			continue
		}

		if inner := compositeLiteralFromExpr(common.UnwrapParenExpr(value)); inner != nil {
			for _, b := range l.literalBindings(inner) {
				bindings = append(bindings, b)
				nested := b
				nested.typeName = typeName
				if !field.Embedded() {
					nested.field = field.Name() + "." + b.field
				}
				bindings = append(bindings, nested)
			}
			continue
		}

		source := value
		if unary, ok := common.UnwrapParenExpr(value).(*ast.UnaryExpr); ok && unary.Op == token.AND {
			source = unary.X
		}
		if name := common.GetVarName(source); name != "" && name != "?" {
			bindings = append(bindings, handleBinding{typeName: typeName, field: field.Name(), source: name})
		}
	}
	return bindings
}

func structFieldNamed(st *types.Struct, name string) *types.Var {
	for field := range st.Fields() {
		if field.Name() == name {
			return field
		}
	}
	return nil
}

// returnsHandleBindingFor reports whether the current function stores the
// value holding mutexName in a handle it returns, and a method of the handle
// releases the lock through that field.
func (l *lifecycleResolver) returnsHandleBindingFor(mutexName string, unlockMethods []string) bool {
	for _, b := range l.returnedHandleBindings(l.function) {
		if b.source == mutexName {
			if l.releaseMethodUnlocks(b.typeName, b.field, "", unlockMethods) {
				return true
			}
			continue
		}
		if suffix, ok := relativeMutexPath(mutexName, b.source); ok && l.releaseMethodUnlocks(b.typeName, b.field, suffix, unlockMethods) {
			return true
		}
	}
	return false
}

// isReleaseForHandleBinding is the other half: the current method of a handle
// type releases a lock that some function of the package took on the value it
// stored in such a handle before returning it.
func (l *lifecycleResolver) isReleaseForHandleBinding(currentType, path string, acquireMethods []string) bool {
	for _, b := range l.handleBindingsOfType(currentType) {
		if b.fn == l.function {
			continue
		}
		rest, ok := strings.CutPrefix(path, b.field)
		if !ok || (rest != "" && !strings.HasPrefix(rest, ".")) {
			continue
		}
		if functionBodyContainsFieldCallBefore(b.fn.Body, b.source+rest, acquireMethods, b.ret.Pos()) {
			return true
		}
	}
	return false
}

// handleBindingsOfType indexes every handle binding of the package by type.
func (l *lifecycleResolver) handleBindingsOfType(typeName string) []handleBinding {
	if l.scanCache.handleBindingsByType == nil {
		index := make(map[string][]handleBinding)
		for _, fn := range l.functions {
			for _, b := range l.returnedHandleBindings(fn) {
				index[b.typeName] = append(index[b.typeName], b)
			}
		}
		l.scanCache.handleBindingsByType = index
	}
	return l.scanCache.handleBindingsByType[typeName]
}
