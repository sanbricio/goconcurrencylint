package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// successResult names how a function reports success in its last result.
type successResult int

const (
	noSuccessResult    successResult = iota
	errorSuccessResult               // a nil error means success
	boolSuccessResult                // true means success
)

// returnLockState is the state of one lock at a return statement.
type returnLockState int

const (
	lockNotAcquiredAtReturn returnLockState = iota
	lockHeldAtReturn
	lockReleasedAtReturn
	// lockNotHandedOverAtReturn: the return sits in the failure arm of a check
	// on an acquire-or-fail helper, which did not hand the lock over there.
	lockNotHandedOverAtReturn
)

// anyAcquireMethods acquires a mutex in either mode: after a helper handed a
// lock over, taking it again in any mode would self-deadlock.
var anyAcquireMethods = slices.Concat(WriteLockPattern.LockMethods, ReadLockPattern.LockMethods)

// anyReleaseMethods releases a mutex in either mode.
var anyReleaseMethods = slices.Concat(WriteLockPattern.UnlockMethods, ReadLockPattern.UnlockMethods)

// conditionalAcquireKey identifies one question about fn and mutexName, in
// write or read mode: whether fn keeps the acquire-or-fail contract for it
// (acquiresOnSuccess), or whether it also hands the lock to callers that take
// it over (handsLockToCaller).
type conditionalAcquireKey struct {
	fn        *ast.FuncDecl
	mutexName string
	read      bool
}

// isConditionalAcquireHelper reports whether the function under analysis hands
// mutexName to its caller on success (see handsLockToCaller), so the lock it
// still holds when it returns is the caller's to release, not a leak.
func (l *lifecycleResolver) isConditionalAcquireHelper(mutexName string, pattern LockPattern) bool {
	return l.handsLockToCaller(l.function, mutexName, pattern)
}

// handsLockToCaller reports whether fn is an acquire-or-fail helper for
// mutexName: like TryLock, it returns holding the lock exactly when it reports
// success (acquiresOnSuccess), and its callers take that lock over.
//
//	func (e *Endpoint) lockAlive() error {
//		e.mutex.Lock()
//		if e.IsDisconnecting() {
//			e.mutex.Unlock()
//			return ErrNotAlive
//		}
//		return nil
//	}
//
// The shape alone cannot tell such a helper from a function that forgot the
// unlock on its success path, so the callers must confirm the contract (see
// callersTakeOverLock). The same answer serves both sides: the helper's own
// lock is not reported, and callers count the lock as theirs on the success
// path.
func (l *lifecycleResolver) handsLockToCaller(fn *ast.FuncDecl, mutexName string, pattern LockPattern) bool {
	key := conditionalAcquireKey{fn: fn, mutexName: mutexName, read: slices.Contains(pattern.LockMethods, "RLock")}
	if answer, ok := l.scanCache.handsOver[key]; ok {
		return answer
	}
	answer := false
	if l.acquiresOnSuccess(fn, mutexName, pattern) {
		receiverType := common.ReceiverTypeName(fn)
		path, _ := receiverRelativePath(mutexName, common.ReceiverName(fn))
		answer = l.callersTakeOverLock(fn, receiverType, path, pattern, l.successResultOf(fn))
	}
	l.scanCache.handsOver[key] = answer
	return answer
}

// acquiresOnSuccess reports whether fn keeps the acquire-or-fail contract for
// mutexName, a lock under its receiver: every return holds the lock exactly
// when it reports success (see lockHeldOnlyOnSuccess). Whether anybody takes
// the lock over is a separate question (callersTakeOverLock): a helper that
// calls another one relies only on this contract, not on who else calls it.
func (l *lifecycleResolver) acquiresOnSuccess(fn *ast.FuncDecl, mutexName string, pattern LockPattern) bool {
	key := conditionalAcquireKey{fn: fn, mutexName: mutexName, read: slices.Contains(pattern.LockMethods, "RLock")}
	return l.scanCache.handOver.solve(key, func() bool {
		return l.computeAcquiresOnSuccess(fn, mutexName, pattern)
	})
}

// handOverSolver memoizes acquiresOnSuccess. Helpers may wrap each other, even
// recursively, so answers can depend on each other in a cycle. A cycle is
// solved optimistically, as an induction over the nested calls: a helper still
// being checked is assumed to hand its lock over, and every answer computed
// while such an assumption stands is tentative. When the outermost query ends,
// an assumption that turned out false is pinned to false and the query runs
// again; once every assumption holds, all its answers settle. The answers are
// thus the same whichever helper of a cycle is asked first.
type handOverSolver struct {
	settled    map[conditionalAcquireKey]bool
	tentative  map[conditionalAcquireKey]bool
	inProgress map[conditionalAcquireKey]bool
	assumed    map[conditionalAcquireKey]bool
	pinned     map[conditionalAcquireKey]bool
	depth      int
}

func newHandOverSolver() *handOverSolver {
	return &handOverSolver{
		settled:    make(map[conditionalAcquireKey]bool),
		tentative:  make(map[conditionalAcquireKey]bool),
		inProgress: make(map[conditionalAcquireKey]bool),
		assumed:    make(map[conditionalAcquireKey]bool),
		pinned:     make(map[conditionalAcquireKey]bool),
	}
}

func (s *handOverSolver) solve(key conditionalAcquireKey, compute func() bool) bool {
	if answer, ok := s.settled[key]; ok {
		return answer
	}
	if s.pinned[key] {
		return false
	}
	if s.inProgress[key] {
		s.assumed[key] = true
		return true
	}
	if answer, ok := s.tentative[key]; ok {
		return answer
	}
	if s.depth > 0 {
		return s.run(key, compute)
	}
	for {
		answer := s.run(key, compute)
		refuted := false
		for assumption := range s.assumed {
			if !s.tentative[assumption] {
				s.pinned[assumption] = true
				refuted = true
			}
		}
		clear(s.assumed)
		if refuted {
			// Each round pins a new key, so the loop ends.
			clear(s.tentative)
			continue
		}
		maps.Copy(s.settled, s.tentative)
		for pinned := range s.pinned {
			s.settled[pinned] = false
		}
		clear(s.tentative)
		clear(s.pinned)
		return answer
	}
}

func (s *handOverSolver) run(key conditionalAcquireKey, compute func() bool) bool {
	s.depth++
	s.inProgress[key] = true
	answer := compute()
	delete(s.inProgress, key)
	s.depth--
	s.tentative[key] = answer
	return answer
}

func (l *lifecycleResolver) computeAcquiresOnSuccess(fn *ast.FuncDecl, mutexName string, pattern LockPattern) bool {
	if fn == nil || fn.Name == nil || fn.Body == nil {
		return false
	}
	receiver := common.ReceiverName(fn)
	if receiver == "" || common.ReceiverTypeName(fn) == "" {
		return false
	}
	if _, ok := receiverRelativePath(mutexName, receiver); !ok {
		return false
	}
	// Cheap pre-filter: the lock must be taken in the body, directly or through
	// a call that may take it.
	if !functionBodyContainsFieldCall(fn.Body, mutexName, pattern.LockMethods) && !l.callsPossibleAcquirer(fn.Body, mutexName, pattern) {
		return false
	}

	kind := l.successResultOf(fn)
	return kind != noSuccessResult && l.lockHeldOnlyOnSuccess(fn, mutexName, pattern, kind)
}

// receiverRelativePath returns where mutexName lives relative to receiver: a
// field path, or "" when the receiver embeds the mutex and is locked itself.
func receiverRelativePath(mutexName, receiver string) (string, bool) {
	if mutexName == receiver {
		return "", true
	}
	return relativeMutexPath(mutexName, receiver)
}

// joinMutexPath is the inverse of receiverRelativePath.
func joinMutexPath(base, path string) string {
	if path == "" {
		return base
	}
	return base + "." + path
}

// successResultOf reports how fn signals success through its last result.
func (l *lifecycleResolver) successResultOf(fn *ast.FuncDecl) successResult {
	if l.typesInfo == nil || fn.Type == nil || fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return noSuccessResult
	}
	typ := l.typesInfo.TypeOf(fn.Type.Results.List[len(fn.Type.Results.List)-1].Type)
	if typ == nil {
		return noSuccessResult
	}
	if types.Identical(typ, types.Universe.Lookup("error").Type()) {
		return errorSuccessResult
	}
	if basic, ok := typ.Underlying().(*types.Basic); ok && basic.Kind() == types.Bool {
		return boolSuccessResult
	}
	return noSuccessResult
}

// lockHeldOnlyOnSuccess reports whether every return of fn holds mutexName
// exactly when it reports success, and at least one failure return leaves
// without it. That failure return may roll the acquisition back, sit in the
// failure arm of a helper that took it (see lockStateAtReturn), or simply
// happen before the lock is reached — a helper that waits for the object to be
// usable and only then locks has nothing to roll back:
//
//	func (m *guard) lock(ctx context.Context) error {
//		if err := m.awaitReady(ctx); err != nil {
//			return err      // no lock taken, nothing to undo
//		}
//		m.mtx.Lock()
//		return nil
//	}
//
// What the failure return must not do is keep the lock, which is exactly the
// forgotten release this analyzer looks for. A return that passes on the result
// of such a helper (`return e.lockAlive()`) keeps the contract by construction.
// Returns inside loops, naked returns and results that do not state success or
// failure explicitly decline: the contract must be visible, not inferred.
func (l *lifecycleResolver) lockHeldOnlyOnSuccess(fn *ast.FuncDecl, mutexName string, pattern LockPattern, kind successResult) bool {
	count := resultCount(fn)
	heldOnSuccess, conditional := false, false
	for _, ret := range functionReturns(fn.Body) {
		if len(ret.Results) != count {
			return false
		}
		state, ok := l.lockStateAtReturn(fn.Body, ret, mutexName, pattern)
		if !ok {
			return false
		}
		result := ret.Results[count-1]
		if state == lockNotAcquiredAtReturn && l.delegatesHandOver(result, mutexName, pattern, kind) {
			heldOnSuccess, conditional = true, true
			continue
		}
		success, ok := l.reportsSuccess(result, kind)
		if !ok {
			return false
		}
		switch state {
		case lockHeldAtReturn:
			if !success {
				return false
			}
			heldOnSuccess = true
		case lockReleasedAtReturn, lockNotHandedOverAtReturn:
			if success {
				return false
			}
			conditional = true
		default:
			if success {
				return false
			}
			// Never acquired on this path: a failure return without the lock,
			// which is the contract holding just as much as a rollback is.
			conditional = true
		}
	}
	return heldOnSuccess && conditional
}

// delegatesHandOver reports whether result is a call to a helper that hands
// mutexName over under the same success convention.
func (l *lifecycleResolver) delegatesHandOver(result ast.Expr, mutexName string, pattern LockPattern, kind successResult) bool {
	call, ok := common.UnwrapParenExpr(result).(*ast.CallExpr)
	if !ok {
		return false
	}
	calleeKind, handsOver := l.callAcquiresOnSuccess(call, mutexName, pattern)
	return handsOver && calleeKind == kind
}

// reportsSuccess classifies a returned success result. Any non-nil error
// expression is a failure; a bool result must be a constant.
func (l *lifecycleResolver) reportsSuccess(result ast.Expr, kind successResult) (bool, bool) {
	result = common.UnwrapParenExpr(result)
	if kind == errorSuccessResult {
		return isNilIdent(result), true
	}
	return common.ConstantBoolValue(result, l.typesInfo)
}

func resultCount(fn *ast.FuncDecl) int {
	count := 0
	for _, field := range fn.Type.Results.List {
		count += max(len(field.Names), 1)
	}
	return count
}

// functionReturns returns fn's own return statements, skipping function
// literals.
func functionReturns(body *ast.BlockStmt) []*ast.ReturnStmt {
	var returns []*ast.ReturnStmt
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			returns = append(returns, node)
		}
		return true
	})
	return returns
}

// lockStateAtReturn finds the latest lock operation that dominates ret: a
// direct Lock/Unlock or a call to a wrapper method (see wrapperLockEffect). An
// earlier `if` without else whose body ends in a return is skipped: reaching
// ret means that body did not run. When that `if` is the failure check of an
// acquire-or-fail helper (see handOverGuard), getting past it means the helper
// succeeded and the lock is held; a ret inside it means the helper failed and
// kept nothing. Any other statement that touches the lock makes the state
// uncertain.
func (l *lifecycleResolver) lockStateAtReturn(body *ast.BlockStmt, ret *ast.ReturnStmt, mutexName string, pattern LockPattern) (returnLockState, bool) {
	path, ok := findStatementPath(body.List, ret)
	if !ok {
		return lockNotAcquiredAtReturn, false
	}
	for k, frame := range slices.Backward(path) {
		switch current := frame.list[frame.index].(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			// An earlier iteration may have left the lock in any state.
			return lockNotAcquiredAtReturn, false
		case *ast.IfStmt:
			if k+1 < len(path) && sameStatementList(path[k+1].list, current.Body.List) &&
				l.handOverGuard(current, frame.list, frame.index, mutexName, pattern) {
				return lockNotHandedOverAtReturn, true
			}
		}
		for j := frame.index - 1; j >= 0; j-- {
			stmt := frame.list[j]
			if method, direct := directLockEffect(stmt, mutexName); direct {
				switch {
				case slices.Contains(pattern.LockMethods, method):
					return lockHeldAtReturn, true
				case slices.Contains(pattern.UnlockMethods, method):
					return lockReleasedAtReturn, true
				default:
					return lockNotAcquiredAtReturn, false
				}
			}
			switch l.wrapperLockEffect(stmt, mutexName, pattern) {
			case wrapperAcquires:
				return lockHeldAtReturn, true
			case wrapperReleases:
				return lockReleasedAtReturn, true
			case wrapperUncertain:
				return lockNotAcquiredAtReturn, false
			}
			if ifStmt, ok := stmt.(*ast.IfStmt); ok && ifStmt.Else == nil && blockEndsInReturn(ifStmt.Body) {
				if l.handOverGuard(ifStmt, frame.list, j, mutexName, pattern) {
					return lockHeldAtReturn, true
				}
				if (ifStmt.Init != nil && statementContainsLockEffect(ifStmt.Init, mutexName)) ||
					statementContainsLockEffect(&ast.ExprStmt{X: ifStmt.Cond}, mutexName) {
					return lockNotAcquiredAtReturn, false
				}
				continue
			}
			if statementContainsLockEffect(stmt, mutexName) {
				return lockNotAcquiredAtReturn, false
			}
		}
	}
	return lockNotAcquiredAtReturn, true
}

// handOverGuard reports whether guard, the statement at index in list, is the
// failure check of an acquire-or-fail helper handing mutexName over: its body
// is the failure arm and ends in a return, and the helper call is in its init,
// is its (negated) condition, or is assigned by the statement right before it.
//
//	if err := e.lockAlive(); err != nil { return err }
//	if !e.lockIfAlive() { return false }
//	err := e.lockAlive()
//	if err != nil { return err }
func (l *lifecycleResolver) handOverGuard(guard *ast.IfStmt, list []ast.Stmt, index int, mutexName string, pattern LockPattern) bool {
	if guard.Else != nil || !blockEndsInReturn(guard.Body) {
		return false
	}
	if guard.Init != nil {
		assign, ok := guard.Init.(*ast.AssignStmt)
		return ok && l.assignmentFailsIntoThen(assign, guard.Cond, mutexName, pattern)
	}
	if unary, ok := common.UnwrapParenExpr(guard.Cond).(*ast.UnaryExpr); ok && unary.Op == token.NOT {
		if call, ok := common.UnwrapParenExpr(unary.X).(*ast.CallExpr); ok {
			kind, handsOver := l.callAcquiresOnSuccess(call, mutexName, pattern)
			return handsOver && kind == boolSuccessResult
		}
	}
	return l.assignmentGuardedBy(list, index, mutexName, pattern)
}

// assignmentGuardedBy reports whether list[index] is the guard (see guardAfter)
// of an earlier assignment of a helper call handing mutexName over, whose
// failure takes the guard's then arm. The statements in between must leave the
// lock alone: the state is read off the guard, not off them.
func (l *lifecycleResolver) assignmentGuardedBy(list []ast.Stmt, index int, mutexName string, pattern LockPattern) bool {
	for j := index - 1; j >= 0; j-- {
		stmt := list[j]
		if assign, ok := stmt.(*ast.AssignStmt); ok && len(assign.Rhs) == 1 {
			if call, ok := common.UnwrapParenExpr(assign.Rhs[0]).(*ast.CallExpr); ok {
				if kind, handsOver := l.callAcquiresOnSuccess(call, mutexName, pattern); handsOver {
					name := assignedSuccessResult(assign, call, resultCount(l.calledFuncDecl(call)))
					guard, failureOnThen, ok := guardAfter(list, j, name, kind)
					return ok && failureOnThen && guard == list[index]
				}
			}
		}
		if !isPlainStatement(stmt) || statementContainsLockEffect(stmt, mutexName) {
			return false
		}
	}
	return false
}

// assignmentFailsIntoThen reports whether assign stores the success result of a
// helper call handing mutexName over, and cond sends its failure to the then
// arm.
func (l *lifecycleResolver) assignmentFailsIntoThen(assign *ast.AssignStmt, cond ast.Expr, mutexName string, pattern LockPattern) bool {
	if len(assign.Rhs) != 1 {
		return false
	}
	call, ok := common.UnwrapParenExpr(assign.Rhs[0]).(*ast.CallExpr)
	if !ok {
		return false
	}
	kind, handsOver := l.callAcquiresOnSuccess(call, mutexName, pattern)
	if !handsOver {
		return false
	}
	name := assignedSuccessResult(assign, call, resultCount(l.calledFuncDecl(call)))
	if name == "" {
		return false
	}
	failureOnThen, ok := failureBranch(cond, name, kind)
	return ok && failureOnThen
}

// guardAfter returns the `if` that checks name, the success result list[index]
// assigns, and whether its then arm handles failure. The guard is the first
// statement after the assignment that is an `if` testing name, past plain
// statements that leave name alone (see isPlainStatement), as in
//
//	err = e.lockAlive()
//	stats.waitingForLock.End(err == nil)
//	if err != nil { return err }
//
// ok is false when other control flow or a new assignment to name comes first.
func guardAfter(list []ast.Stmt, index int, name string, kind successResult) (*ast.IfStmt, bool, bool) {
	for _, stmt := range list[index+1:] {
		if guard, ok := stmt.(*ast.IfStmt); ok {
			if guard.Init != nil {
				return nil, false, false
			}
			failureOnThen, ok := failureBranch(guard.Cond, name, kind)
			return guard, failureOnThen, ok
		}
		if !isPlainStatement(stmt) || statementAssignsIdent(stmt, name) {
			return nil, false, false
		}
	}
	return nil, false, false
}

// isPlainStatement reports whether stmt runs straight through: no branch, loop,
// return, goroutine or deferred call of its own.
func isPlainStatement(stmt ast.Stmt) bool {
	switch stmt.(type) {
	case *ast.ExprStmt, *ast.AssignStmt, *ast.IncDecStmt, *ast.DeclStmt, *ast.SendStmt, *ast.EmptyStmt:
		return true
	default:
		return false
	}
}

// sameStatementList reports whether two non-empty lists share their backing
// array, i.e. are the same block's statements.
func sameStatementList(a, b []ast.Stmt) bool {
	return len(a) > 0 && len(b) > 0 && &a[0] == &b[0]
}

func blockEndsInReturn(block *ast.BlockStmt) bool {
	if block == nil || len(block.List) == 0 {
		return false
	}
	_, ok := block.List[len(block.List)-1].(*ast.ReturnStmt)
	return ok
}

// handOverCall is a call to a method of this package that reports success
// through its last result, the only kind of call that can hand a lock over.
// bases are the names the callee's receiver goes by in the caller (see
// receiverBases).
type handOverCall struct {
	callee         *ast.FuncDecl
	calleeReceiver string
	bases          []string
	kind           successResult
}

// handOverCallOf resolves call as a possible hand-over, x.helper(...), without
// yet deciding which lock, if any, the helper hands over.
func (l *lifecycleResolver) handOverCallOf(call *ast.CallExpr) (handOverCall, bool) {
	sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return handOverCall{}, false
	}
	callee := l.calledFuncDecl(call)
	if callee == nil || callee.Body == nil {
		return handOverCall{}, false
	}
	target := handOverCall{
		callee:         callee,
		calleeReceiver: common.ReceiverName(callee),
		bases:          l.receiverBases(sel),
		kind:           l.successResultOf(callee),
	}
	if target.calleeReceiver == "" || len(target.bases) == 0 || target.kind == noSuccessResult {
		return handOverCall{}, false
	}
	return target, true
}

// receiverBases returns the names the receiver of the method call sel goes by
// in the caller: as written, and first, when the method is promoted from an
// embedded field, the explicit path to that field. h.lockOpen() on a type that
// embeds *session reaches session's lock as h.session.mu, which the caller may
// equally write h.mu.
func (l *lifecycleResolver) receiverBases(sel *ast.SelectorExpr) []string {
	written := common.GetVarName(sel.X)
	if written == "?" {
		return nil
	}
	if l.typesInfo == nil {
		return []string{written}
	}
	selection := l.typesInfo.Selections[sel]
	if selection == nil || selection.Kind() != types.MethodVal || len(selection.Index()) < 2 {
		return []string{written}
	}
	var explicit strings.Builder
	explicit.WriteString(written)
	typ := selection.Recv()
	for _, index := range selection.Index()[:len(selection.Index())-1] {
		fields, ok := structFields(typ)
		if !ok || index >= fields.NumFields() {
			return []string{written}
		}
		explicit.WriteString("." + fields.Field(index).Name())
		typ = fields.Field(index).Type()
	}
	return []string{explicit.String(), written}
}

// structFields returns the struct behind typ, through one pointer.
func structFields(typ types.Type) (*types.Struct, bool) {
	if ptr, ok := types.Unalias(typ).(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	fields, ok := typ.Underlying().(*types.Struct)
	return fields, ok
}

// calleeLockFor maps mutexName, a lock named from the caller's side, to the
// callee's receiver through one of the call's receiver bases.
func calleeLockFor(target handOverCall, mutexName string) (string, bool) {
	for _, base := range target.bases {
		if path, ok := receiverRelativePath(mutexName, base); ok {
			return joinMutexPath(target.calleeReceiver, path), true
		}
	}
	return "", false
}

// handsOver reports whether the call hands mutexName, a lock named from the
// caller's side, over on success (see handsLockToCaller).
func (l *lifecycleResolver) handsOver(target handOverCall, mutexName string, pattern LockPattern) bool {
	calleeLock, ok := calleeLockFor(target, mutexName)
	return ok && l.handsLockToCaller(target.callee, calleeLock, pattern)
}

// callAcquiresOnSuccess reports whether call leaves mutexName, a lock named
// from the caller's side, held exactly when it reports success (see
// acquiresOnSuccess), and how the callee reports success.
func (l *lifecycleResolver) callAcquiresOnSuccess(call *ast.CallExpr, mutexName string, pattern LockPattern) (successResult, bool) {
	target, ok := l.handOverCallOf(call)
	if !ok {
		return noSuccessResult, false
	}
	calleeLock, ok := calleeLockFor(target, mutexName)
	if !ok || !l.acquiresOnSuccess(target.callee, calleeLock, pattern) {
		return noSuccessResult, false
	}
	return target.kind, true
}

// callsPossibleAcquirer reports whether body calls, on the owner of mutexName,
// a method of this package that may take the lock: a wrapper that acquires it
// (e.lock()) or a method reporting success, which may be a helper handing it
// over.
func (l *lifecycleResolver) callsPossibleAcquirer(body *ast.BlockStmt, mutexName string, pattern LockPattern) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if method, path, ok := l.ownerMethodCall(call, mutexName); ok {
			found = l.successResultOf(method) != noSuccessResult ||
				functionBodyContainsFieldCall(method.Body, joinMutexPath(common.ReceiverName(method), path), pattern.LockMethods)
		}
		return !found
	})
	return found
}

// ownerMethodCall resolves call as x.m(...), a method of this package called
// on the owner of mutexName, and returns m with the lock's path below x.
func (l *lifecycleResolver) ownerMethodCall(call *ast.CallExpr, mutexName string) (*ast.FuncDecl, string, bool) {
	sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return nil, "", false
	}
	for _, base := range l.receiverBases(sel) {
		path, ok := receiverRelativePath(mutexName, base)
		if !ok {
			continue
		}
		method := l.calledFuncDecl(call)
		if method == nil || method.Body == nil || common.ReceiverName(method) == "" {
			return nil, "", false
		}
		return method, path, true
	}
	return nil, "", false
}

// wrapperEffect classifies a statement that calls a method on a lock's owner.
type wrapperEffect int

const (
	noWrapperEffect wrapperEffect = iota
	wrapperAcquires
	wrapperReleases
	wrapperUncertain
)

// wrapperLockEffect reports what a call statement `x.m()` does to mutexName
// through the body of m: only acquire it (e.lock()), only release it
// (e.unlock()), or both, which makes the lock state after it unknown (a
// helper whose result is ignored, or a method that drops and retakes it).
func (l *lifecycleResolver) wrapperLockEffect(stmt ast.Stmt, mutexName string, pattern LockPattern) wrapperEffect {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return noWrapperEffect
	}
	call, ok := common.UnwrapParenExpr(exprStmt.X).(*ast.CallExpr)
	if !ok {
		return noWrapperEffect
	}
	method, path, ok := l.ownerMethodCall(call, mutexName)
	if !ok {
		return noWrapperEffect
	}
	target := joinMutexPath(common.ReceiverName(method), path)
	acquires := functionBodyContainsFieldCall(method.Body, target, anyAcquireMethods)
	releases := functionBodyContainsFieldCall(method.Body, target, anyReleaseMethods)
	switch {
	case acquires == releases:
		if acquires {
			return wrapperUncertain
		}
		return noWrapperEffect
	case acquires && functionBodyContainsFieldCall(method.Body, target, pattern.LockMethods):
		return wrapperAcquires
	case releases && functionBodyContainsFieldCall(method.Body, target, pattern.UnlockMethods):
		return wrapperReleases
	default:
		// The other lock mode: this contract says nothing about it.
		return wrapperUncertain
	}
}
