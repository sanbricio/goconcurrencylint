package common

import (
	"go/ast"
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

// GetVarName returns the variable name if the expression is an identifier, otherwise returns "?".
func GetVarName(expr ast.Expr) string {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return "?"
}

// GetAddValue extracts the integer value from an Add() call.
// If the argument is not a literal integer, returns 1 by default.
func GetAddValue(call *ast.CallExpr) int {
	if len(call.Args) == 0 {
		return 1
	}
	if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.INT {
		if val, err := strconv.Atoi(lit.Value); err == nil {
			return val
		}
	}
	// If the argument is not a literal integer, default to 1.
	return 1
}
