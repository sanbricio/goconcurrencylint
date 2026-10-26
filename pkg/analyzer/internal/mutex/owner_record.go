package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A type can make its lock reentrant per owner by recording who holds it:
//
//	func (m *manager) lockFor(id int64) {
//		if m.owner.Load() != id {
//			m.mu.Lock()
//			m.owner.Store(id)
//		}
//	}
//
//	func (m *manager) unlockFor(id int64) {
//		if m.owner.Load() == id {
//			m.owner.Store(0)
//			m.mu.Unlock()
//		}
//	}
//
// Each half takes or gives back the lock only on one branch, which on its own
// is the shape of a forgotten release (see isMirrorPairHalf). Here the branch
// is decided by the record the halves keep between them: the acquiring half
// takes the lock only when the record does not name the caller and then makes
// it name the caller; the releasing half gives it back only when the record
// names the caller and then clears it. The lock is held exactly while the
// record is set, so neither half is unbalanced: the lifecycle spans the two.

// ownerGuardedOp is the one lock operation of a method, made under a guard on
// an owner record kept in a field of the receiver.
type ownerGuardedOp struct {
	record  string
	acquire bool
}

// isOwnerRecordHalf reports whether the lock operation under analysis is one
// half of such a pair: its method guards it with the owner record, and a
// sibling method guards the opposite operation on the same lock with the same
// record, the other way round.
func (w *wrapperResolver) isOwnerRecordHalf(varName, methodName string, oppositeMethods []string) bool {
	receiverType := common.ReceiverTypeName(w.function)
	ref, ok := lockRefFor(varName, common.ReceiverName(w.function))
	if receiverType == "" || !ok || !ref.rooted {
		return false
	}
	own, ok := ownerGuardedOpOf(w.function, ref, []string{methodName})
	if !ok {
		return false
	}
	for name, sibling := range w.receiverMethods[receiverType] {
		if name == w.function.Name.Name {
			continue
		}
		other, ok := ownerGuardedOpOf(sibling, ref, oppositeMethods)
		if ok && other.record == own.record && other.acquire != own.acquire {
			return true
		}
	}
	return false
}

// ownerGuardedOpOf finds fn's single operation on the lock, which must be one
// of methods, made directly in the body of an else-less `if` at the top of fn.
// The condition compares the owner record with some value. The acquiring side
// tests that the record differs and stores that value in it; the releasing
// side tests that it matches and stores the zero value.
func ownerGuardedOpOf(fn *ast.FuncDecl, ref lockRef, methods []string) (ownerGuardedOp, bool) {
	if fn == nil || fn.Body == nil {
		return ownerGuardedOp{}, false
	}
	receiver := common.ReceiverName(fn)
	if receiver == "" || countLockOps(fn.Body, ref, receiver) != 1 {
		return ownerGuardedOp{}, false
	}

	for index, stmt := range fn.Body.List {
		guard, ok := stmt.(*ast.IfStmt)
		if !ok || guard.Init != nil || guard.Else != nil || !blockCallsDirectly(guard.Body, ref, receiver, methods) {
			continue
		}
		cond, ok := common.UnwrapParenExpr(guard.Cond).(*ast.BinaryExpr)
		if !ok || (cond.Op != token.EQL && cond.Op != token.NEQ) {
			return ownerGuardedOp{}, false
		}

		locals := ownerRecordLocals(fn.Body.List[:index], receiver)
		record, value, ok := ownerComparison(cond, receiver, locals)
		if !ok {
			return ownerGuardedOp{}, false
		}

		acquire := lockMethodSide(methods[0]) == acquireLockSide
		switch {
		case acquire && cond.Op == token.NEQ && blockStoresRecord(guard.Body, receiver, record, func(v ast.Expr) bool { return exprString(v) == exprString(value) }):
			return ownerGuardedOp{record: record, acquire: true}, true
		case !acquire && cond.Op == token.EQL && blockStoresRecord(guard.Body, receiver, record, isZeroLiteral):
			return ownerGuardedOp{record: record, acquire: false}, true
		}
		return ownerGuardedOp{}, false
	}
	return ownerGuardedOp{}, false
}

// countLockOps counts every lock operation on the lock in body.
func countLockOps(body *ast.BlockStmt, ref lockRef, receiver string) int {
	all := append(append([]string{}, WriteLockPattern.LockMethods...), WriteLockPattern.UnlockMethods...)
	all = append(append(all, ReadLockPattern.LockMethods...), ReadLockPattern.UnlockMethods...)
	count := 0
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && ref.matches(call, receiver, all) {
			count++
		}
		return true
	})
	return count
}

// blockCallsDirectly reports whether a statement of block itself performs one
// of methods on the lock.
func blockCallsDirectly(block *ast.BlockStmt, ref lockRef, receiver string, methods []string) bool {
	for _, stmt := range block.List {
		if expr, ok := stmt.(*ast.ExprStmt); ok {
			if call, ok := expr.X.(*ast.CallExpr); ok && ref.matches(call, receiver, methods) {
				return true
			}
		}
	}
	return false
}

// ownerRecordLocals maps the locals the statements before the guard load from
// a field of the receiver (`id := m.owner.Load()`) to that field.
func ownerRecordLocals(stmts []ast.Stmt, receiver string) map[string]string {
	locals := make(map[string]string)
	for _, stmt := range stmts {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			continue
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			continue
		}
		if record, ok := ownerRecordRead(assign.Rhs[0], receiver); ok {
			locals[ident.Name] = record
		} else {
			delete(locals, ident.Name)
		}
	}
	return locals
}

// ownerComparison splits `record <op> value` (either way round) into the
// record's field and the value it is compared with.
func ownerComparison(cond *ast.BinaryExpr, receiver string, locals map[string]string) (string, ast.Expr, bool) {
	read := func(expr ast.Expr) (string, bool) {
		if ident, ok := common.UnwrapParenExpr(expr).(*ast.Ident); ok {
			record, ok := locals[ident.Name]
			return record, ok
		}
		return ownerRecordRead(expr, receiver)
	}
	if record, ok := read(cond.X); ok {
		return record, cond.Y, true
	}
	if record, ok := read(cond.Y); ok {
		return record, cond.X, true
	}
	return "", nil, false
}

// ownerRecordRead recognizes a read of a receiver field: `m.owner` or
// `m.owner.Load()`.
func ownerRecordRead(expr ast.Expr, receiver string) (string, bool) {
	expr = common.UnwrapParenExpr(expr)
	if call, ok := expr.(*ast.CallExpr); ok && len(call.Args) == 0 {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Load" {
			expr = sel.X
		}
	}
	if _, ok := expr.(*ast.SelectorExpr); !ok {
		return "", false
	}
	return relativeMutexPath(common.GetVarName(expr), receiver)
}

// blockStoresRecord reports whether a statement of block writes a value accepted
// by want into the record: `m.owner = v` or `m.owner.Store(v)`.
func blockStoresRecord(block *ast.BlockStmt, receiver, record string, want func(ast.Expr) bool) bool {
	target := receiver + "." + record
	for _, stmt := range block.List {
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			if len(s.Lhs) == 1 && len(s.Rhs) == 1 && s.Tok == token.ASSIGN && common.GetVarName(s.Lhs[0]) == target && want(s.Rhs[0]) {
				return true
			}
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				continue
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Store" && common.GetVarName(sel.X) == target && want(call.Args[0]) {
				return true
			}
		}
	}
	return false
}

// isZeroLiteral reports whether expr spells a zero value: 0, "", nil or false.
func isZeroLiteral(expr ast.Expr) bool {
	switch e := common.UnwrapParenExpr(expr).(type) {
	case *ast.BasicLit:
		return e.Value == "0" || e.Value == `""`
	case *ast.Ident:
		return e.Name == "nil" || e.Name == "false"
	}
	return false
}
