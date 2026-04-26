package common

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strconv"
)

// IsMutex returns true if the given type is sync.Mutex or *sync.Mutex.
func IsMutex(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	return ok && named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == "sync" &&
		named.Obj().Name() == "Mutex"
}

// IsRWMutex returns true if the given type is sync.RWMutex or *sync.RWMutex.
func IsRWMutex(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	return ok && named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == "sync" &&
		named.Obj().Name() == "RWMutex"
}

// IsWaitGroup returns true if the given type is sync.WaitGroup or *sync.WaitGroup.
func IsWaitGroup(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	return ok && named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == "sync" &&
		named.Obj().Name() == "WaitGroup"
}

// GetVarName returns the variable name if the expression is an identifier,
// or a compound name for selector expressions (e.g., "s.mu" for struct field access).
// Returns "?" if the expression cannot be reduced to a name.
func GetVarName(expr ast.Expr) string {
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
