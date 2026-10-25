package mutex

import (
	"go/ast"
	"go/token"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// lockEvent is the first thing a caller does to a lock after calling a helper.
type lockEvent int

const (
	noLockEvent lockEvent = iota
	lockEventAcquire
	lockEventRelease
)

// callersTakeOverLock reports whether fn's callers take its lock over on the
// success path: a caller releases it after the call, directly or at the end of
// a chain of callers that pass it on (each keeping the contract itself, see
// passesLockOn), and no direct caller acquires it there first, which would
// self-deadlock if fn held it. Only a release ends a chain: helpers that merely
// pass the lock to each other confirm nothing. What a caller does in the branch
// that handles fn's failure is irrelevant, since fn released the lock there
// itself.
func (l *lifecycleResolver) callersTakeOverLock(fn *ast.FuncDecl, receiverType, path string, pattern LockPattern, kind successResult) bool {
	methodEvents := make(map[methodEventKey]lockEvent)
	visited := map[*ast.FuncDecl]bool{fn: true}
	queue := []lockHop{{fn: fn, receiverType: receiverType, path: path, kind: kind}}
	for len(queue) > 0 {
		hop := queue[0]
		queue = queue[1:]
		released, contradicted := false, false
		var onward []lockHop
		for _, site := range l.callSitesNamed(hop.fn.Name.Name) {
			if site.enclosing == nil || site.enclosing == hop.fn || site.enclosing.Body == nil {
				continue
			}
			// Promoted calls count too: the helper's receiver is then an
			// embedded field of what the caller calls it on.
			sel, ok := common.UnwrapParenExpr(site.call.Fun).(*ast.SelectorExpr)
			if !ok || l.calledFuncDecl(site.call) != hop.fn {
				continue
			}
			bases := l.receiverBases(sel)
			if len(bases) == 0 {
				continue
			}
			skip := failureArmAfterCall(site.enclosing.Body, site.call, hop.kind, resultCount(hop.fn))
			switch l.firstLockEventAfter(site, skip, hop.fn, hop.receiverType, bases, hop.path, pattern, methodEvents) {
			case lockEventAcquire:
				contradicted = true
			case lockEventRelease:
				released = true
			default:
				if next, ok := l.passesLockOn(site.enclosing, joinMutexPath(bases[0], hop.path), pattern); ok && !visited[next.fn] {
					visited[next.fn] = true
					onward = append(onward, next)
				}
			}
		}
		switch {
		case contradicted && hop.fn == fn:
			return false
		case contradicted:
			// The callers of this link disagree: nothing is learned through it.
		case released:
			return true
		default:
			queue = append(queue, onward...)
		}
	}
	return false
}

// lockHop is one function on a chain of callers that pass a helper's lock on:
// the lock is path under its receiver.
type lockHop struct {
	fn           *ast.FuncDecl
	receiverType string
	path         string
	kind         successResult
}

// passesLockOn reports whether caller, having done nothing with lockName after
// a successful call, hands it on to its own callers: the lock sits under its
// receiver and caller keeps the acquire-or-fail contract for it.
func (l *lifecycleResolver) passesLockOn(caller *ast.FuncDecl, lockName string, pattern LockPattern) (lockHop, bool) {
	receiver := common.ReceiverName(caller)
	receiverType := common.ReceiverTypeName(caller)
	if receiver == "" || receiverType == "" {
		return lockHop{}, false
	}
	path, ok := receiverRelativePath(lockName, receiver)
	if !ok {
		return lockHop{}, false
	}
	kind := l.successResultOf(caller)
	if kind == noSuccessResult || !l.acquiresOnSuccess(caller, lockName, pattern) {
		return lockHop{}, false
	}
	return lockHop{fn: caller, receiverType: receiverType, path: path, kind: kind}, true
}

// firstLockEventAfter returns the first acquire or release of the helper's lock
// on the paths that go on after the call: the rest of the statement holding it
// (minus the skipped failure arm), then the statements that follow in its block
// and in each enclosing block, up to the function or function literal the call
// sits in. Sibling branches never run after the call; a return, or a call that
// ends execution, stops the scan, while break, continue and goto leave the
// block. A call to a method of the helper's type counts when that method only
// releases or only acquires the lock.
func (l *lifecycleResolver) firstLockEventAfter(site methodCallSite, skip ast.Node, helper *ast.FuncDecl, receiverType string, bases []string, path string, pattern LockPattern, methodEvents map[methodEventKey]lockEvent) lockEvent {
	targets := make([]string, 0, len(bases))
	for _, base := range bases {
		targets = append(targets, joinMutexPath(base, path))
	}
	eventIn := func(n ast.Node, after token.Pos) lockEvent {
		first, firstPos := noLockEvent, token.NoPos
		record := func(event lockEvent, pos token.Pos) {
			if event != noLockEvent && (firstPos == token.NoPos || pos < firstPos) {
				first, firstPos = event, pos
			}
		}
		ast.Inspect(n, func(n ast.Node) bool {
			if n == nil || n == skip || n.End() <= after {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || call.Pos() < after {
				return true
			}
			sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr)
			if !ok {
				return true
			}
			name := common.GetVarName(sel.X)
			switch {
			case slices.Contains(targets, name) && slices.Contains(pattern.UnlockMethods, sel.Sel.Name):
				record(lockEventRelease, call.Pos())
			case slices.Contains(targets, name) && slices.Contains(anyAcquireMethods, sel.Sel.Name):
				record(lockEventAcquire, call.Pos())
			case slices.Contains(bases, name):
				if method := l.receiverMethods[receiverType][sel.Sel.Name]; method != nil {
					record(l.methodLockEvent(method, helper, path, pattern, methodEvents), call.Pos())
				}
			}
			return true
		})
		return first
	}

	frames := statementPathToCall(site.enclosing.Body.List, site.call)
	if len(frames) == 0 {
		return noLockEvent
	}
	term := newTerminationAnalyzer(l.typesInfo)
	holder := frames[len(frames)-1]
	if event := eventIn(holder.list[holder.index], site.call.End()); event != noLockEvent {
		return event
	}
	if exitsFunction(term, holder.list[holder.index]) {
		return noLockEvent
	}
	for _, frame := range slices.Backward(frames) {
		for _, next := range frame.list[frame.index+1:] {
			if event := eventIn(next, token.NoPos); event != noLockEvent {
				return event
			}
			if exitsFunction(term, next) {
				return noLockEvent
			}
			if term.statementAlwaysTerminates(next) {
				break // break, continue or goto: control leaves this block
			}
		}
	}
	return noLockEvent
}

// statementPathToCall returns the statements leading to the innermost one that
// holds call, one frame per nesting level, as findStatementPath does for a
// statement. A call inside a function literal is traced from the literal's own
// body: what follows the literal in the enclosing function does not follow the
// call.
func statementPathToCall(list []ast.Stmt, call *ast.CallExpr) []statementPathFrame {
	for index, stmt := range list {
		if stmt.Pos() > call.Pos() || call.End() > stmt.End() {
			continue
		}
		frame := statementPathFrame{list: list, index: index}
		for _, children := range statementChildLists(stmt) {
			if inner := statementPathToCall(children, call); inner != nil {
				return append([]statementPathFrame{frame}, inner...)
			}
		}
		if lit := funcLitAround(stmt, call); lit != nil {
			return statementPathToCall(lit.Body.List, call)
		}
		return []statementPathFrame{frame}
	}
	return nil
}

// funcLitAround returns the outermost function literal in n that contains call.
func funcLitAround(n ast.Node, call *ast.CallExpr) *ast.FuncLit {
	var found *ast.FuncLit
	ast.Inspect(n, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if lit, ok := n.(*ast.FuncLit); ok && lit.Body != nil && lit.Pos() <= call.Pos() && call.End() <= lit.End() {
			found = lit
			return false
		}
		return true
	})
	return found
}

// exitsFunction reports whether stmt always leaves the function: a return, a
// call that ends execution, or an if whose every arm does.
func exitsFunction(term *terminationAnalyzer, stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.ExprStmt:
		call, ok := common.UnwrapParenExpr(s.X).(*ast.CallExpr)
		return ok && term.callTerminatesExecution(call)
	case *ast.BlockStmt:
		return len(s.List) > 0 && exitsFunction(term, s.List[len(s.List)-1])
	case *ast.IfStmt:
		return s.Else != nil && exitsFunction(term, s.Body) && exitsFunction(term, s.Else)
	default:
		return false
	}
}

// methodEventKey caches methodLockEvent per method and lock path.
type methodEventKey struct {
	method *ast.FuncDecl
	path   string
}

// methodLockEvent classifies a call to a method of the helper's type by what its
// own body does to the lock. Calling the helper again acquires it.
func (l *lifecycleResolver) methodLockEvent(method, helper *ast.FuncDecl, path string, pattern LockPattern, cache map[methodEventKey]lockEvent) lockEvent {
	if method == helper {
		return lockEventAcquire
	}
	key := methodEventKey{method: method, path: path}
	if event, ok := cache[key]; ok {
		return event
	}
	event := noLockEvent
	if receiver := common.ReceiverName(method); receiver != "" && method.Body != nil {
		target := joinMutexPath(receiver, path)
		releases := functionBodyContainsFieldCall(method.Body, target, pattern.UnlockMethods)
		acquires := functionBodyContainsFieldCall(method.Body, target, anyAcquireMethods)
		switch {
		case releases && !acquires:
			event = lockEventRelease
		case acquires && !releases:
			event = lockEventAcquire
		}
	}
	cache[key] = event
	return event
}

// failureArmAfterCall returns the branch a caller runs when call reports
// failure: the arm of the `if` that checks the call's success result, either in
// the `if` header itself or after the assignment (see guardAfter). It returns
// nil when the result is not checked that way.
func failureArmAfterCall(body *ast.BlockStmt, call *ast.CallExpr, kind successResult, count int) ast.Node {
	var arm ast.Node
	ast.Inspect(body, func(n ast.Node) bool {
		if arm != nil {
			return false
		}
		list := statementListOf(n)
		for index, stmt := range list {
			if ifStmt, ok := stmt.(*ast.IfStmt); ok {
				if name := assignedSuccessResult(ifStmt.Init, call, count); name != "" {
					arm = failureArm(ifStmt, name, kind)
				} else if kind == boolSuccessResult {
					arm = failureArmForCallCondition(ifStmt, call)
				}
			} else if name := assignedSuccessResult(stmt, call, count); name != "" {
				if guard, failureOnThen, ok := guardAfter(list, index, name, kind); ok {
					arm = ifArm(guard, failureOnThen)
				}
			}
			if arm != nil {
				return false
			}
		}
		return true
	})
	return arm
}

// assignedSuccessResult returns the variable that receives call's success
// result when stmt assigns it.
func assignedSuccessResult(stmt ast.Stmt, call *ast.CallExpr, count int) string {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok {
		return ""
	}
	index := -1
	switch {
	case len(assign.Rhs) == 1 && common.UnwrapParenExpr(assign.Rhs[0]) == call && len(assign.Lhs) == count:
		index = count - 1
	case count == 1 && len(assign.Rhs) == len(assign.Lhs):
		index = slices.IndexFunc(assign.Rhs, func(rhs ast.Expr) bool { return common.UnwrapParenExpr(rhs) == call })
	}
	if index < 0 {
		return ""
	}
	ident, ok := common.UnwrapParenExpr(assign.Lhs[index]).(*ast.Ident)
	if !ok || ident.Name == "_" {
		return ""
	}
	return ident.Name
}

// failureArm returns the arm of stmt taken when the success result held in name
// reports failure, or nil when the condition does not test it.
func failureArm(stmt *ast.IfStmt, name string, kind successResult) ast.Node {
	failureOnThen, ok := failureBranch(stmt.Cond, name, kind)
	if !ok {
		return nil
	}
	return ifArm(stmt, failureOnThen)
}

// failureBranch reports whether cond, testing the success result held in name,
// takes its then arm on failure. ok is false when cond does not test it.
func failureBranch(cond ast.Expr, name string, kind successResult) (failureOnThen, ok bool) {
	switch kind {
	case errorSuccessResult:
		return nilComparisonFailureBranch(cond, name)
	case boolSuccessResult:
		return boolIdentFailureBranch(cond, name)
	default:
		return false, false
	}
}

// failureArmForCallCondition handles a bool helper called in the condition
// itself: `if !h.lock() { ... }` or `if h.lock() { ... } else { ... }`.
func failureArmForCallCondition(stmt *ast.IfStmt, call *ast.CallExpr) ast.Node {
	cond := common.UnwrapParenExpr(stmt.Cond)
	if cond == call {
		return ifArm(stmt, false)
	}
	if unary, ok := cond.(*ast.UnaryExpr); ok && unary.Op == token.NOT && common.UnwrapParenExpr(unary.X) == call {
		return ifArm(stmt, true)
	}
	return nil
}

func boolIdentFailureBranch(cond ast.Expr, name string) (bool, bool) {
	cond = common.UnwrapParenExpr(cond)
	if ident, ok := cond.(*ast.Ident); ok && ident.Name == name {
		return false, true
	}
	if unary, ok := cond.(*ast.UnaryExpr); ok && unary.Op == token.NOT {
		if ident, ok := common.UnwrapParenExpr(unary.X).(*ast.Ident); ok && ident.Name == name {
			return true, true
		}
	}
	return false, false
}

func ifArm(stmt *ast.IfStmt, then bool) ast.Node {
	if then {
		return stmt.Body
	}
	if stmt.Else == nil {
		return nil
	}
	return stmt.Else
}
