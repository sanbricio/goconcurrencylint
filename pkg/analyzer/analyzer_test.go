package analyzer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestMutexAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "mutex")
}

func TestWaitGroupAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "waitgroup")
}

func TestGetVariableTypeNilCases(t *testing.T) {
	vs := &ast.ValueSpec{
		Names:  []*ast.Ident{{Name: "test"}},
		Type:   nil,
		Values: nil,
	}

	pass := &analysis.Pass{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
		},
	}

	result := getVariableType(vs, pass)
	assert.Nil(t, result, "Should return nil when no type info available")
}

func TestGetVariableTypeWithValuesButNoTypeInfo(t *testing.T) {
	unknownExpr := &ast.BasicLit{Value: "123"}

	vs := &ast.ValueSpec{
		Names:  []*ast.Ident{{Name: "test"}},
		Type:   nil,
		Values: []ast.Expr{unknownExpr},
	}

	pass := &analysis.Pass{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
			Defs:  make(map[*ast.Ident]types.Object),
		},
	}

	result := getVariableType(vs, pass)
	assert.Nil(t, result, "Should return nil when TypeOf returns nil for value")
}

func TestNoPrimitivesDetected(t *testing.T) {
	src := `package main

func TestFunc() {
	var x int
	x = 5
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, 0)
	assert.NoError(t, err)

	pass := &analysis.Pass{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
		},
	}

	// Find the test function
	var testFunc *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "TestFunc" {
			testFunc = fn
			break
		}
	}

	primitives := findSyncPrimitives(testFunc, pass)

	assert.False(t, hasMutexes(primitives), "Should not have mutexes")
	assert.False(t, hasWaitGroups(primitives), "Should not have waitgroups")
}
