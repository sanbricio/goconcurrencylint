package mutex

import (
	"go/ast"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
)

// recoverGuardInspector performs static, comment-aware inspection of a block to
// decide what a deferred function literal does with a mutex: whether it contains
// lock/unlock calls and whether every unlock is guarded by a recover() check.
// It depends only on the comment filter, so it can be built and exercised
// without a full Checker.
type recoverGuardInspector struct {
	commentFilter *commentfilter.CommentFilter
}

func newRecoverGuardInspector(commentFilter *commentfilter.CommentFilter) *recoverGuardInspector {
	return &recoverGuardInspector{commentFilter: commentFilter}
}

// unlocksOnlyInRecoverGuard reports whether the block contains at least one
// target unlock and every target unlock is guarded by a recover() check.
func (g *recoverGuardInspector) unlocksOnlyInRecoverGuard(block *ast.BlockStmt, mutexName, methodName string) bool {
	foundUnlock := false
	foundUnguardedUnlock := false

	var (
		visitStmt  func(ast.Stmt, bool)
		visitBlock func(*ast.BlockStmt, bool)
		visitExpr  func(ast.Expr, bool)
	)

	visitExpr = func(expr ast.Expr, recoverGuarded bool) {
		if expr == nil || foundUnguardedUnlock {
			return
		}
		ast.Inspect(expr, func(n ast.Node) bool {
			if foundUnguardedUnlock {
				return false
			}
			if fnlit, ok := n.(*ast.FuncLit); ok && fnlit != expr {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || g.commentFilter.ShouldSkipCall(call) {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != methodName || common.GetVarName(sel.X) != mutexName {
				return true
			}
			foundUnlock = true
			if !recoverGuarded {
				foundUnguardedUnlock = true
			}
			return false
		})
	}

	visitBlock = func(block *ast.BlockStmt, recoverGuarded bool) {
		if block == nil || foundUnguardedUnlock {
			return
		}
		for _, stmt := range block.List {
			visitStmt(stmt, recoverGuarded)
			if foundUnguardedUnlock {
				return
			}
		}
	}

	visitStmt = func(stmt ast.Stmt, recoverGuarded bool) {
		if stmt == nil || foundUnguardedUnlock {
			return
		}
		switch s := stmt.(type) {
		case *ast.BlockStmt:
			visitBlock(s, recoverGuarded)
		case *ast.LabeledStmt:
			visitStmt(s.Stmt, recoverGuarded)
		case *ast.IfStmt:
			bodyGuarded := recoverGuarded || containsRecoverCall(s.Init) || containsRecoverCall(s.Cond)
			visitBlock(s.Body, bodyGuarded)
			if s.Else != nil {
				visitStmt(s.Else, recoverGuarded)
			}
		case *ast.ForStmt:
			visitStmt(s.Init, recoverGuarded)
			visitExpr(s.Cond, recoverGuarded)
			visitStmt(s.Post, recoverGuarded)
			visitBlock(s.Body, recoverGuarded)
		case *ast.RangeStmt:
			visitExpr(s.X, recoverGuarded)
			visitBlock(s.Body, recoverGuarded)
		case *ast.SwitchStmt:
			visitStmt(s.Init, recoverGuarded)
			visitExpr(s.Tag, recoverGuarded)
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					visitBlock(&ast.BlockStmt{List: cc.Body}, recoverGuarded)
				}
			}
		case *ast.TypeSwitchStmt:
			visitStmt(s.Init, recoverGuarded)
			visitStmt(s.Assign, recoverGuarded)
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					visitBlock(&ast.BlockStmt{List: cc.Body}, recoverGuarded)
				}
			}
		case *ast.SelectStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CommClause); ok {
					visitStmt(cc.Comm, recoverGuarded)
					visitBlock(&ast.BlockStmt{List: cc.Body}, recoverGuarded)
				}
			}
		case *ast.ExprStmt:
			visitExpr(s.X, recoverGuarded)
		case *ast.DeferStmt:
			visitExpr(s.Call, recoverGuarded)
		case *ast.GoStmt:
			visitExpr(s.Call, recoverGuarded)
		case *ast.AssignStmt:
			for _, expr := range s.Rhs {
				visitExpr(expr, recoverGuarded)
			}
		case *ast.DeclStmt:
			if gen, ok := s.Decl.(*ast.GenDecl); ok {
				for _, spec := range gen.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, expr := range vs.Values {
							visitExpr(expr, recoverGuarded)
						}
					}
				}
			}
		case *ast.ReturnStmt:
			for _, expr := range s.Results {
				visitExpr(expr, recoverGuarded)
			}
		}
	}

	visitBlock(block, false)
	return foundUnlock && !foundUnguardedUnlock
}

// containsRecoverCall reports whether node contains a recover() call, ignoring
// nested function literals that execute in a different frame.
func containsRecoverCall(node ast.Node) bool {
	if node == nil {
		return false
	}
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		if fnlit, ok := n.(*ast.FuncLit); ok && fnlit != node {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if ok && ident.Name == "recover" {
			found = true
			return false
		}
		return true
	})
	return found
}

// containsUnlock checks if a block contains an unlock call for a specific mutex
func (g *recoverGuardInspector) containsUnlock(block *ast.BlockStmt, mutexName string) bool {
	return g.containsMutexMethodCall(block, mutexName, "Unlock")
}

// containsLock checks if a block contains a lock call for a specific mutex
func (g *recoverGuardInspector) containsLock(block *ast.BlockStmt, mutexName string) bool {
	return g.containsMutexMethodCall(block, mutexName, "Lock")
}

// containsRUnlock checks if a block contains an runlock call for a specific rwmutex
func (g *recoverGuardInspector) containsRUnlock(block *ast.BlockStmt, mutexName string) bool {
	return g.containsMutexMethodCall(block, mutexName, "RUnlock")
}

// containsRLock checks if a block contains an rlock call for a specific rwmutex
func (g *recoverGuardInspector) containsRLock(block *ast.BlockStmt, mutexName string) bool {
	return g.containsMutexMethodCall(block, mutexName, "RLock")
}

// containsMutexMethodCall checks if a block contains a call to a specific
// method on the given mutex variable.
func (g *recoverGuardInspector) containsMutexMethodCall(block *ast.BlockStmt, mutexName, method string) bool {
	var found bool
	ast.Inspect(block, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || g.commentFilter.ShouldSkipCall(call) {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		if sel.Sel.Name == method &&
			common.GetVarName(sel.X) == mutexName {
			found = true
		}

		return !found
	})
	return found
}
