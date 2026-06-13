package common

import (
	"go/ast"
	"go/types"
)

// BuildReceiverMethodMap indexes every method declaration in files by receiver
// type name and then method name, so callers can resolve a method value
// (x.method) to its *ast.FuncDecl.
//
// Generated files are indexed too, so sibling-method lookups that cross the
// generated boundary (e.g. wrapper Lock/Unlock pairs) still resolve.
func BuildReceiverMethodMap(files []*ast.File) map[string]map[string]*ast.FuncDecl {
	methods := make(map[string]map[string]*ast.FuncDecl)

	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv == nil {
				continue
			}

			receiverType := ReceiverTypeName(fn)
			if receiverType == "" {
				continue
			}

			if methods[receiverType] == nil {
				methods[receiverType] = make(map[string]*ast.FuncDecl)
			}
			methods[receiverType][fn.Name.Name] = fn
		}
	}

	return methods
}

// ReceiverTypeName returns the base type name of fn's receiver (e.g. "Foo" for
// both "func (s Foo)" and "func (s *Foo[T])"), or "" when fn has no receiver.
func ReceiverTypeName(fn *ast.FuncDecl) string {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}

	return BaseTypeName(fn.Recv.List[0].Type)
}

// BaseTypeName reduces a type expression to its underlying type name, peeling
// pointers and generic instantiation brackets. Returns "" for expressions that
// do not name a type.
func BaseTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return BaseTypeName(e.X)
	case *ast.IndexExpr:
		return BaseTypeName(e.X)
	case *ast.IndexListExpr:
		return BaseTypeName(e.X)
	case *ast.SelectorExpr:
		return e.Sel.Name
	default:
		return ""
	}
}

// BaseTypeNameFromType reduces a resolved type to its named-type name, peeling
// pointers and aliases. Returns "" when typ is not (a pointer to) a named type.
func BaseTypeNameFromType(typ types.Type) string {
	if typ == nil {
		return ""
	}

	switch t := types.Unalias(typ).(type) {
	case *types.Pointer:
		return BaseTypeNameFromType(t.Elem())
	case *types.Named:
		if obj := t.Obj(); obj != nil {
			return obj.Name()
		}
	}

	return ""
}
