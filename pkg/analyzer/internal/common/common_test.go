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

func TestIsConstantIntExpr(t *testing.T) {
	literalZero := &ast.BasicLit{Kind: token.INT, Value: "0"}
	literalFive := &ast.BasicLit{Kind: token.INT, Value: "5"}
	constIdent := &ast.Ident{Name: "K"}
	unaryNegLit := &ast.UnaryExpr{Op: token.SUB, X: &ast.BasicLit{Kind: token.INT, Value: "3"}}
	lenCall := &ast.CallExpr{Fun: &ast.Ident{Name: "len"}, Args: []ast.Expr{&ast.Ident{Name: "s"}}}
	varIdent := &ast.Ident{Name: "n"}

	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{
			constIdent: {Value: constant.MakeInt64(7)},
			lenCall:    {Value: nil},
			varIdent:   {Value: nil},
		},
	}

	assert.True(t, IsConstantIntExpr(literalZero, info))
	assert.True(t, IsConstantIntExpr(literalFive, info))
	assert.True(t, IsConstantIntExpr(unaryNegLit, info))
	assert.True(t, IsConstantIntExpr(constIdent, info))

	assert.False(t, IsConstantIntExpr(lenCall, info))
	assert.False(t, IsConstantIntExpr(varIdent, info))
	assert.False(t, IsConstantIntExpr(nil, info))

	// Literals resolve without types.Info; non-literal idents do not.
	assert.True(t, IsConstantIntExpr(literalZero, nil))
	assert.False(t, IsConstantIntExpr(constIdent, nil))
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

func TestUnwrapParenExpr(t *testing.T) {
	identMu := &ast.Ident{Name: "mu"}

	singleParen := &ast.ParenExpr{X: identMu}

	multiParen := &ast.ParenExpr{
		X: &ast.ParenExpr{
			X: &ast.ParenExpr{
				X: identMu,
			},
		},
	}

	tests := []struct {
		name  string
		input ast.Expr
		want  ast.Expr
	}{
		{
			name:  "Non-parenthesized expression (Ident) returns itself",
			input: identMu,
			want:  identMu,
		},
		{
			name:  "Single parenthesized expression (x) unwraps to x",
			input: singleParen,
			want:  identMu,
		},
		{
			name:  "Nested parenthesized expression (((x))) unwraps completely to x",
			input: multiParen,
			want:  identMu,
		},
		{
			name:  "Nil input returns nil without panicking",
			input: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UnwrapParenExpr(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDerefOnceAndUnalias(t *testing.T) {
	pkg := types.NewPackage("example.com/test", "test")
	intType := types.Typ[types.Int]

	objAliasInt := types.NewTypeName(token.NoPos, pkg, "AliasInt", nil)
	aliasInt := types.NewAlias(objAliasInt, intType)

	ptrToInt := types.NewPointer(intType)

	ptrToAliasInt := types.NewPointer(aliasInt)

	objAliasPtr := types.NewTypeName(token.NoPos, pkg, "AliasPtr", nil)
	aliasPtr := types.NewAlias(objAliasPtr, ptrToAliasInt)

	tests := []struct {
		name  string
		input types.Type
		want  types.Type
	}{
		{
			name:  "Basic type (int) returns itself",
			input: intType,
			want:  intType,
		},
		{
			name:  "Direct alias to basic type (AliasInt) unaliases to int",
			input: aliasInt,
			want:  intType,
		},
		{
			name:  "Standard pointer (*int) dereferences directly to int",
			input: ptrToInt,
			want:  intType,
		},
		{
			name:  "Pointer to alias (*AliasInt) dereferences and unaliases to int",
			input: ptrToAliasInt,
			want:  intType,
		},
		{
			name:  "Alias to pointer to alias (AliasPtr -> *AliasInt -> int) fully resolves to int",
			input: aliasPtr,
			want:  intType,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DerefOnceAndUnalias(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDerefOnce(t *testing.T) {
	intType := types.Typ[types.Int]
	ptrToInt := types.NewPointer(intType)
	ptrToPtrToInt := types.NewPointer(ptrToInt)

	tests := []struct {
		name  string
		input types.Type
		want  types.Type
	}{
		{
			name:  "Non-pointer type (Basic int) returns itself",
			input: intType,
			want:  intType,
		},
		{
			name:  "Single pointer (*int) returns underlying type",
			input: ptrToInt,
			want:  intType,
		},
		{
			name:  "Double pointer (**int) dereferences only once to *int",
			input: ptrToPtrToInt,
			want:  ptrToInt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DerefOnce(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMatchPkgAndName(t *testing.T) {
	pkgFoo := types.NewPackage("example.com/foo", "foo")
	objA := types.NewTypeName(token.NoPos, pkgFoo, "TypeA", nil)
	typeA := types.NewNamed(objA, types.Typ[types.String], nil)

	objB := types.NewTypeName(token.NoPos, pkgFoo, "TypeB", nil)
	typeB := types.NewNamed(objB, types.Typ[types.Int], nil)

	pkgBar := types.NewPackage("example.com/bar", "bar")
	objC := types.NewTypeName(token.NoPos, pkgBar, "TypeA", nil)
	typeC := types.NewNamed(objC, types.Typ[types.String], nil)

	objNilPkg := types.NewTypeName(token.NoPos, nil, "TypeA", nil)
	typeNilPkg := types.NewNamed(objNilPkg, types.Typ[types.Bool], nil)

	tests := []struct {
		name        string
		typ         types.Type
		pkg         string
		names       []string
		wantName    string
		wantMatched bool
	}{
		{
			name:        "Not a named type (Basic int type)",
			typ:         types.Typ[types.Int],
			pkg:         "example.com/foo",
			names:       []string{"TypeA"},
			wantName:    "",
			wantMatched: false,
		},
		{
			name:        "Named type with nil package",
			typ:         typeNilPkg,
			pkg:         "example.com/foo",
			names:       []string{"TypeA"},
			wantName:    "",
			wantMatched: false,
		},
		{
			name:        "Package does not match (same type name)",
			typ:         typeC,
			pkg:         "example.com/foo",
			names:       []string{"TypeA"},
			wantName:    "",
			wantMatched: false,
		},
		{
			name:        "Package matches but the name is not in the list",
			typ:         typeB,
			pkg:         "example.com/foo",
			names:       []string{"TypeA", "TypeC"},
			wantName:    "",
			wantMatched: false,
		},
		{
			name:        "Exact match with a single name",
			typ:         typeA,
			pkg:         "example.com/foo",
			names:       []string{"TypeA"},
			wantName:    "TypeA",
			wantMatched: true,
		},
		{
			name:        "Exact match within multiple valid names",
			typ:         typeA,
			pkg:         "example.com/foo",
			names:       []string{"TypeB", "TypeA", "TypeC"},
			wantName:    "TypeA",
			wantMatched: true,
		},
		{
			name:        "Empty names list",
			typ:         typeA,
			pkg:         "example.com/foo",
			names:       []string{},
			wantName:    "",
			wantMatched: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotMatched := MatchPkgAndName(tt.typ, tt.pkg, tt.names...)
			assert.Equal(t, tt.wantName, gotName)
			assert.Equal(t, tt.wantMatched, gotMatched)

			gotMatchesOnly := MatchesPkgAndName(tt.typ, tt.pkg, tt.names...)
			assert.Equal(t, tt.wantMatched, gotMatchesOnly)
		})
	}
}
