package mutex

import (
	"go/ast"
	"go/token"
	"slices"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A type may split one lock's lifecycle across two of its methods on purpose:
// one takes the lock, the other hands it back, and callers use them as a pair.
//
//	func (s *shard) wLock()   { recordMetric(); s.rwLock.Lock() }
//	func (s *shard) wUnlock() { s.rwLock.Unlock() }
//
// Read one at a time, each half looks exactly like the bug this analyzer exists
// to find. What tells them apart is not how they are spelled — a linter that
// decided from the words `lock` and `unlock` would be reading one project's
// house style — but three facts about the code:
//
//  1. the half under analysis touches the lock once, unconditionally, so it has
//     no path where it holds the lock and no path where it does not (the
//     forgotten release almost always does: `Lock(); if err != nil { return }`);
//  2. the receiver type carries the opposite operation on the same lock, either
//     in another method or promoted from a mutex it embeds; and
//  3. the method exists only to expose that operation — its body does nothing
//     else — or some function in the package really uses the two halves
//     together.
//
// The third is what keeps the rule honest, and it has two forms because real
// pairs come in two shapes. A body that is nothing but the lock call (or a loop
// of lock calls over a collection) cannot be the function that forgot to
// release: it has no work to do between taking the lock and returning. A half
// that does its own work alongside — records a metric, stores a version —
// could be, so that one has to be caught in the act: some caller must use it
// and its counterpart as one operation. That question is asked about *this*
// method, so a method that genuinely leaks is still reported even when the type
// offers a proper pair beside it, because its own callers never release what it
// took. The cost is a false negative for a working pair that no caller in the
// package uses, which is the direction this analyzer errs.

// isMirrorPairHalf reports whether the lock operation under analysis is one half
// of such a pair.
func (w *wrapperResolver) isMirrorPairHalf(varName, methodName string, oppositeMethods []string) bool {
	if w.lifecycle == nil || w.function == nil || w.function.Name == nil {
		return false
	}

	receiverType := common.ReceiverTypeName(w.function)
	if receiverType == "" {
		return false
	}

	ref, ok := lockRefFor(varName, common.ReceiverName(w.function))
	if !ok {
		return false
	}

	if !bodyCallsUnconditionally(w.function, ref, []string{methodName}) {
		return false
	}

	siblings := w.mirrorSiblingNames(receiverType, ref, methodName, oppositeMethods)
	if len(siblings) == 0 {
		return false
	}

	// A body that is nothing but the lock call speaks for itself only while the
	// package says nothing about it. Once something calls the method, that use
	// is the better evidence and has to agree: a release helper called from a
	// caller that never took the lock is a leak however pure it looks.
	if bodyIsPureLockAdapter(w.function, ref, []string{methodName}) &&
		!w.lifecycle.methodIsCalledInPackage(w.function.Name.Name, w.function) {
		return true
	}

	// The acquiring half is used before its counterpart, the releasing half
	// after it, so which one to look for depends on the side this method is on.
	acquiresFirst := lockMethodSide(methodName) == acquireLockSide
	for _, sibling := range siblings {
		if w.lifecycle.callersPairMethods(w.function.Name.Name, sibling, acquiresFirst, w.function) {
			return true
		}
	}

	return false
}

// lockRef identifies the lock a method operates on. A lock reached from the
// receiver is identified by its path below it, so the same lock can be found in
// a sibling whose receiver is spelled differently; the empty path is the
// receiver itself, which is how a promoted mutex is reached
// (`func (c *counter) lock() { c.Lock() }`). A lock reached some other way — an
// element of a collection the method walks — can only be matched by the field
// path it hangs off, since no two methods spell its root the same way.
type lockRef struct {
	path   string
	suffix string
	rooted bool
}

func lockRefFor(varName, receiver string) (lockRef, bool) {
	if receiver != "" {
		if varName == receiver {
			return lockRef{rooted: true}, true
		}
		if path, ok := strings.CutPrefix(varName, receiver+"."); ok {
			return lockRef{path: path, rooted: true}, true
		}
	}

	if _, suffix, ok := splitBaseAndSuffix(varName); ok {
		return lockRef{suffix: suffix}, true
	}

	return lockRef{}, false
}

// matches reports whether call performs one of methods on this lock, read from
// a method whose receiver is named receiver.
func (r lockRef) matches(call *ast.CallExpr, receiver string, methods []string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !containsMethod(methods, sel.Sel.Name) {
		return false
	}

	target := common.GetVarName(sel.X)
	if r.rooted {
		if receiver == "" {
			return false
		}
		if r.path == "" {
			return target == receiver
		}
		return target == receiver+"."+r.path
	}

	_, suffix, ok := splitBaseAndSuffix(target)
	return ok && suffix == r.suffix
}

// mirrorSiblingNames returns the methods of receiverType that could be the other
// half: a method performing the opposite operation on the same lock and not the
// same one, plus the sync operation itself when the type inherits it from an
// embedded mutex (`func (i *internalMutex) UnlockIgnoreTime() { i.Mutex.Unlock() }`
// pairs with the `Lock` the embedded mutex promotes).
func (w *wrapperResolver) mirrorSiblingNames(receiverType string, ref lockRef, methodName string, oppositeMethods []string) []string {
	sameSide := mutexMethodGroup(methodName)

	var names []string
	declared := w.receiverMethods[receiverType]
	for name, fn := range declared {
		if fn == nil || fn.Body == nil || name == w.function.Name.Name {
			continue
		}
		// The counterpart must perform the opposite operation on every path.
		// It may also perform this method's own operation, as long as that one
		// is conditional: an acquire-or-fail helper rolls the lock back on its
		// failure arm and is still, on balance, the acquiring half. A method
		// that does both unconditionally is balanced in itself and pairs with
		// nobody.
		if !bodyCallsUnconditionally(fn, ref, oppositeMethods) || bodyCallsUnconditionally(fn, ref, sameSide) {
			continue
		}
		names = append(names, name)
	}

	if receiver := w.receiverType(); receiver != nil {
		for _, opposite := range oppositeMethods {
			if declared[opposite] == nil && common.HasNiladicMethod(receiver, opposite) {
				names = append(names, opposite)
			}
		}
	}

	return names
}

// bodyCallsUnconditionally reports whether fn performs one of methods on the
// lock at least once, and never where a branch could skip it. A loop counts as
// unconditional: a method that locks every element of a collection is answered
// by one that unlocks every element of the same collection, and an empty
// collection leaves both halves with nothing to do.
func bodyCallsUnconditionally(fn *ast.FuncDecl, ref lockRef, methods []string) bool {
	if fn == nil || fn.Body == nil {
		return false
	}

	receiver := common.ReceiverName(fn)
	total, conditional := 0, 0
	var stack []ast.Node
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)

		call, ok := n.(*ast.CallExpr)
		if !ok || !ref.matches(call, receiver, methods) {
			return true
		}

		total++
		if pathIsConditional(stack) {
			conditional++
		}
		return true
	})

	return total > 0 && conditional == 0
}

// pathIsConditional reports whether the node at the top of stack is reached only
// when some condition holds. An `if` counts only when the node hangs off an arm:
// its init statement and its condition run either way.
func pathIsConditional(stack []ast.Node) bool {
	for index := 0; index+1 < len(stack); index++ {
		child := stack[index+1]
		switch parent := stack[index].(type) {
		case *ast.IfStmt:
			if child == ast.Node(parent.Body) || (parent.Else != nil && child == parent.Else) {
				return true
			}
		case *ast.CaseClause, *ast.CommClause:
			return true
		case *ast.FuncLit:
			return true
		case *ast.BinaryExpr:
			if (parent.Op == token.LAND || parent.Op == token.LOR) && child == ast.Node(parent.Y) {
				return true
			}
		}
	}

	return false
}

// bodyIsPureLockAdapter reports whether fn does nothing but perform one of
// methods on the lock — either directly, or once per element of a collection it
// walks. Such a method holds no state between taking the lock and returning, so
// it is the lock's API rather than a user of it.
func bodyIsPureLockAdapter(fn *ast.FuncDecl, ref lockRef, methods []string) bool {
	if fn == nil {
		return false
	}
	return blockIsPureLockAdapter(fn.Body, ref, common.ReceiverName(fn), methods)
}

func blockIsPureLockAdapter(body *ast.BlockStmt, ref lockRef, receiver string, methods []string) bool {
	if body == nil || len(body.List) == 0 {
		return false
	}

	for _, stmt := range body.List {
		switch node := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := node.X.(*ast.CallExpr)
			if !ok || !ref.matches(call, receiver, methods) {
				return false
			}
		case *ast.RangeStmt:
			if !blockIsPureLockAdapter(node.Body, ref, receiver, methods) {
				return false
			}
		case *ast.ForStmt:
			if !blockIsPureLockAdapter(node.Body, ref, receiver, methods) {
				return false
			}
		default:
			return false
		}
	}

	return true
}

// callersPairMethods reports whether some function in the package calls both
// halves on the same receiver expression, the acquiring one first — the
// evidence that the two are used as one operation. acquiresFirst says which
// side the method under analysis is on. exclude keeps a method that calls its
// own counterpart from vouching for itself.
func (l *lifecycleResolver) callersPairMethods(method, sibling string, acquiresFirst bool, exclude *ast.FuncDecl) bool {
	key := methodPairKey{first: method, second: sibling, acquiresFirst: acquiresFirst}
	if cached, ok := l.scanCache.pairedCallers[key]; ok {
		return cached
	}

	paired := false
	for _, site := range l.callSitesNamed(method) {
		if site.enclosing == nil || site.enclosing.Body == nil || site.enclosing == exclude {
			continue
		}
		sel, ok := site.call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		receiver := common.GetVarName(sel.X)
		if receiver == "" || receiver == "?" {
			continue
		}
		if callsMethodOnReceiver(site.enclosing.Body, receiver, sibling, site.call.Pos(), acquiresFirst) {
			paired = true
			break
		}
	}

	if l.scanCache.pairedCallers == nil {
		l.scanCache.pairedCallers = make(map[methodPairKey]bool)
	}
	l.scanCache.pairedCallers[key] = paired
	return paired
}

// methodIsCalledInPackage reports whether anything other than the method itself
// calls a method of that name anywhere in the package. It is deliberately
// keyed on the name alone: a method shared by two types answers true for both,
// which only moves the decision to the stricter evidence check.
func (l *lifecycleResolver) methodIsCalledInPackage(name string, exclude *ast.FuncDecl) bool {
	for _, site := range l.callSitesNamed(name) {
		if site.enclosing != nil && site.enclosing != exclude {
			return true
		}
	}

	return false
}

// callsMethodOnReceiver reports whether body calls receiver.name(...) on the
// far side of pos: after it when the counterpart releases (`x.lock(); defer
// x.unlock()`), before it when the counterpart acquires (`x.lock(); x.unlock()`
// seen from unlock). A deferred release qualifies, since it is written below
// the acquire.
func callsMethodOnReceiver(body *ast.BlockStmt, receiver, name string, pos token.Pos, after bool) bool {
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
		if !ok || sel.Sel.Name != name {
			return true
		}
		if after != (call.Pos() > pos) {
			return true
		}
		if common.GetVarName(sel.X) == receiver {
			found = true
			return false
		}
		return true
	})

	return found
}

// firstLockOpIsRelease reports whether the first thing fn does to the lock is
// give it back. That is the signature of a method written to be handed a lock
// its caller holds: it cannot have taken one yet, so unlocking would panic if
// the caller were not holding it. What the method does afterwards — grpc's
// resetTransportAndUnlock releases, does its slow work, then locks again for
// its own bookkeeping — is its own business.
//
// Order is read from the source positions, so a method that releases in one
// arm of a branch and acquires in the other is read by its first arm. Such a
// method expects the lock held on some paths and not others, which is broken
// however it is analyzed.
func firstLockOpIsRelease(fn *ast.FuncDecl, ref lockRef, pattern LockPattern) bool {
	if fn == nil || fn.Body == nil {
		return false
	}

	receiver := common.ReceiverName(fn)
	firstPos, firstIsRelease, found := token.NoPos, false, false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		release := ref.matches(call, receiver, pattern.UnlockMethods)
		if !release && !ref.matches(call, receiver, pattern.LockMethods) {
			return true
		}

		if !found || call.Pos() < firstPos {
			firstPos, firstIsRelease, found = call.Pos(), release, true
		}
		return true
	})

	return found && firstIsRelease
}

// isMirrorPairRelease reports whether the function under analysis is the
// releasing half of a lock lifecycle the type splits across two methods.
//
// The lock/unlock walk asks the wrapper resolver about each call as it meets
// it, but a release written as `defer m.lock.Unlock()`, or one whose matching
// acquire lives in another method entirely, does not reach that question: it
// surfaces later as a deferred unlock with nothing to balance, or at the end of
// the function as a borrowed one. Both ask here instead.
func (c *Checker) isMirrorPairRelease(varName string, acquireMethods []string) bool {
	for _, release := range matchingUnlockMethods(acquireMethods) {
		if c.wrapper.isMirrorPairHalf(varName, release, acquireMethods) {
			return true
		}
	}

	return false
}

// isReadGateAcquire reports whether a lone read lock is a wait on a gate rather
// than a forgotten release.
//
// An RWMutex can be used as a one-way gate instead of as a reader/writer lock:
// something takes the write lock to hold everyone back, readers pile up on
// RLock, and the write Unlock releases them all at once. The readers' RLock is
// answered by that Unlock, never by an RUnlock, so the pairing this analyzer
// looks for does not exist by construction.
//
//	func (r *pending) start() { r.executing.Lock() }    // gate closed
//	func (r *pending) Wait()  { r.executing.RLock() }   // blocks until it opens
//	func (r *pending) done()  { r.executing.Unlock() }  // gate opened
//
// What identifies the gate is the write half being split across two methods of
// the type that owns the mutex: one that takes the write lock and never gives
// it back, one that gives it back without ever taking it. No ordinary
// reader/writer discipline is written that way — a writer locks and unlocks in
// the same place — so a type that does is running a protocol of its own, and a
// lone read lock on that mutex belongs to it.
//
// The mutex is matched by the field it hangs off rather than by a path from
// some receiver, because the reader usually is not a method of the owning type
// at all: in a worker/barrier the reader reaches the gate through a parent
// pointer.
func (w *wrapperResolver) isReadGateAcquire(varName, methodName string) bool {
	if w.lifecycle == nil || !slices.Contains(ReadLockPattern.LockMethods, methodName) {
		return false
	}

	baseVar, suffix, ok := splitBaseAndSuffix(varName)
	if !ok {
		return false
	}

	ownerType := ""
	if baseVar == common.ReceiverName(w.function) {
		ownerType = common.ReceiverTypeName(w.function)
	} else {
		ownerType = w.typeNameForBaseVar(baseVar)
	}
	if ownerType == "" {
		return false
	}

	halves := w.lifecycle.gateHalvesFor(ownerType, suffix)
	return halves.closes && halves.opens && !halves.readReleased
}

// gateHalves records how a package drives one mutex field.
type gateHalves struct {
	closes       bool
	opens        bool
	readReleased bool
}

// gateHalvesFor scans the package for what it does to the field named suffix of
// ownerType. The search is package-wide because the two halves of a gate are
// rarely methods of the same type: vitess closes its gate inside the factory
// that builds the object (`r.executing.Lock()` in consolidator.Create) and
// opens it from a method of the object itself.
//
// The field is matched through its owner's type, never by the field name alone,
// so a package full of `.mu` fields answers each of them separately.
func (l *lifecycleResolver) gateHalvesFor(ownerType, suffix string) gateHalves {
	key := gateKey{ownerType: ownerType, suffix: suffix}
	if cached, ok := l.scanCache.gateHalves[key]; ok {
		return cached
	}

	var halves gateHalves
	for _, fn := range l.functions {
		takes, gives, readReleases := l.gateOpsIn(fn, ownerType, suffix)
		switch {
		case takes && !gives:
			halves.closes = true
		case gives && !takes:
			halves.opens = true
		}
		if readReleases {
			halves.readReleased = true
		}
	}

	if l.scanCache.gateHalves == nil {
		l.scanCache.gateHalves = make(map[gateKey]gateHalves)
	}
	l.scanCache.gateHalves[key] = halves
	return halves
}

// gateOpsIn reports what fn does to that field: whether it takes the write lock
// on every path, gives it back on every path, and whether it releases the read
// half at all.
func (l *lifecycleResolver) gateOpsIn(fn *ast.FuncDecl, ownerType, suffix string) (takes, gives, readReleases bool) {
	if fn == nil || fn.Body == nil || l.typesInfo == nil {
		return false, false, false
	}

	conditionalTake, conditionalGive := false, false
	var stack []ast.Node
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		field, ok := sel.X.(*ast.SelectorExpr)
		if !ok || field.Sel.Name != suffix {
			return true
		}
		if common.BaseTypeNameFromType(l.typesInfo.TypeOf(field.X)) != ownerType {
			return true
		}

		conditional := pathIsConditional(stack)
		switch {
		case containsMethod(ReadLockPattern.UnlockMethods, sel.Sel.Name):
			readReleases = true
		case containsMethod(WriteLockPattern.LockMethods, sel.Sel.Name):
			if conditional {
				conditionalTake = true
			} else {
				takes = true
			}
		case containsMethod(WriteLockPattern.UnlockMethods, sel.Sel.Name):
			if conditional {
				conditionalGive = true
			} else {
				gives = true
			}
		}
		return true
	})

	return takes && !conditionalTake, gives && !conditionalGive, readReleases
}
