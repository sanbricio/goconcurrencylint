package waitgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

func TestIsBuiltinPanicDistinguishesShadowedIdentifier(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "shadow.go", `package p
func f() {
	panic("builtin")
	panic := func(interface{}) {}
	panic("shadowed")
}
`, 0)
	if err != nil {
		t.Fatal(err)
	}

	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Defs:  map[*ast.Ident]types.Object{},
		Uses:  map[*ast.Ident]types.Object{},
	}
	if _, err := new(types.Config).Check("p", fset, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}

	var calls []*ast.Ident
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if ok && ident.Name == "panic" {
			calls = append(calls, ident)
		}
		return true
	})
	if len(calls) != 2 {
		t.Fatalf("found %d panic calls, want 2", len(calls))
	}

	analyzer := &Checker{typesInfo: info}
	if !analyzer.isBuiltinPanic(calls[0]) {
		t.Fatal("predeclared panic was not recognized as builtin")
	}
	if analyzer.isBuiltinPanic(calls[1]) {
		t.Fatal("shadowed panic was recognized as builtin")
	}
}

func TestIsBuiltinPanicFallbacks(t *testing.T) {
	t.Run("without types info", func(t *testing.T) {
		analyzer := &Checker{}
		if !analyzer.isBuiltinPanic(ast.NewIdent("panic")) {
			t.Fatal("panic should be treated as builtin when type info is unavailable")
		}
	})

	t.Run("missing object", func(t *testing.T) {
		analyzer := &Checker{typesInfo: &types.Info{Uses: map[*ast.Ident]types.Object{}}}
		if !analyzer.isBuiltinPanic(ast.NewIdent("panic")) {
			t.Fatal("panic should be treated as builtin when no object is recorded")
		}
	})

	t.Run("different name", func(t *testing.T) {
		analyzer := &Checker{}
		if analyzer.isBuiltinPanic(ast.NewIdent("recover")) {
			t.Fatal("recover should not be treated as panic")
		}
	})
}

func TestCallAbortsWorkerRuntimeGoexitFallback(t *testing.T) {
	t.Run("unaliased runtime without types info", func(t *testing.T) {
		call := parseCallExpr(t, "runtime.Goexit()")
		analyzer := &Checker{}
		if !analyzer.callAbortsWorker(call) {
			t.Fatal("runtime.Goexit should abort the worker when type info is unavailable")
		}
	})

	t.Run("alias without types info", func(t *testing.T) {
		call := parseCallExpr(t, "rt.Goexit()")
		analyzer := &Checker{}
		if analyzer.callAbortsWorker(call) {
			t.Fatal("rt.Goexit should require type info to be recognized")
		}
	})
}

func parseCallExpr(t *testing.T, src string) *ast.CallExpr {
	t.Helper()

	expr, err := parser.ParseExpr(src)
	if err != nil {
		t.Fatal(err)
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		t.Fatalf("%q parsed as %T, want *ast.CallExpr", src, expr)
	}
	return call
}
