package common

import (
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsMutex(t *testing.T) {
	assert.True(t, IsMutex(makeNamedType("sync", "Mutex", false)))
	assert.True(t, IsMutex(makeNamedType("sync", "Mutex", true)))
	assert.False(t, IsMutex(makeNamedType("sync", "RWMutex", false)))
	assert.False(t, IsMutex(makeNamedType("sync", "WaitGroup", false)))
	assert.False(t, IsMutex(nil))
}

func TestIsRWMutex(t *testing.T) {
	assert.True(t, IsRWMutex(makeNamedType("sync", "RWMutex", false)))
	assert.True(t, IsRWMutex(makeNamedType("sync", "RWMutex", true)))
	assert.False(t, IsRWMutex(makeNamedType("sync", "Mutex", false)))
	assert.False(t, IsRWMutex(makeNamedType("sync", "WaitGroup", false)))
	assert.False(t, IsRWMutex(nil))
}

func TestIsWaitGroup(t *testing.T) {
	assert.True(t, IsWaitGroup(makeNamedType("sync", "WaitGroup", false)))
	assert.True(t, IsWaitGroup(makeNamedType("sync", "WaitGroup", true)))
	assert.False(t, IsWaitGroup(makeNamedType("sync", "Mutex", false)))
	assert.False(t, IsWaitGroup(makeNamedType("sync", "RWMutex", false)))
	assert.False(t, IsWaitGroup(nil))
}

func TestGetVarName(t *testing.T) {
	ident := &ast.Ident{Name: "foo"}
	assert.Equal(t, "foo", GetVarName(ident))

	lit := &ast.BasicLit{Value: "123"}
	assert.Equal(t, "?", GetVarName(lit))

	assert.Equal(t, "?", GetVarName(nil))
}

func TestGetAddValue(t *testing.T) {
	call := &ast.CallExpr{
		Args: []ast.Expr{
			&ast.BasicLit{Kind: token.INT, Value: "3"},
		},
	}
	assert.Equal(t, 3, GetAddValue(call))

	callNoArgs := &ast.CallExpr{}
	assert.Equal(t, 1, GetAddValue(callNoArgs))

	callNonLit := &ast.CallExpr{
		Args: []ast.Expr{&ast.Ident{Name: "foo"}},
	}
	assert.Equal(t, 1, GetAddValue(callNonLit))

	callWrongLit := &ast.CallExpr{
		Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: "\"4\""}},
	}
	assert.Equal(t, 1, GetAddValue(callWrongLit))


	callBadLit := &ast.CallExpr{
		Args: []ast.Expr{&ast.BasicLit{Kind: token.INT, Value: "xxx"}},
	}
	assert.Equal(t, 1, GetAddValue(callBadLit))
}

func makeNamedType(pkgPath, name string, isPtr bool) types.Type {
	pkg := types.NewPackage(pkgPath, "")
	named := types.NewNamed(types.NewTypeName(0, pkg, name, nil), nil, nil)
	if isPtr {
		return types.NewPointer(named)
	}
	return named
}
