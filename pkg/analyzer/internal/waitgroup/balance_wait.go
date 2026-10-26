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
			len(st.addCalls) > 0 || len(st.goCalls) > 0 || !b.waitGroupStartsAtZero(wgName) {
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
					b.hasAddInLocalClosure(targetObj, waitPos) ||
					b.isRangeVariable(targetObj)) {
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

// waitGroupStartsAtZero reports whether every assignment that gives the local
// name wgName a group gives it a fresh one: `var wg sync.WaitGroup`,
// `sync.WaitGroup{}`, `&sync.WaitGroup{}` or `new(sync.WaitGroup)`. Only such a
// group has a counter this function can account for; one that comes out of a
// call (alone or among other results), a map, a field or another group carries
// whatever its producer left on it.
func (b *balanceValidator) waitGroupStartsAtZero(wgName string) bool {
	defined, fresh := false, true
	record := func(expr ast.Expr) {
		defined = true
		if !b.isFreshWaitGroupExpr(expr) {
			fresh = false
		}
	}
	ast.Inspect(b.function.Body, func(n ast.Node) bool {
		if !fresh {
			return false
		}
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name != wgName {
					continue
				}
				if len(node.Lhs) != len(node.Rhs) {
					record(nil)
					continue
				}
				record(node.Rhs[i])
			}
		case *ast.ValueSpec:
			for i, name := range node.Names {
				if name.Name != wgName {
					continue
				}
				switch {
				case len(node.Values) == 0:
					// The zero value of the group itself; a nil pointer is no group.
					defined = true
					if obj := b.typesInfo.ObjectOf(name); obj == nil || !isWaitGroupValueType(obj.Type()) {
						fresh = false
					}
				case len(node.Values) != len(node.Names):
					record(nil)
				default:
					record(node.Values[i])
				}
			}
		}
		return true
	})
	return defined && fresh
}

// isFreshWaitGroupExpr reports whether expr builds a new, zero group.
func (b *balanceValidator) isFreshWaitGroupExpr(expr ast.Expr) bool {
	switch e := common.UnwrapParenExpr(expr).(type) {
	case *ast.CompositeLit:
		return isWaitGroupValueType(b.typesInfo.TypeOf(e))
	case *ast.UnaryExpr:
		lit, ok := common.UnwrapParenExpr(e.X).(*ast.CompositeLit)
		return e.Op == token.AND && ok && isWaitGroupValueType(b.typesInfo.TypeOf(lit))
	case *ast.CallExpr:
		ident, ok := common.UnwrapParenExpr(e.Fun).(*ast.Ident)
		if !ok || len(e.Args) != 1 {
			return false
		}
		if builtin, ok := b.typesInfo.ObjectOf(ident).(*types.Builtin); !ok || builtin.Name() != "new" {
			return false
		}
		return isWaitGroupValueType(b.typesInfo.TypeOf(e.Args[0]))
	}
	return false
}

func isWaitGroupValueType(typ types.Type) bool {
	if typ == nil {
		return false
	}
	if _, isPointer := typ.Underlying().(*types.Pointer); isPointer {
		return false
	}
	return common.IsWaitGroup(typ)
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

// isRangeVariable reports whether target is a range key or value.
// Ranged WaitGroups are owned by the collection's producer.
func (b *balanceValidator) isRangeVariable(target types.Object) bool {
	if b.function == nil || b.function.Body == nil || target == nil || b.typesInfo == nil {
		return false
	}

	found := false
	ast.Inspect(b.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		rangeStmt, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		for _, expr := range []ast.Expr{rangeStmt.Key, rangeStmt.Value} {
			ident, ok := expr.(*ast.Ident)
			if !ok {
				continue
			}
			if b.typesInfo.ObjectOf(ident) == target {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
