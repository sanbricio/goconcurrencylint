package waitgroup

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"golang.org/x/tools/go/ast/astutil"
)

func (b *balanceValidator) checkWaitWithoutAdd(stats map[string]*Stats) {
	for wgName, st := range stats {
		if !b.localWaitGroupNames[wgName] || strings.Contains(wgName, ".") ||
			len(st.addCalls) > 0 || len(st.goCalls) > 0 || b.waitGroupInitializedFromAnother(wgName) {
			continue
		}
		for _, waitPos := range st.waitCalls {
			targetObj := b.waitGroupReceiverObjectAt(wgName, "Wait", waitPos)
			// The Add may live in a helper function the WaitGroup is passed to,
			// or in a closure assigned to a local variable that is invoked later.
			// Both checks must consider only references that appear before the
			// Wait, since later code cannot supply the missing Add.
			if targetObj != nil &&
				(b.isWaitGroupPassedToOtherFunctionsForWait(targetObj, waitPos) ||
					b.hasAddInLocalClosure(targetObj, waitPos)) {
				continue
			}
			b.reporter.AddError(waitPos, category.WaitWithoutAdd, "waitgroup '"+wgName+"' Wait called without any Add")
		}
	}
}

// hasAddInLocalClosure reports whether a WaitGroup has Add called inside a
// function literal assigned to a local variable. This is intentionally
// permissive: it does not prove the closure is invoked. Only closures whose
// definition appears before waitPos are considered, since a closure defined
// later cannot have run before the Wait.
func (b *balanceValidator) hasAddInLocalClosure(target types.Object, waitPos token.Pos) bool {
	if b.function == nil || b.function.Body == nil || target == nil {
		return false
	}

	found := false
	funcLitDepth := 0
	astutil.Apply(b.function.Body, func(c *astutil.Cursor) bool {
		if found {
			return false
		}
		node := c.Node()
		if node == nil {
			return true
		}
		if fnLit, ok := node.(*ast.FuncLit); ok {
			if fnLit.Pos() >= waitPos {
				return false
			}
			funcLitDepth++
			return true
		}
		call, ok := node.(*ast.CallExpr)
		if !ok || funcLitDepth == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Add" {
			return true
		}
		if b.exprReferencesObject(sel.X, target) {
			found = true
			return false
		}
		return true
	}, func(c *astutil.Cursor) bool {
		if _, ok := c.Node().(*ast.FuncLit); ok {
			funcLitDepth--
		}
		return true
	})
	return found
}

// isWaitGroupPassedToOtherFunctionsForWait reports whether the WaitGroup
// referred to by target is referenced (passed, assigned, returned, etc.)
// somewhere in the enclosing function before waitPos. References after the
// Wait cannot supply its missing Add.
func (b *balanceValidator) isWaitGroupPassedToOtherFunctionsForWait(target types.Object, waitPos token.Pos) bool {
	if b.function == nil || b.function.Body == nil || target == nil {
		return false
	}

	found := false
	ast.Inspect(b.function.Body, func(n ast.Node) bool {
		if found || n == nil || n.Pos() >= waitPos {
			return false
		}
		switch node := n.(type) {
		case *ast.CallExpr:
			for _, arg := range node.Args {
				if b.exprReferencesObject(arg, target) {
					found = true
					return false
				}
			}
		case *ast.AssignStmt:
			for _, rhs := range node.Rhs {
				if b.exprReferencesObject(rhs, target) {
					found = true
					return false
				}
			}
		case *ast.ValueSpec:
			for _, value := range node.Values {
				if b.exprReferencesObject(value, target) {
					found = true
					return false
				}
			}
		case *ast.ReturnStmt:
			for _, result := range node.Results {
				if b.exprReferencesObject(result, target) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func (b *balanceValidator) waitGroupReceiverObjectAt(wgName, method string, pos token.Pos) types.Object {
	if b.function == nil || b.function.Body == nil {
		return nil
	}
	var obj types.Object
	ast.Inspect(b.function.Body, func(n ast.Node) bool {
		if obj != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Pos() != pos {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != method || common.GetVarName(sel.X) != wgName {
			return true
		}
		obj = b.receiverObject(sel.X)
		return false
	})
	return obj
}

func (b *balanceValidator) exprReferencesObject(expr ast.Expr, target types.Object) bool {
	if expr == nil || target == nil {
		return false
	}
	if obj := b.receiverObject(expr); obj != nil {
		return obj == target
	}

	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		exprNode, ok := n.(ast.Expr)
		if !ok {
			return true
		}
		if obj := b.receiverObject(exprNode); obj != nil && obj == target {
			found = true
			return false
		}
		return true
	})
	return found
}

func (b *balanceValidator) receiverObject(expr ast.Expr) types.Object {
	switch e := expr.(type) {
	case *ast.Ident:
		if b.typesInfo == nil {
			return nil
		}
		return b.typesInfo.ObjectOf(e)
	case *ast.ParenExpr:
		return b.receiverObject(e.X)
	case *ast.UnaryExpr:
		if e.Op == token.AND || e.Op == token.MUL {
			return b.receiverObject(e.X)
		}
	}
	return nil
}

func (b *balanceValidator) waitGroupInitializedFromAnother(wgName string) bool {
	found := false
	ast.Inspect(b.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name != wgName || i >= len(node.Rhs) {
					continue
				}
				if b.isWaitGroupAliasedOrCopiedExpr(node.Rhs[i]) {
					found = true
					return false
				}
			}
		case *ast.ValueSpec:
			for i, name := range node.Names {
				if name.Name != wgName || i >= len(node.Values) {
					continue
				}
				if b.isWaitGroupAliasedOrCopiedExpr(node.Values[i]) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// isWaitGroupAliasedOrCopiedExpr reports whether expr initializes a local
// WaitGroup handle from another WaitGroup, either by value or by address.
func (b *balanceValidator) isWaitGroupAliasedOrCopiedExpr(expr ast.Expr) bool {
	if expr == nil {
		return false
	}
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return false
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return b.isWaitGroupFieldExpr(e.X)
		}
		return b.isWaitGroupAliasedOrCopiedExpr(e.X)
	}
	return common.IsWaitGroup(b.typesInfo.TypeOf(expr))
}

func (b *balanceValidator) isWaitGroupFieldExpr(expr ast.Expr) bool {
	_, ok := expr.(*ast.SelectorExpr)
	return ok && common.IsWaitGroup(b.typesInfo.TypeOf(expr))
}

// checkUnreachableDone checks for Done calls that are unreachable due to early returns
func (b *balanceValidator) checkUnreachableDone() {
	for wgName := range b.waitGroupNames {
		ast.Inspect(b.function.Body, func(n ast.Node) bool {
			goStmt, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}

			if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
				if b.hasUnreachableDone(fnLit.Body, wgName) {
					addPos := b.findRelatedAddCall(goStmt, wgName)
					if addPos != token.NoPos {
						b.reporter.AddError(addPos, category.AddWithoutDone,
							"waitgroup '"+wgName+"' has Add without corresponding Done")
					}
				}
			}

			return true
		})
	}
}
