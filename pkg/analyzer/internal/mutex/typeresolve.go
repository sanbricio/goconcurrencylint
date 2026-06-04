package mutex

import (
	"go/ast"
	"go/types"
	"slices"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// Indexes generated files too so sibling-method lookups (e.g. wrapper
// Lock/Unlock pairs split across the boundary) resolve.
func buildReceiverMethodMap(files []*ast.File) map[string]map[string]*ast.FuncDecl {
	methods := make(map[string]map[string]*ast.FuncDecl)

	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv == nil {
				continue
			}

			receiverType := receiverTypeName(fn)
			if receiverType == "" {
				continue
			}

			if methods[receiverType] == nil {
				methods[receiverType] = make(map[string]*ast.FuncDecl)
			}
			methods[receiverType][fn.Name.Name] = fn
		}
	}

	return methods
}

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

func receiverTypeName(fn *ast.FuncDecl) string {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}

	return baseTypeName(fn.Recv.List[0].Type)
}

func baseTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return baseTypeName(e.X)
	case *ast.IndexExpr:
		return baseTypeName(e.X)
	case *ast.IndexListExpr:
		return baseTypeName(e.X)
	case *ast.SelectorExpr:
		return e.Sel.Name
	default:
		return ""
	}
}

func baseTypeNameFromType(typ types.Type) string {
	if typ == nil {
		return ""
	}

	switch t := types.Unalias(typ).(type) {
	case *types.Pointer:
		return baseTypeNameFromType(t.Elem())
	case *types.Named:
		if obj := t.Obj(); obj != nil {
			return obj.Name()
		}
	}

	return ""
}

func methodNameMatchesAnyHint(fnName string, hints []string) bool {
	lowerName := strings.ToLower(fnName)
	for _, hint := range hints {
		if hint == "" {
			continue
		}
		lowerHint := strings.ToLower(hint)
		if strings.Contains(lowerName, lowerHint) {
			return true
		}
		if lowerHint == "rlock" && strings.Contains(lowerName, "readlock") {
			return true
		}
		if lowerHint == "runlock" && strings.Contains(lowerName, "readunlock") {
			return true
		}
	}
	return false
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

func containsMethod(methodNames []string, methodName string) bool {
	return slices.Contains(methodNames, methodName)
}
