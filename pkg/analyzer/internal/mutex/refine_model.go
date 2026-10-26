package mutex

import (
	"go/ast"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// Lock modeling that only refined runs use (see refine.go). Each piece makes
// the analysis follow code the main analysis deliberately leaves alone; inside
// a refined run it can only help prove a reported function balanced.

// applyLockOwnershipMove follows a held lock from one variable to another. In
// hand-over-hand traversal the lock of the current node becomes the lock of
// the previous one:
//
//	prev.Unlock()
//	prev = curr
//
// after which curr names the next node, so the lock taken through curr is now
// released through prev. The move happens only when the destination holds
// nothing and the source holds plain locks with no deferred release pending.
func (c *Checker) applyLockOwnershipMove(stmt *ast.AssignStmt, stats map[string]*Stats) {
	if c.refine == nil || len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
		return
	}
	dst, ok := stmt.Lhs[0].(*ast.Ident)
	if !ok {
		return
	}
	src, ok := common.UnwrapParenExpr(stmt.Rhs[0]).(*ast.Ident)
	if !ok || src.Name == dst.Name {
		return
	}
	sameKind := (c.mutexNames[dst.Name] && c.mutexNames[src.Name]) ||
		(c.rwMutexNames[dst.Name] && c.rwMutexNames[src.Name])
	if !sameKind {
		return
	}
	from, to := stats[src.Name], stats[dst.Name]
	if from == nil || to == nil || !holdsOnlyPlainLocks(from) || !holdsNothing(to) {
		return
	}
	stats[dst.Name] = cloneStats(from)
	stats[src.Name] = &Stats{}
}

func holdsOnlyPlainLocks(st *Stats) bool {
	return (st.lock > 0 || st.rlock > 0) && st.deferUnlock == 0 && st.deferRUnlock == 0 &&
		st.borrowedLock == 0 && st.borrowedRLock == 0
}

func holdsNothing(st *Stats) bool {
	return st.lock == 0 && st.rlock == 0 && st.deferUnlock == 0 && st.deferRUnlock == 0 &&
		st.borrowedLock == 0 && st.borrowedRLock == 0
}

// applyDelegatedWrapperEffect models a wrapper method called on a value that
// is itself a lock. A type whose Unlock releases one of its own fields is
// that field as far as locking goes:
//
//	func (t *batchTx) lock()   { t.Mutex.Lock() }
//	func (t *batchTx) Unlock() { ...; t.Mutex.Unlock() }
//
//	t.lock()
//	t.Unlock()
//
// t.Unlock() releases t, so t.lock() — which takes t.Mutex — takes t. The
// wrapper's effect on that field is applied to the value. Unlock alone decides
// which field it is: a type that embeds one mutex and locks another through
// its own methods keeps the two apart.
func (c *Checker) applyDelegatedWrapperEffect(call *ast.CallExpr, callee *ast.FuncDecl, calleeReceiver, receiverType, mutexName string, rw bool, stats map[string]*Stats) bool {
	if c.refine == nil || isSyncLockerMethod(callee.Name.Name) {
		return false
	}
	path, ok := c.lockDelegatePath(receiverType)
	if !ok {
		return false
	}
	simulated := c.simulateMethodEffect(callee, calleeReceiver+"."+path, rw, stats[mutexName], c.callArgumentFacts(call, callee))
	if simulated == nil {
		return false
	}
	attributeToCall(simulated, stats[mutexName], call.Pos())
	stats[mutexName] = simulated
	return true
}

// lockDelegatePath returns the receiver field the Unlock of typeName releases:
// the field its own Unlock releases on every path, or the embedded mutex whose
// Unlock it promotes.
func (c *Checker) lockDelegatePath(typeName string) (string, bool) {
	if unlock := c.receiverMethods[typeName]["Unlock"]; unlock != nil {
		receiver := common.ReceiverName(unlock)
		if receiver == "" || unlock.Body == nil {
			return "", false
		}
		var found []string
		ast.Inspect(unlock.Body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Unlock" {
				return true
			}
			if path, ok := relativeMutexPath(common.GetVarName(sel.X), receiver); ok {
				found = append(found, path)
			}
			return true
		})
		if len(found) != 1 || !bodyCallsUnconditionally(unlock, lockRef{path: found[0], rooted: true}, []string{"Unlock"}) {
			return "", false
		}
		return found[0], true
	}
	return c.embeddedMutexField(typeName)
}

// embeddedMutexField returns the name of the single sync mutex typeName embeds.
func (c *Checker) embeddedMutexField(typeName string) (string, bool) {
	if c.typesInfo == nil {
		return "", false
	}
	for _, fn := range c.receiverMethods[typeName] {
		if fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		typ := c.typesInfo.TypeOf(fn.Recv.List[0].Type)
		if ptr, ok := typ.(*types.Pointer); ok {
			typ = ptr.Elem()
		}
		st, ok := typ.Underlying().(*types.Struct)
		if !ok {
			return "", false
		}
		name := ""
		for field := range st.Fields() {
			if field.Embedded() && (common.IsMutex(field.Type()) || common.IsRWMutex(field.Type())) {
				if name != "" {
					return "", false
				}
				name = field.Name()
			}
		}
		return name, name != ""
	}
	return "", false
}
