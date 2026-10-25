package mutex

import (
	"go/ast"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// Includes generated files so functionIsCallerManagedReleaseFor sees every
// call site of the current method.
func collectFunctionDecls(files []*ast.File) []*ast.FuncDecl {
	var functions []*ast.FuncDecl

	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			functions = append(functions, fn)
		}
	}

	return functions
}

func functionBodyContainsFieldCall(body *ast.BlockStmt, varName string, methodNames []string) bool {
	if body == nil {
		return false
	}

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		if containsMethod(methodNames, sel.Sel.Name) && common.GetVarName(sel.X) == varName {
			found = true
			return false
		}

		return true
	})

	return found
}

// bodyContainsAnyMethodCall reports whether body calls one of methodNames on
// any receiver.
func bodyContainsAnyMethodCall(body *ast.BlockStmt, methodNames []string) bool {
	if body == nil {
		return false
	}

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && containsMethod(methodNames, sel.Sel.Name) {
			found = true
			return false
		}

		return true
	})

	return found
}

func containsMethod(methodNames []string, methodName string) bool {
	return slices.Contains(methodNames, methodName)
}
