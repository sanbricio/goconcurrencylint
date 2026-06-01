package waitgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

func buildEscapeAnalyzer(t *testing.T, src string, isLocalChannel localChannelChecker) *escapeAnalyzer {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "escape_test.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if candidate, ok := decl.(*ast.FuncDecl); ok && candidate.Name.Name == "f" {
			fn = candidate
			break
		}
	}
	if fn == nil {
		t.Fatal("function f not found")
	}

	return newEscapeAnalyzer(
		fn,
		func(*ast.CallExpr, string) (*ast.FuncDecl, string, bool) {
			return nil, "", false
		},
		func(*ast.FuncDecl, string, map[token.Pos]bool) bool {
			return false
		},
		testDoneCallAnalyzer,
		isLocalChannel,
		func(ast.Expr) *ast.FuncDecl {
			return nil
		},
	)
}

func testDoneCallAnalyzer(block *ast.BlockStmt, wgName string, _ map[token.Pos]bool) doneCallInfo {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Done" && common.GetVarName(sel.X) == wgName {
			found = true
			return false
		}
		return true
	})
	return doneCallInfo{hasAnyDone: found, hasGuaranteedDone: found}
}

func TestEscapeAnalyzer_ReturnedWaitGroupEscapes(t *testing.T) {
	e := buildEscapeAnalyzer(t, `package p
func f(wg any) any {
	return wg
}`, nil)

	if !e.isWaitGroupPassedToOtherFunctions("wg") {
		t.Fatal("expected returned WaitGroup reference to escape")
	}
}

func TestEscapeAnalyzer_CallbackIdentifierEscapes(t *testing.T) {
	e := buildEscapeAnalyzer(t, `package p
func f() {
	callback := func() {
		wg.Done()
	}
	registerCallback(callback)
}`, nil)

	if !e.isWaitGroupPassedToOtherFunctions("wg") {
		t.Fatal("expected callback variable containing Done to escape")
	}
}

func TestEscapeAnalyzer_LocalChannelSendDoesNotEscape(t *testing.T) {
	e := buildEscapeAnalyzer(t, `package p
func f() {
	ch := make(chan func())
	ch <- wg.Done
}`, func(chanName string) bool {
		return chanName == "ch"
	})

	if e.isWaitGroupPassedToOtherFunctions("wg") {
		t.Fatal("expected send to locally-created channel to stay local")
	}
}

func TestEscapeAnalyzer_ExternalChannelSendEscapes(t *testing.T) {
	e := buildEscapeAnalyzer(t, `package p
func f(ch chan func()) {
	ch <- wg.Done
}`, func(string) bool {
		return false
	})

	if !e.isWaitGroupPassedToOtherFunctions("wg") {
		t.Fatal("expected send to external channel to escape")
	}
}
