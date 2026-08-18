package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// detectLockAliases maps a variable that holds a lock (`locker = &mu`) to the
// lock itself, so operations made through the alias land on the real counters.
// A variable that receives more than one lock stays unmapped: which lock it
// releases would depend on the path.
func (c *Checker) detectLockAliases(fn *ast.FuncDecl) map[string]string {
	if fn == nil || fn.Body == nil {
		return nil
	}

	targets := make(map[string]map[string]bool)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			if i >= len(assign.Rhs) {
				break
			}
			ident, ok := lhs.(*ast.Ident)
			if !ok || ident.Name == "_" {
				continue
			}
			target := c.aliasedLockTarget(assign.Rhs[i])
			if target == "" || target == ident.Name {
				continue
			}
			if targets[ident.Name] == nil {
				targets[ident.Name] = make(map[string]bool)
			}
			targets[ident.Name][target] = true
		}
		return true
	})

	if len(targets) == 0 {
		return nil
	}

	aliases := make(map[string]string, len(targets))
	for name, candidates := range targets {
		if len(candidates) != 1 {
			continue
		}
		for target := range candidates {
			aliases[name] = target
		}
	}
	return aliases
}

// aliasedLockTarget returns the tracked lock an expression refers to, whether
// it is taken by address or already a pointer.
func (c *Checker) aliasedLockTarget(expr ast.Expr) string {
	expr = common.UnwrapParenExpr(expr)
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		expr = common.UnwrapParenExpr(unary.X)
	}

	name := common.GetVarName(expr)
	if name == "" || name == "?" {
		return ""
	}
	if !c.mutexNames[name] && !c.rwMutexNames[name] {
		return ""
	}
	return name
}

// resolveLockAlias returns the lock a name ultimately refers to.
func (c *Checker) resolveLockAlias(varName string) string {
	if target, ok := c.lockAliases[varName]; ok {
		return target
	}
	return varName
}

// aliasGuardedReleaseFlags maps a lock to the alias that guards its deferred
// release: `defer func() { if locker != nil { locker.Unlock() } }()`. It is
// detectFlagGuardedReleaseFlags with the alias standing in for the boolean, and
// keeps the same requirement that every acquisition records itself.
func (c *Checker) aliasGuardedReleaseFlags(fn *ast.FuncDecl) map[string]string {
	if fn == nil || fn.Body == nil || len(c.lockAliases) == 0 {
		return nil
	}

	var result map[string]string
	for alias, target := range c.lockAliases {
		if !deferredAliasGuardedUnlock(fn.Body, alias) {
			continue
		}
		if !everyLockPairsWithAliasAssign(fn.Body, target, alias) {
			continue
		}
		if result == nil {
			result = make(map[string]string)
		}
		result[target] = alias
	}
	return result
}

// deferredAliasGuardedUnlock reports whether a deferred closure releases the
// lock through the alias, with every release under a single `alias != nil`
// guard.
func deferredAliasGuardedUnlock(body *ast.BlockStmt, alias string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		deferStmt, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		fnlit, ok := deferStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnlit.Body == nil {
			return true
		}

		total := countMutexMethodCalls(fnlit.Body, alias, "Unlock") +
			countMutexMethodCalls(fnlit.Body, alias, "RUnlock")
		if total == 0 {
			return true
		}

		for _, inner := range fnlit.Body.List {
			ifStmt, ok := inner.(*ast.IfStmt)
			if !ok || ifStmt.Init != nil || !isNotNilCheck(ifStmt.Cond, alias) {
				continue
			}
			guarded := countMutexMethodCalls(ifStmt.Body, alias, "Unlock") +
				countMutexMethodCalls(ifStmt.Body, alias, "RUnlock")
			if guarded == total {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// isNotNilCheck reports whether cond is `name != nil`.
func isNotNilCheck(cond ast.Expr, name string) bool {
	binary, ok := common.UnwrapParenExpr(cond).(*ast.BinaryExpr)
	if !ok || binary.Op != token.NEQ {
		return false
	}
	ident, ok := common.UnwrapParenExpr(binary.X).(*ast.Ident)
	if !ok || ident.Name != name {
		return false
	}
	nilIdent, ok := common.UnwrapParenExpr(binary.Y).(*ast.Ident)
	return ok && nilIdent.Name == "nil"
}

// everyLockPairsWithAliasAssign reports whether every acquisition of the lock
// is immediately followed by the assignment that records it in the alias.
func everyLockPairsWithAliasAssign(body *ast.BlockStmt, target, alias string) bool {
	return everyAcquisitionPairsWith(body,
		func(stmt ast.Stmt) bool {
			return isMutexMethodCallStmt(stmt, target, "Lock") || isMutexMethodCallStmt(stmt, target, "RLock")
		},
		func(stmt ast.Stmt) bool { return isAliasAssignment(stmt, target, alias) },
	)
}

// isAliasAssignment reports whether stmt is `alias = &target` or `alias = target`.
func isAliasAssignment(stmt ast.Stmt, target, alias string) bool {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return false
	}
	ident, ok := assign.Lhs[0].(*ast.Ident)
	if !ok || ident.Name != alias {
		return false
	}

	rhs := common.UnwrapParenExpr(assign.Rhs[0])
	if unary, ok := rhs.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		rhs = common.UnwrapParenExpr(unary.X)
	}
	return common.GetVarName(rhs) == target
}
