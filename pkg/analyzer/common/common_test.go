package common

import (
	"go/ast"
	"go/constant"
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

	// SelectorExpr: s.mu → "s.mu"
	sel := &ast.SelectorExpr{
		X:   &ast.Ident{Name: "s"},
		Sel: &ast.Ident{Name: "mu"},
	}
	assert.Equal(t, "s.mu", GetVarName(sel))

	// Nested SelectorExpr: a.b.mu → "a.b.mu"
	nested := &ast.SelectorExpr{
		X:   sel,
		Sel: &ast.Ident{Name: "extra"},
	}
	assert.Equal(t, "s.mu.extra", GetVarName(nested))

	// SelectorExpr with non-ident root → "?"
	selBadRoot := &ast.SelectorExpr{
		X:   &ast.BasicLit{Value: "123"},
		Sel: &ast.Ident{Name: "mu"},
	}
	assert.Equal(t, "?", GetVarName(selBadRoot))
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

func TestIntegerLiteralValue(t *testing.T) {
	tests := []struct {
		name  string
		expr  ast.Expr
		value int
		ok    bool
	}{
		{
			name:  "positive",
			expr:  &ast.BasicLit{Kind: token.INT, Value: "5"},
			value: 5,
			ok:    true,
		},
		{
			name: "explicit plus",
			expr: &ast.UnaryExpr{
				Op: token.ADD,
				X:  &ast.BasicLit{Kind: token.INT, Value: "5"},
			},
			value: 5,
			ok:    true,
		},
		{
			name: "negative",
			expr: &ast.UnaryExpr{
				Op: token.SUB,
				X:  &ast.BasicLit{Kind: token.INT, Value: "3"},
			},
			value: -3,
			ok:    true,
		},
		{
			name: "nested unary",
			expr: &ast.UnaryExpr{
				Op: token.SUB,
				X: &ast.UnaryExpr{
					Op: token.SUB,
					X:  &ast.BasicLit{Kind: token.INT, Value: "3"},
				},
			},
			value: 3,
			ok:    true,
		},
		{
			name: "unsupported unary",
			expr: &ast.UnaryExpr{
				Op: token.XOR,
				X:  &ast.BasicLit{Kind: token.INT, Value: "3"},
			},
			ok: false,
		},
		{
			name: "non-integer literal",
			expr: &ast.BasicLit{Kind: token.STRING, Value: `"3"`},
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, ok := IntegerLiteralValue(tt.expr)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.value, value)
		})
	}
}

func TestConstantBoolValue(t *testing.T) {
	trueExpr := &ast.Ident{Name: "always"}
	falseExpr := &ast.Ident{Name: "never"}
	unknownExpr := &ast.Ident{Name: "maybe"}

	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{
			trueExpr: {
				Type:  types.Typ[types.Bool],
				Value: constant.MakeBool(true),
			},
			falseExpr: {
				Type:  types.Typ[types.Bool],
				Value: constant.MakeBool(false),
			},
			unknownExpr: {
				Type: types.Typ[types.Bool],
			},
		},
	}

	value, ok := ConstantBoolValue(trueExpr, info)
	assert.True(t, ok)
	assert.True(t, value)

	value, ok = ConstantBoolValue(falseExpr, info)
	assert.True(t, ok)
	assert.False(t, value)

	_, ok = ConstantBoolValue(unknownExpr, info)
	assert.False(t, ok)

	_, ok = ConstantBoolValue(nil, info)
	assert.False(t, ok)

	_, ok = ConstantBoolValue(trueExpr, nil)
	assert.False(t, ok)
}

func makeNamedType(pkgPath, name string, isPtr bool) types.Type {
	pkg := types.NewPackage(pkgPath, "")
	named := types.NewNamed(types.NewTypeName(0, pkg, name, nil), nil, nil)
	if isPtr {
		return types.NewPointer(named)
	}
	return named
}
