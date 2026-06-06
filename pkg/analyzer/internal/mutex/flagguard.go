package mutex

import (
	"go/ast"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// detectFlagGuardedReleases returns the set of mutex/rwmutex names whose lock is
// released by a deferred, flag-guarded unlock. The pattern is:
//
//	held := false
//	defer func() { if held { mu.Unlock() } }()
//	...
//	mu.Lock()
//	held = true
//
// The deferred closure unlocks exactly when a lock was taken, so each acquisition
// is balanced by the deferred release.
//
// To stay sound, a mutex only qualifies when every Lock is immediately followed
// by `flag = true`: a Lock that does not set the flag is not covered by the
// deferred release and must still be reported.
func (c *Checker) detectFlagGuardedReleases(fn *ast.FuncDecl) map[string]bool {
	if fn == nil || fn.Body == nil {
		return nil
	}

	var result map[string]bool
	consider := func(names map[string]bool) {
		for name := range names {
			flag, ok := deferredFlagGuardedUnlock(fn.Body, name, "Unlock")
			if !ok || !everyLockPairsWithSetFlag(fn.Body, name, "Lock", flag) {
				continue
			}
			if result == nil {
				result = make(map[string]bool)
			}
			result[name] = true
		}
	}

	consider(c.mutexNames)
	consider(c.rwMutexNames)
	return result
}

// deferredFlagGuardedUnlock finds a deferred closure in body whose unlocks of
// mutexName (via unlockMethod) all sit inside a single `if <flag> { ... }`, and
// returns the guard flag name.
func deferredFlagGuardedUnlock(body *ast.BlockStmt, mutexName, unlockMethod string) (string, bool) {
	for _, stmt := range body.List {
		deferStmt, ok := stmt.(*ast.DeferStmt)
		if !ok {
			continue
		}
		fnlit, ok := deferStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnlit.Body == nil {
			continue
		}

		total := countMutexMethodCalls(fnlit.Body, mutexName, unlockMethod)
		if total == 0 {
			continue
		}

		for _, inner := range fnlit.Body.List {
			ifStmt, ok := inner.(*ast.IfStmt)
			if !ok || ifStmt.Init != nil {
				continue
			}
			flagIdent, ok := ifStmt.Cond.(*ast.Ident)
			if !ok {
				continue
			}
			// Every unlock in the closure must be under this single flag guard.
			if countMutexMethodCalls(ifStmt.Body, mutexName, unlockMethod) == total {
				return flagIdent.Name, true
			}
		}
	}
	return "", false
}

// everyLockPairsWithSetFlag reports whether body contains at least one
// `mutexName.lockMethod()` and every such call is immediately followed by
// `flag = true` in the same statement list. Function literals, deferred calls and
// goroutines run in a different frame and are not traversed.
func everyLockPairsWithSetFlag(body *ast.BlockStmt, mutexName, lockMethod, flag string) bool {
	foundLock := false
	paired := true

	var visit func(stmts []ast.Stmt)
	visit = func(stmts []ast.Stmt) {
		for i, stmt := range stmts {
			if isMutexMethodCallStmt(stmt, mutexName, lockMethod) {
				foundLock = true
				if i+1 >= len(stmts) || !isAssignTrue(stmts[i+1], flag) {
					paired = false
				}
				continue
			}

			switch s := stmt.(type) {
			case *ast.BlockStmt:
				visit(s.List)
			case *ast.IfStmt:
				if s.Body != nil {
					visit(s.Body.List)
				}
				if s.Else != nil {
					visit([]ast.Stmt{s.Else})
				}
			case *ast.ForStmt:
				if s.Body != nil {
					visit(s.Body.List)
				}
			case *ast.RangeStmt:
				if s.Body != nil {
					visit(s.Body.List)
				}
			case *ast.SwitchStmt:
				if s.Body != nil {
					visit(s.Body.List)
				}
			case *ast.TypeSwitchStmt:
				if s.Body != nil {
					visit(s.Body.List)
				}
			case *ast.SelectStmt:
				if s.Body != nil {
					visit(s.Body.List)
				}
			case *ast.CaseClause:
				visit(s.Body)
			case *ast.CommClause:
				visit(s.Body)
			case *ast.LabeledStmt:
				visit([]ast.Stmt{s.Stmt})
			}
		}
	}

	visit(body.List)
	return foundLock && paired
}

func countMutexMethodCalls(block *ast.BlockStmt, mutexName, method string) int {
	count := 0
	ast.Inspect(block, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok &&
			sel.Sel.Name == method && common.GetVarName(sel.X) == mutexName {
			count++
		}
		return true
	})
	return count
}

func isMutexMethodCallStmt(stmt ast.Stmt, mutexName, method string) bool {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := exprStmt.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == method && common.GetVarName(sel.X) == mutexName
}

// isAssignTrue reports whether stmt assigns the boolean literal true to flag.
func isAssignTrue(stmt ast.Stmt, flag string) bool {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return false
	}
	lhs, ok := assign.Lhs[0].(*ast.Ident)
	if !ok || lhs.Name != flag {
		return false
	}
	rhs, ok := assign.Rhs[0].(*ast.Ident)
	return ok && rhs.Name == "true"
}
