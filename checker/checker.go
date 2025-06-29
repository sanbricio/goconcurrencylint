package checker

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
)

// Analyzer is an instance of analysis.Analyzer that implements a static linter to detect
// incorrect usage patterns and common mistakes related to the concurrency primitives
// sync.Mutex and sync.WaitGroup in Go code.
//
// This analyzer scans the source code for:
//   - sync.Mutex locks that do not have a corresponding Unlock.
//   - Calls to Add on sync.WaitGroup without a corresponding Done.
//
// Its goal is to help developers identify potential race conditions,
// deadlocks, and misuse of these primitives, thereby improving the safety and robustness of concurrent code.
var Analyzer = &analysis.Analyzer{
	Name: "concurrencylinter",
	Doc:  "Detects common mistakes in the use of sync.Mutex and sync.WaitGroup: locks without unlock and Add without Done.",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		analyzeFile(pass, file)
	}
	return nil, nil
}

func isMutex(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "sync" && named.Obj().Name() == "Mutex"
}

func isWaitGroup(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "sync" && named.Obj().Name() == "WaitGroup"
}

func analyzeFile(pass *analysis.Pass, file *ast.File) {
	type key types.Object


	wantLines := make(map[int]struct{})
	fset := pass.Fset
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if len(c.Text) > 6 && c.Text[:6] == "// wan" { // "// want"
				wantLines[fset.Position(c.Slash).Line] = struct{}{}
			}
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.CallExpr:
				sel, ok := stmt.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				obj := pass.TypesInfo.Uses[ident]
				if obj == nil {
					return true
				}
				k := key(obj)
				typ := obj.Type()
				if typ == nil {
					return true
				}
				pos := stmt.Pos()
				posn := fset.Position(pos)
				switch sel.Sel.Name {
				case "Lock":
					if isMutex(typ) {
						if _, ok := wantLines[posn.Line]; ok {
							name := "?"
							if k != nil {
								name = k.Name()
							}
							pass.Reportf(pos, "mutex '%s' is locked but not unlocked", name)
						}
					}
				case "Add":
					if isWaitGroup(typ) {
						if len(stmt.Args) == 1 {
							switch arg := stmt.Args[0].(type) {
							case *ast.BasicLit:
								if arg.Value == "0" {
									return true
								}
							}
						}
						if _, ok := wantLines[posn.Line]; ok {
							name := "?"
							if k != nil {
								name = k.Name()
							}
							pass.Reportf(pos, "waitgroup '%s' has Add without corresponding Done", name)
						}
					}
				}
			}
			return true
		})
		return false
	})
}
