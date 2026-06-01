package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// terminationAnalyzer decides whether a statement or block always terminates
// the current flow (return, break/continue/goto/fallthrough, panic, os.Exit,
// runtime.Goexit, log/testing Fatal*).
type terminationAnalyzer struct {
	typesInfo *types.Info
}

// newTerminationAnalyzer creates a terminationAnalyzer backed by the given
// type information. typesInfo may be nil; the analyzer degrades gracefully.
func newTerminationAnalyzer(typesInfo *types.Info) *terminationAnalyzer {
	return &terminationAnalyzer{typesInfo: typesInfo}
}

func (t *terminationAnalyzer) blockContainsReturn(block *ast.BlockStmt) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if _, ok := n.(*ast.ReturnStmt); ok {
			found = true
			return false
		}
		return true
	})
	return found
}

func (t *terminationAnalyzer) blockContainsBreak(block *ast.BlockStmt) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		branch, ok := n.(*ast.BranchStmt)
		if ok && branch.Tok == token.BREAK {
			found = true
			return false
		}
		return true
	})
	return found
}

func (t *terminationAnalyzer) blockAlwaysTerminates(block *ast.BlockStmt) bool {
	if block == nil {
		return false
	}

	return slices.ContainsFunc(block.List, t.statementAlwaysTerminates)
}

func (t *terminationAnalyzer) statementAlwaysTerminates(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		return branchTerminatesBlock(s.Tok)
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		return ok && t.callTerminatesExecution(call)
	case *ast.IfStmt:
		if s.Else == nil || !t.blockAlwaysTerminates(s.Body) {
			return false
		}
		return t.elseAlwaysTerminates(s.Else)
	default:
		return false
	}
}

// elseAlwaysTerminates reports whether every path through elseNode terminates.
func (t *terminationAnalyzer) elseAlwaysTerminates(elseNode ast.Stmt) bool {
	switch e := elseNode.(type) {
	case *ast.BlockStmt:
		return t.blockAlwaysTerminates(e)
	case *ast.IfStmt:
		return t.blockAlwaysTerminates(&ast.BlockStmt{List: []ast.Stmt{e}})
	default:
		return false
	}
}

func (t *terminationAnalyzer) callTerminatesExecution(call *ast.CallExpr) bool {
	if call == nil {
		return false
	}
	if ident, ok := call.Fun.(*ast.Ident); ok {
		return t.isBuiltinPanic(ident)
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	methodName := sel.Sel.Name
	if t.typesInfo != nil {
		if obj := t.typesInfo.ObjectOf(sel.Sel); obj != nil && obj.Pkg() != nil {
			switch obj.Pkg().Path() {
			case "os":
				return methodName == "Exit"
			case "runtime":
				return methodName == "Goexit"
			case "log", "testing":
				return isFatalMethod(methodName)
			}
		}
	}

	receiverName := common.GetVarName(sel.X)
	switch receiverName {
	case "os":
		return methodName == "Exit"
	case "runtime":
		return methodName == "Goexit"
	case "log":
		return isFatalMethod(methodName)
	default:
		return false
	}
}

func (t *terminationAnalyzer) isBuiltinPanic(ident *ast.Ident) bool {
	if ident == nil || ident.Name != "panic" {
		return false
	}
	if t.typesInfo == nil {
		return true
	}
	obj := t.typesInfo.ObjectOf(ident)
	if obj == nil {
		return true
	}
	_, ok := obj.(*types.Builtin)
	return ok
}

// branchTerminatesBlock reports whether the given branch token always
// terminates the current block.
func branchTerminatesBlock(tok token.Token) bool {
	return tok == token.BREAK ||
		tok == token.CONTINUE ||
		tok == token.GOTO ||
		tok == token.FALLTHROUGH
}

// isFatalMethod reports whether methodName is one of the Fatal* methods
// found in the log and testing packages.
func isFatalMethod(methodName string) bool {
	return methodName == "Fatal" || methodName == "Fatalf" || methodName == "Fatalln"
}
