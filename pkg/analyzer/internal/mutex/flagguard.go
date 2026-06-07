package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// detectFlagGuardedReleaseFlags maps each mutex/rwmutex name whose lock is
// released by a deferred, flag-guarded unlock to its guard flag's name. The
// pattern is:
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
func (c *Checker) detectFlagGuardedReleaseFlags(fn *ast.FuncDecl) map[string]string {
	if fn == nil || fn.Body == nil {
		return nil
	}

	var result map[string]string
	consider := func(names map[string]bool) {
		for name := range names {
			flag, ok := deferredFlagGuardedUnlock(fn.Body, name, "Unlock")
			if !ok || !everyLockPairsWithSetFlag(fn.Body, name, "Lock", flag) {
				continue
			}
			if result == nil {
				result = make(map[string]string)
			}
			result[name] = flag
		}
	}

	consider(c.mutexNames)
	consider(c.rwMutexNames)
	return result
}

// isFlagGuarded reports whether mutexName's lock is released by a deferred,
// flag-guarded unlock (see detectFlagGuardedReleaseFlags).
func (c *Checker) isFlagGuarded(mutexName string) bool {
	_, ok := c.flagGuardedFlags[mutexName]
	return ok
}

func (c *Checker) unlockIsGuardedByFlag(mutexName string, pos token.Pos) bool {
	flag := c.flagGuardedFlags[mutexName]
	return flag != "" && positionInsideFlagGuard(c.function, pos, flag)
}

func positionInsideFlagGuard(fn *ast.FuncDecl, pos token.Pos, flag string) bool {
	if fn == nil || fn.Body == nil || !pos.IsValid() || flag == "" {
		return false
	}

	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok || ifStmt.Body == nil {
			return true
		}
		if !exprMentionsIdent(ifStmt.Cond, flag) {
			return true
		}
		if ifStmt.Body.Pos() <= pos && pos <= ifStmt.Body.End() {
			found = true
			return false
		}
		return true
	})
	return found
}

func exprMentionsIdent(expr ast.Expr, name string) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		ident, ok := n.(*ast.Ident)
		if ok && ident.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

// deferredFlagGuardedUnlock finds a deferred closure in body whose unlocks of
// mutexName (via unlockMethod) all sit inside a single `if <flag> { ... }`, and
// returns the guard flag name.
func deferredFlagGuardedUnlock(body *ast.BlockStmt, mutexName, unlockMethod string) (string, bool) {
	return deferredFlagGuardedUnlockInStatements(body.List, mutexName, unlockMethod)
}

func deferredFlagGuardedUnlockInStatements(stmts []ast.Stmt, mutexName, unlockMethod string) (string, bool) {
	for _, stmt := range stmts {
		switch s := stmt.(type) {
		case *ast.DeferStmt:
			if flag, ok := flagFromGuardedDefer(s, mutexName, unlockMethod); ok {
				return flag, true
			}
		case *ast.BlockStmt:
			if flag, ok := deferredFlagGuardedUnlockInStatements(s.List, mutexName, unlockMethod); ok {
				return flag, true
			}
		case *ast.IfStmt:
			if flag, ok := deferredFlagGuardedUnlockInStatements(s.Body.List, mutexName, unlockMethod); ok {
				return flag, true
			}
			if flag, ok := deferredFlagGuardedUnlockInElse(s.Else, mutexName, unlockMethod); ok {
				return flag, true
			}
		case *ast.ForStmt:
			if flag, ok := deferredFlagGuardedUnlockInStatements(s.Body.List, mutexName, unlockMethod); ok {
				return flag, true
			}
		case *ast.RangeStmt:
			if flag, ok := deferredFlagGuardedUnlockInStatements(s.Body.List, mutexName, unlockMethod); ok {
				return flag, true
			}
		case *ast.SwitchStmt:
			if flag, ok := deferredFlagGuardedUnlockInCaseClauses(s.Body.List, mutexName, unlockMethod); ok {
				return flag, true
			}
		case *ast.TypeSwitchStmt:
			if flag, ok := deferredFlagGuardedUnlockInCaseClauses(s.Body.List, mutexName, unlockMethod); ok {
				return flag, true
			}
		case *ast.SelectStmt:
			if flag, ok := deferredFlagGuardedUnlockInCommClauses(s.Body.List, mutexName, unlockMethod); ok {
				return flag, true
			}
		case *ast.LabeledStmt:
			if flag, ok := deferredFlagGuardedUnlockInStatements([]ast.Stmt{s.Stmt}, mutexName, unlockMethod); ok {
				return flag, true
			}
		}
	}
	return "", false
}

func deferredFlagGuardedUnlockInElse(stmt ast.Stmt, mutexName, unlockMethod string) (string, bool) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return deferredFlagGuardedUnlockInStatements(s.List, mutexName, unlockMethod)
	case *ast.IfStmt:
		return deferredFlagGuardedUnlockInStatements([]ast.Stmt{s}, mutexName, unlockMethod)
	default:
		return "", false
	}
}

func deferredFlagGuardedUnlockInCaseClauses(stmts []ast.Stmt, mutexName, unlockMethod string) (string, bool) {
	for _, stmt := range stmts {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		if flag, ok := deferredFlagGuardedUnlockInStatements(cc.Body, mutexName, unlockMethod); ok {
			return flag, true
		}
	}
	return "", false
}

func deferredFlagGuardedUnlockInCommClauses(stmts []ast.Stmt, mutexName, unlockMethod string) (string, bool) {
	for _, stmt := range stmts {
		cc, ok := stmt.(*ast.CommClause)
		if !ok {
			continue
		}
		if flag, ok := deferredFlagGuardedUnlockInStatements(cc.Body, mutexName, unlockMethod); ok {
			return flag, true
		}
	}
	return "", false
}

func flagFromGuardedDefer(deferStmt *ast.DeferStmt, mutexName, unlockMethod string) (string, bool) {
	fnlit, ok := deferStmt.Call.Fun.(*ast.FuncLit)
	if !ok || fnlit.Body == nil {
		return "", false
	}

	total := countMutexMethodCalls(fnlit.Body, mutexName, unlockMethod)
	if total == 0 {
		return "", false
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
	var visitCallbackExprs func(ast.Expr)
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
			case *ast.AssignStmt:
				for _, rhs := range s.Rhs {
					visitCallbackExprs(rhs)
				}
			case *ast.ExprStmt:
				visitCallbackExprs(s.X)
			case *ast.ReturnStmt:
				for _, result := range s.Results {
					visitCallbackExprs(result)
				}
			case *ast.IfStmt:
				if s.Init != nil {
					visit([]ast.Stmt{s.Init})
				}
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

	visitCallbackExprs = func(expr ast.Expr) {
		call, ok := expr.(*ast.CallExpr)
		if !ok {
			return
		}
		for _, arg := range call.Args {
			fnlit, ok := arg.(*ast.FuncLit)
			if !ok || fnlit.Body == nil {
				continue
			}
			visit(fnlit.Body.List)
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
