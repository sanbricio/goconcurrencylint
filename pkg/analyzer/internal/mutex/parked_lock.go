package mutex

import (
	"go/ast"
	"go/token"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A search can take a lock on a candidate and keep the first one that
// qualifies, parking it in a variable declared outside the search:
//
//	var locked *node
//	for _, n := range nodes {
//		if n.TryLock() {
//			if n.ready() { locked = n; break }
//			n.Unlock()
//		}
//	}
//	...
//	locked.Unlock()
//
// The release through the parking variable gives back whatever lock the
// search left there. The analysis follows the lock under the candidate's name,
// which the search may leave in any state, so it cannot pair the two; what it
// can check is the parking itself: every assignment of a lock to the variable
// happens while that lock is held.

// releasesParkedLock reports whether the release at pos goes through a
// variable that only ever receives varName while it is held (or nil).
func (c *Checker) releasesParkedLock(varName string, pos token.Pos, acquireMethods []string) bool {
	if c.function == nil || c.function.Body == nil {
		return false
	}
	parking := ""
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		if parking != "" {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Pos() != pos {
			return true
		}
		if sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr); ok {
			parking = common.GetVarName(sel.X)
		}
		return false
	})
	if parking == "" || parking == varName || c.lockAliases[parking] != varName {
		return false
	}

	parked := 0
	ok := true
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		if !ok {
			return false
		}
		assign, isAssign := n.(*ast.AssignStmt)
		if !isAssign || len(assign.Lhs) != len(assign.Rhs) {
			return true
		}
		for index, lhs := range assign.Lhs {
			ident, isIdent := lhs.(*ast.Ident)
			if !isIdent || ident.Name != parking {
				continue
			}
			rhs := assign.Rhs[index]
			if isNilIdent(common.UnwrapParenExpr(rhs)) {
				continue
			}
			if c.aliasedLockTarget(rhs) != varName || !lockHeldAtStatement(c.function.Body, assign, varName, acquireMethods) {
				ok = false
				return false
			}
			parked++
		}
		return true
	})
	return ok && parked > 0
}

// lockHeldAtStatement reports whether the statements leading to stmt leave
// varName held: the latest operation on it before stmt, in stmt's own list or
// in the lists around it, is an acquisition, or stmt sits in the success arm
// of a TryLock on it with no release in between.
func lockHeldAtStatement(body *ast.BlockStmt, stmt ast.Stmt, varName string, acquireMethods []string) bool {
	path, ok := findStatementPath(body.List, stmt)
	if !ok {
		return false
	}
	for k, frame := range slices.Backward(path) {
		for j := frame.index - 1; j >= 0; j-- {
			if method, direct := directLockEffect(frame.list[j], varName); direct {
				return slices.Contains(acquireMethods, method)
			}
			if statementContainsLockEffect(frame.list[j], varName) {
				return false
			}
		}
		if k == 0 {
			break
		}
		parent, isIf := path[k-1].list[path[k-1].index].(*ast.IfStmt)
		if !isIf || parent.Init != nil || !sameStatementList(frame.list, parent.Body.List) {
			continue
		}
		if call, negated := negatedCall(parent.Cond); call != nil && !negated && len(call.Args) == 0 {
			if sel, isSel := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr); isSel &&
				common.GetVarName(sel.X) == varName && slices.Contains(acquireMethods, sel.Sel.Name) {
				return true
			}
		}
	}
	return false
}
