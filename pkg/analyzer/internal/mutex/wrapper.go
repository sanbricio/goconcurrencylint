package mutex

import (
	"go/ast"
	"go/types"
	"slices"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// wrapperResolver decides whether a lone Lock/Unlock (etc.) call inside a method
// is a "borrowed" half of a wrapper pair — e.g. a `func (s *S) Lock()` that only
// calls `s.mu.Lock()` while a sibling `func (s *S) Unlock()` calls
// `s.mu.Unlock()`. Such a call is balanced by its sibling and must not be
// flagged as an unmatched lock.
//
// It was extracted from Checker because the heuristic is cohesive and reads a
// well-defined slice of state: the package-wide receiver→methods index
// (configuration) plus the function currently under analysis. rawBodyEffects is
// snapshotted at construction (it is set once per funcAnalysis and never mutated
// mid-analysis); during a simulated run it is true and resolve short-circuits.
type wrapperResolver struct {
	receiverMethods map[string]map[string]*ast.FuncDecl
	function        *ast.FuncDecl
	rawBodyEffects  bool
	typesInfo       *types.Info
	// lifecycle answers "what does the rest of the package do with this
	// method", which is what isMirrorPairHalf needs and what separates a pair
	// the type splits on purpose from a forgotten release. It is nil in the
	// unit tests that exercise the purely local heuristics.
	lifecycle *lifecycleResolver
}

func newWrapperResolver(receiverMethods map[string]map[string]*ast.FuncDecl, function *ast.FuncDecl, rawBodyEffects bool, typesInfo *types.Info, lifecycle *lifecycleResolver) *wrapperResolver {
	return &wrapperResolver{
		receiverMethods: receiverMethods,
		function:        function,
		rawBodyEffects:  rawBodyEffects,
		typesInfo:       typesInfo,
		lifecycle:       lifecycle,
	}
}

// resolve reports whether the call `<varName>.<methodName>()` is the borrowed
// half of a wrapper pair completed by a sibling method on the same receiver.
func (w *wrapperResolver) resolve(varName, methodName string) bool {
	if w.rawBodyEffects {
		return false
	}

	if w.function == nil || w.function.Name == nil {
		return false
	}

	currentReceiver := common.ReceiverName(w.function)
	if currentReceiver == "" {
		return false
	}

	oppositeMethods := oppositeMutexMethods(methodName)
	if len(oppositeMethods) == 0 || w.currentMethodContainsFieldCall(varName, oppositeMethods) {
		return false
	}

	if w.implementsLockContract(varName, methodName, oppositeMethods) {
		return true
	}

	if w.isKeyedLocalWrapperPair(varName, methodName, oppositeMethods) {
		return true
	}

	// A single-statement Lock/Unlock split across sibling methods is an
	// intentional cross-method barrier, not a leak.
	if w.isBarrierPairHalf(varName, methodName, oppositeMethods) {
		return true
	}

	if w.isMirrorPairHalf(varName, methodName, oppositeMethods) {
		return true
	}

	if w.isOwnerRecordHalf(varName, methodName, oppositeMethods) {
		return true
	}

	if w.isReadGateAcquire(varName, methodName) {
		return true
	}

	// suffix is the mutex's path below the receiver, or "" when the receiver
	// embeds the mutex and the method calls the promoted Lock/Unlock:
	// `func (s *S) lock() { s.Lock() }` pairs with `func (s *S) unlock() {
	// s.Unlock() }` just like the field form does.
	suffix, ok := strings.CutPrefix(varName, currentReceiver)
	if !ok || (suffix != "" && !strings.HasPrefix(suffix, ".")) {
		return false
	}

	if group := mutexMethodGroup(w.function.Name.Name); group != nil {
		if slices.Contains(group, methodName) {
			return w.siblingMethodContainsFieldCall(suffix, oppositeMethods, oppositeMethods)
		}
		// A method named Lock/Unlock may implement the sync.Locker contract
		// over the READ half of an inner RWMutex — the sync.RWMutex.RLocker()
		// shape (Lock → RLock, Unlock → RUnlock), e.g. a test store exposing
		// `func (s *S) Lock() { s.mu.RLock() }` / `func (s *S) Unlock() {
		// s.mu.RUnlock() }`. The sibling is still found by the contract name
		// (Lock ↔ Unlock) while its body must perform the opposite of the
		// inner read op. A read-locking Lock with no read-releasing Unlock
		// sibling is still reported.
		if w.isReadLockerAdapterOp(methodName) {
			return w.siblingMethodContainsFieldCall(suffix, oppositeMutexMethods(w.function.Name.Name), oppositeMethods)
		}
		return false
	}

	return false
}

// isKeyedLocalWrapperPair recognizes keyed-lock APIs whose public Lock/Unlock
// methods fetch the same mutex from a registry and deliberately split its
// lifecycle across calls:
//
//	func (r *registry) LockID(id any)   { m, _ := r.items[id]; m.Lock() }
//	func (r *registry) UnlockID(id any) { m, _ := r.items[id]; m.Unlock() }
//
// The lock is a local, so it has no path a sibling could name; what ties the
// two halves together is the collection they both take it out of. A sibling
// that reaches into the same collection and performs the opposite operation is
// the other half, whatever either method is called.
func (w *wrapperResolver) isKeyedLocalWrapperPair(varName, methodName string, oppositeMethods []string) bool {
	if strings.Contains(varName, ".") {
		return false
	}

	source := localIndexedSource(w.function.Body, varName)
	if source == "" {
		return false
	}

	receiverType := common.ReceiverTypeName(w.function)
	for name, sibling := range w.receiverMethods[receiverType] {
		if name == w.function.Name.Name || sibling == nil || sibling.Body == nil {
			continue
		}
		if bodyOperatesOnIndexedLock(sibling.Body, source, oppositeMethods) {
			return true
		}
	}

	return false
}

// bodyOperatesOnIndexedLock reports whether body takes a lock out of the
// collection named by source and performs one of methods on it.
func bodyOperatesOnIndexedLock(body *ast.BlockStmt, source string, methods []string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !containsMethod(methods, sel.Sel.Name) {
			return true
		}
		local := common.GetVarName(sel.X)
		if local == "" || strings.Contains(local, ".") {
			return true
		}
		if localIndexedSource(body, local) == source {
			found = true
			return false
		}
		return true
	})

	return found
}

func localIndexedSource(body *ast.BlockStmt, localName string) string {
	if body == nil || localName == "" {
		return ""
	}
	found := ""
	ast.Inspect(body, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for index, lhs := range assign.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok || ident.Name != localName {
				continue
			}
			var rhs ast.Expr
			switch {
			case len(assign.Rhs) == 1:
				rhs = assign.Rhs[0]
			case index < len(assign.Rhs):
				rhs = assign.Rhs[index]
			}
			indexed, ok := common.UnwrapParenExpr(rhs).(*ast.IndexExpr)
			if !ok {
				continue
			}
			found = common.GetVarName(indexed.X)
			return false
		}
		return true
	})
	return found
}

// isReadLockerAdapterOp reports whether the inner mutex call is the read-side
// implementation of a write-named Locker method: a Lock/TryLock-named method
// acquiring via RLock/TryRLock, or an Unlock-named method releasing via
// RUnlock.
func (w *wrapperResolver) isReadLockerAdapterOp(methodName string) bool {
	fnName := w.function.Name.Name
	if slices.Contains(WriteLockPattern.LockMethods, fnName) {
		return slices.Contains(ReadLockPattern.LockMethods, methodName)
	}
	if slices.Contains(WriteLockPattern.UnlockMethods, fnName) {
		return slices.Contains(ReadLockPattern.UnlockMethods, methodName)
	}
	return false
}

func (w *wrapperResolver) currentMethodContainsFieldCall(varName string, methodNames []string) bool {
	return functionBodyContainsFieldCall(w.function.Body, varName, methodNames)
}

func (w *wrapperResolver) siblingMethodContainsFieldCall(fieldSuffix string, siblingMethods, fieldMethods []string) bool {
	receiverType := common.ReceiverTypeName(w.function)
	if receiverType == "" {
		return false
	}

	methods := w.receiverMethods[receiverType]
	if len(methods) == 0 {
		return false
	}

	for _, siblingMethod := range siblingMethods {
		fn := methods[siblingMethod]
		if fn == nil || fn.Body == nil {
			continue
		}

		siblingReceiver := common.ReceiverName(fn)
		if siblingReceiver == "" {
			continue
		}

		targetVar := siblingReceiver + fieldSuffix
		if functionBodyContainsFieldCall(fn.Body, targetVar, fieldMethods) {
			return true
		}
	}

	return false
}

func (w *wrapperResolver) typeNameForBaseVar(baseVar string) string {
	if baseVar == "" {
		return ""
	}
	if baseVar == common.ReceiverName(w.function) {
		return common.ReceiverTypeName(w.function)
	}
	if w.function == nil || w.function.Body == nil || w.typesInfo == nil {
		return ""
	}

	found := ""
	ast.Inspect(w.function.Body, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		ident, ok := n.(*ast.Ident)
		if !ok || ident.Name != baseVar {
			return true
		}
		found = common.BaseTypeNameFromType(w.typesInfo.TypeOf(ident))
		return found == ""
	})
	return found
}

// isBarrierPairHalf reports whether the current method consists of exactly one
// mutex call on a field, balanced by a sibling method consisting of exactly one
// matching opposite call on the same field. Both halves being single statements
// is the signal that the split across methods is an intentional barrier/latch
// rather than a forgotten unlock, so it holds regardless of the method names.
func (w *wrapperResolver) isBarrierPairHalf(varName, methodName string, oppositeMethods []string) bool {
	_, fieldSuffix, ok := splitBaseAndSuffix(varName)
	if !ok {
		return false
	}
	if !bodyIsSingleFieldSuffixCall(w.function.Body, fieldSuffix, []string{methodName}) {
		return false
	}
	return w.anySiblingBodyIsSingleFieldSuffixCall(fieldSuffix, w.function.Name.Name, oppositeMethods)
}

func (w *wrapperResolver) anySiblingBodyIsSingleFieldSuffixCall(fieldSuffix, excludeMethod string, methodNames []string) bool {
	receiverType := common.ReceiverTypeName(w.function)
	if receiverType == "" {
		return false
	}

	for methodName, fn := range w.receiverMethods[receiverType] {
		if methodName == excludeMethod || fn == nil {
			continue
		}
		if bodyIsSingleFieldSuffixCall(fn.Body, fieldSuffix, methodNames) {
			return true
		}
	}

	return false
}

// bodyIsSingleFieldSuffixCall reports whether body is exactly one expression
// statement calling one of methodNames on a field whose suffix equals
// fieldSuffix (e.g. body `{ b.lock.Unlock() }` with suffix "lock").
func bodyIsSingleFieldSuffixCall(body *ast.BlockStmt, fieldSuffix string, methodNames []string) bool {
	if body == nil || len(body.List) != 1 {
		return false
	}

	exprStmt, ok := body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}

	call, ok := exprStmt.X.(*ast.CallExpr)
	if !ok {
		return false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !containsMethod(methodNames, sel.Sel.Name) {
		return false
	}

	_, suffix, ok := splitBaseAndSuffix(common.GetVarName(sel.X))
	return ok && suffix == fieldSuffix
}

// implementsLockContract reports whether the enclosing method is its receiver
// type's own implementation of a sync lock operation — the Lock of a
// sync.Locker, the RUnlock of an RWMutex-shaped type — so the inner call may
// leave the lock in the state that contract promises. The canonical shape is a
// mutex wrapped to measure or order acquisition:
//
//	func (s *sortableMutex) Lock() {
//		start := time.Now()
//		s.Mutex.Lock()          // held on return: that is what Lock means
//		s.acquireDuration += time.Since(start)
//	}
//
// This reads the interface, not the spelling. The method's name must be a sync
// lock operation exactly, and the receiver type must expose the opposite
// operation — declared beside it, or promoted from an embedded mutex as above.
// A lone Lock with nothing on the type to release it is not a contract and is
// still reported, which is the safety net that keeps a method merely called
// `Lock` from silencing itself.
//
// The inner call must sit on the same side as the method implementing it: an
// acquire inside an acquire, a release inside a release. An acquire left held
// by an Unlock implementation is not the contract and stays reported.
func (w *wrapperResolver) implementsLockContract(varName, methodName string, oppositeMethods []string) bool {
	fnName := w.function.Name.Name
	if !sameLockSide(fnName, methodName) {
		return false
	}

	receiverType := w.receiverType()
	if receiverType == nil {
		return false
	}

	declared := w.receiverMethods[common.ReceiverTypeName(w.function)]
	for _, opposite := range oppositeMutexMethods(fnName) {
		if !common.HasNiladicMethod(receiverType, opposite) {
			continue
		}

		// The counterpart must really perform the opposite operation: a wrapper
		// whose Unlock forgets the inner mutex leaks exactly like an inline
		// one, and is still reported. A counterpart promoted from an embedded
		// mutex has no body here to check, and is answered by isMirrorPairHalf
		// instead.
		sibling := declared[opposite]
		if sibling != nil && w.siblingPerformsOpposite(sibling, varName, oppositeMethods) {
			return true
		}
	}

	return false
}

// siblingPerformsOpposite reports whether sibling performs one of oppositeOps
// on the lock the current call names. The lock is matched by path when it is
// rooted at the receiver (`im.realLock` in Lock pairs with `im.realLock` in
// Unlock, whatever either receiver is called); a lock reached some other way —
// an element of a collection the method walks, say — is matched by operation
// alone, since the path cannot be spelled the same way twice.
func (w *wrapperResolver) siblingPerformsOpposite(sibling *ast.FuncDecl, varName string, oppositeOps []string) bool {
	if sibling.Body == nil {
		return false
	}

	siblingReceiver := common.ReceiverName(sibling)
	base, suffix, ok := splitBaseAndSuffix(varName)
	if ok && siblingReceiver != "" && base == common.ReceiverName(w.function) {
		return functionBodyContainsFieldCall(sibling.Body, siblingReceiver+"."+suffix, oppositeOps)
	}

	return bodyContainsAnyMethodCall(sibling.Body, oppositeOps)
}

// receiverType returns the current method's receiver type with the pointer
// peeled off, or nil when the function is not a method.
func (w *wrapperResolver) receiverType() types.Type {
	if w.typesInfo == nil || w.function == nil || w.function.Recv == nil || len(w.function.Recv.List) == 0 {
		return nil
	}

	recvType := w.typesInfo.TypeOf(w.function.Recv.List[0].Type)
	if recvType == nil {
		return nil
	}
	if ptr, ok := types.Unalias(recvType).(*types.Pointer); ok {
		recvType = ptr.Elem()
	}

	return types.Unalias(recvType)
}
