package common

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"strconv"
)

// IsGeneratedFile reports whether file has the standard
// `// Code generated ... DO NOT EDIT.` header. Apply at the report boundary
// only: cross-file symbol maps must keep generated declarations.
func IsGeneratedFile(file *ast.File) bool {
	return ast.IsGenerated(file)
}

// DerefOnceAndUnalias removes a single pointer indirection from typ
// and resolves aliases before and after dereferencing.
func DerefOnceAndUnalias(typ types.Type) types.Type {
	typ = types.Unalias(typ)
	typ = DerefOnce(typ)
	return types.Unalias(typ)
}

// DerefOnce removes a single pointer indirection from typ.
// Non-pointer types are returned unchanged.
func DerefOnce(typ types.Type) types.Type {
	if ptr, ok := typ.(*types.Pointer); ok {
		return ptr.Elem()
	}
	return typ
}

// IsMutex returns true if the given type is sync.Mutex or *sync.Mutex.
func IsMutex(typ types.Type) bool {
	typ = DerefOnce(typ)
	return MatchesPkgAndName(typ, "sync", "Mutex")
}

// IsRWMutex returns true if the given type is sync.RWMutex or *sync.RWMutex.
func IsRWMutex(typ types.Type) bool {
	typ = DerefOnce(typ)
	return MatchesPkgAndName(typ, "sync", "RWMutex")
}

// IsWaitGroup returns true if the given type is sync.WaitGroup or *sync.WaitGroup.
func IsWaitGroup(typ types.Type) bool {
	typ = DerefOnce(typ)
	return MatchesPkgAndName(typ, "sync", "WaitGroup")
}

// IsOnce returns true if the given type is sync.Once or *sync.Once.
func IsOnce(typ types.Type) bool {
	typ = DerefOnce(typ)
	return MatchesPkgAndName(typ, "sync", "Once")
}

// IsCond returns true if the given type is sync.Cond or *sync.Cond. In
// practice a Cond is almost always used through the *sync.Cond returned by
// sync.NewCond, so the pointer form is the common one.
func IsCond(typ types.Type) bool {
	typ = DerefOnce(typ)
	return MatchesPkgAndName(typ, "sync", "Cond")
}

// IsPool returns true if the given type is sync.Pool or *sync.Pool. Pool
// methods have pointer receivers, so a value is auto-addressed at the call
// site; both the value and pointer forms appear in practice.
func IsPool(typ types.Type) bool {
	typ = DerefOnce(typ)
	return MatchesPkgAndName(typ, "sync", "Pool")
}

// MatchesPkgAndName reports whether typ is a named type declared in pkg
// whose name matches any of names.
//
// The type is matched nominally and is not automatically dereferenced
// or unaliased.
func MatchesPkgAndName(typ types.Type, pkg string, names ...string) bool {
	_, matches := MatchPkgAndName(typ, pkg, names...)
	return matches
}

// MatchPkgAndName returns the matched type name if typ is a named type
// declared in pkg whose name matches any of names.
//
// The returned boolean reports whether a match was found.
//
// The type is matched nominally and is not automatically dereferenced
// or unaliased.
func MatchPkgAndName(typ types.Type, pkg string, names ...string) (string, bool) {
	named, ok := typ.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return "", false
	}

	if named.Obj().Pkg().Path() != pkg {
		return "", false
	}

	name := named.Obj().Name()
	if slices.Contains(names, name) {
		return name, true
	}

	return "", false
}

// GetVarName returns the variable name if the expression is an identifier,
// or a compound name for selector expressions (e.g., "s.mu" for struct field access).
// Returns "?" if the expression cannot be reduced to a name.
func GetVarName(expr ast.Expr) string {
	expr = UnwrapParenExpr(expr)
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		parent := GetVarName(e.X)
		if parent != "?" {
			return parent + "." + e.Sel.Name
		}
	}
	return "?"
}

// UnwrapParenExpr returns the underlying expression stripped of any surrounding parentheses.
//
// It iteratively removes all layers of *ast.ParenExpr until a non-parenthesized
// expression is found. If the input expression is not parenthesized or is nil,
// it returns the original expression unchanged.
func UnwrapParenExpr(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}

		expr = paren.X
	}
}

// GetAddValue extracts the integer value from an Add() call.
// If the argument is not a literal integer, returns 1 by default.
func GetAddValue(call *ast.CallExpr) int {
	if len(call.Args) == 0 {
		return 1
	}
	if val, ok := IntegerLiteralValue(call.Args[0]); ok {
		return val
	}
	// If the argument is not a literal integer, default to 1.
	return 1
}

// ConstantIntValue returns the integer value of expr when it is a literal or a
// typed constant available through go/types.
func ConstantIntValue(expr ast.Expr, info *types.Info) (int, bool) {
	if val, ok := IntegerLiteralValue(expr); ok {
		return val, true
	}
	if expr == nil || info == nil {
		return 0, false
	}

	tv, ok := info.Types[expr]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.Int {
		return 0, false
	}

	value, ok := constant.Int64Val(tv.Value)
	if !ok {
		return 0, false
	}
	return int(value), true
}

// IsConstantIntExpr reports whether expr is a compile-time integer constant
// (literal, named const, or unary +/- of those). Returns false for runtime
// expressions like len(x), variables, or function calls.
func IsConstantIntExpr(expr ast.Expr, info *types.Info) bool {
	if expr == nil {
		return false
	}
	if _, ok := IntegerLiteralValue(expr); ok {
		return true
	}
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	return ok && tv.Value != nil && tv.Value.Kind() == constant.Int
}

// IntegerLiteralValue extracts a signed integer literal value when expr is a
// basic integer literal or a unary +/- integer literal.
func IntegerLiteralValue(expr ast.Expr) (int, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.INT {
			return 0, false
		}
		val, err := strconv.Atoi(e.Value)
		if err != nil {
			return 0, false
		}
		return val, true
	case *ast.UnaryExpr:
		val, ok := IntegerLiteralValue(e.X)
		if !ok {
			return 0, false
		}
		switch e.Op {
		case token.ADD:
			return val, true
		case token.SUB:
			return -val, true
		}
	}
	return 0, false
}

// ReceiverName returns the bound name of fn's receiver (e.g. "s" in
// "func (s *Foo) Bar()"). It returns "" when fn has no receiver, when the
// receiver list is empty, or when the receiver is unnamed.
func ReceiverName(fn *ast.FuncDecl) string {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	if len(fn.Recv.List[0].Names) == 0 {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}

// ConstantBoolValue returns the constant boolean value of expr when one is
// available in the type information.
func ConstantBoolValue(expr ast.Expr, info *types.Info) (bool, bool) {
	if expr == nil || info == nil {
		return false, false
	}

	tv, ok := info.Types[expr]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.Bool {
		return false, false
	}

	return constant.BoolVal(tv.Value), true
}
