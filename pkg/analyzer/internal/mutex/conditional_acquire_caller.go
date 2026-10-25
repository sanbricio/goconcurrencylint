package mutex

import (
	"go/ast"
	"go/token"
	"maps"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/primitives"
	"golang.org/x/tools/go/analysis"
)

// handedOverLock is a tracked lock that a call to an acquire-or-fail helper
// leaves held when the call reports success.
type handedOverLock struct {
	varName string
	method  string // Lock or RLock
}

// handOverModes pairs each lock mode with the acquisition it hands over.
var handOverModes = []struct {
	pattern LockPattern
	method  string
}{
	{WriteLockPattern, "Lock"},
	{ReadLockPattern, "RLock"},
}

// locksHandedOverBy returns the tracked locks that call leaves held when it
// reports success, along with how the callee reports success and how many
// results it has. The callee must hand its lock to callers (see
// handsLockToCaller); a helper whose lock travels in a returned handle instead
// has no caller releasing it, so it never qualifies.
func (c *Checker) locksHandedOverBy(call *ast.CallExpr) ([]handedOverLock, successResult, int) {
	target, ok := c.lifecycle.handOverCallOf(call)
	if !ok || target.callee == c.function {
		return nil, noSuccessResult, 0
	}

	var locks []handedOverLock
	collect := func(names map[string]bool, rw bool) {
		for name := range names {
			for _, mode := range handOverModes {
				if mode.method == "RLock" && !rw {
					continue
				}
				if c.lifecycle.handsOver(target, name, mode.pattern) {
					locks = append(locks, handedOverLock{varName: name, method: mode.method})
				}
			}
		}
	}
	collect(c.mutexNames, false)
	collect(c.rwMutexNames, true)
	return locks, target.kind, resultCount(target.callee)
}

// pendingAcquisition is a lock handed over by a helper call on success only.
// It waits for the `if` that checks the call's result and lands on the arm
// where the call succeeded.
type pendingAcquisition struct {
	lock          handedOverLock
	pos           token.Pos
	successOnThen bool
}

// applyConditionalAcquireCall models `err := e.lockAlive()` in the caller: the
// call leaves the lock held only when it reports success, so the lock is not
// counted at the assignment but on the success arm of the `if` that checks the
// result (see guardAfterAssignment). Like a TryLock condition, the lock is then
// held only where the helper really holds it; a result that is not checked
// that way leaves the caller's state alone.
func (c *Checker) applyConditionalAcquireCall(stmt *ast.AssignStmt) {
	if c.funcAnalysis == nil || len(stmt.Rhs) != 1 {
		return
	}
	call, ok := common.UnwrapParenExpr(stmt.Rhs[0]).(*ast.CallExpr)
	if !ok {
		return
	}
	locks, kind, count := c.locksHandedOverBy(call)
	if len(locks) == 0 {
		return
	}
	resultName := assignedSuccessResult(stmt, call, count)
	if resultName == "" {
		return
	}
	guard, failureOnThen, ok := c.guardAfterAssignment(stmt, resultName, kind)
	if !ok {
		return
	}
	for _, lock := range locks {
		c.pendingAcquisitions[guard] = append(c.pendingAcquisitions[guard], pendingAcquisition{
			lock:          lock,
			pos:           stmt.Pos(),
			successOnThen: !failureOnThen,
		})
	}
}

// applyPendingAcquisitions lands the locks recorded for stmt by
// applyConditionalAcquireCall on its success arm and reports whether there were
// any. They are consumed: the assignment records them again whenever it is
// analyzed again.
func (c *Checker) applyPendingAcquisitions(stmt *ast.IfStmt, thenStats, elseStats map[string]*Stats) bool {
	pending := c.pendingAcquisitions[stmt]
	if len(pending) == 0 {
		return false
	}
	delete(c.pendingAcquisitions, stmt)
	for _, acquisition := range pending {
		target := elseStats
		if acquisition.successOnThen {
			target = thenStats
		}
		acquireHandedOverLock(target[acquisition.lock.varName], acquisition.lock.method, acquisition.pos)
	}
	return true
}

// conditionHandedOverLocks returns the locks a bool acquire-or-fail helper
// called as a condition leaves held on its then arm: `if e.lockIfAlive()`
// acquires like `if mu.TryLock()`.
func (c *Checker) conditionHandedOverLocks(cond ast.Expr) (*ast.CallExpr, []handedOverLock) {
	call, ok := common.UnwrapParenExpr(cond).(*ast.CallExpr)
	if !ok {
		return nil, nil
	}
	locks, kind, _ := c.locksHandedOverBy(call)
	if kind != boolSuccessResult {
		return nil, nil
	}
	return call, locks
}

// applyAssertedHandOver is applyAssertedTryLock for acquire-or-fail helpers:
// require.NoError(t, e.lockAlive()) and require.True(t, e.lockIfAlive()) only
// return when the helper succeeded, so its lock is held afterwards.
func (c *Checker) applyAssertedHandOver(call *ast.CallExpr, stats map[string]*Stats) bool {
	if len(call.Args) < 2 || c.typesInfo == nil {
		return false
	}
	sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	var asserted successResult
	switch sel.Sel.Name {
	case "NoError", "NoErrorf":
		asserted = errorSuccessResult
	case "True", "Truef":
		asserted = boolSuccessResult
	default:
		return false
	}
	obj := c.typesInfo.ObjectOf(sel.Sel)
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != "github.com/stretchr/testify/require" {
		return false
	}
	helperCall, ok := common.UnwrapParenExpr(call.Args[1]).(*ast.CallExpr)
	if !ok {
		return false
	}
	locks, kind, _ := c.locksHandedOverBy(helperCall)
	if len(locks) == 0 || kind != asserted {
		return false
	}
	for _, lock := range locks {
		acquireHandedOverLock(stats[lock.varName], lock.method, helperCall.Pos())
	}
	return true
}

// acquireHandedOverLock counts a lock a helper acquired for the caller. Like a
// direct Lock, it first settles an earlier unlock of a lock the caller was
// handed itself (unlock, slow work, take it back through the helper).
func acquireHandedOverLock(stat *Stats, method string, pos token.Pos) {
	if stat == nil {
		return
	}
	if method == "RLock" {
		if stat.borrowedRLock > 0 {
			stat.borrowedRLock--
			stat.removeFirstBorrowedRUnlockPos()
			return
		}
		stat.rlock++
		stat.rlockPos = append(stat.rlockPos, pos)
		return
	}
	if stat.borrowedLock > 0 {
		stat.borrowedLock--
		stat.removeFirstBorrowedUnlockPos()
		return
	}
	stat.lock++
	stat.lockPos = append(stat.lockPos, pos)
}

// withHandedOverLocks returns fr extended with the locks fn takes through
// acquire-or-fail helpers of this package (see handsLockToCaller). A caller
// that only calls e.lockAlive() and e.unlock() names no mutex, so without this
// it would not be analyzed at all and a path that forgets e.unlock() would go
// unreported. primitives caches and shares fr, so it is copied, never modified.
func (s *packageScope) withHandedOverLocks(fn *ast.FuncDecl, fr *primitives.FunctionResult, pass *analysis.Pass) *primitives.FunctionResult {
	if fn.Body == nil {
		return fr
	}
	l := s.handOverResolverFor(pass)

	extended := fr
	// track adds the lock under the first of its spellings (the explicit one
	// for a promoted call) unless the caller already knows it under any.
	track := func(spellings []string, rw bool) {
		for _, name := range spellings {
			if extended.Mutexes[name] || extended.RWMutexes[name] {
				return
			}
		}
		name := spellings[0]
		if extended == fr {
			clone := *fr
			clone.Mutexes = maps.Clone(fr.Mutexes)
			clone.RWMutexes = maps.Clone(fr.RWMutexes)
			extended = &clone
		}
		if rw {
			extended.RWMutexes[name] = true
		} else {
			extended.Mutexes[name] = true
		}
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		target, ok := l.handOverCallOf(call)
		if !ok || target.callee == fn {
			return true
		}
		for path, rw := range s.typeLockPaths(common.ReceiverTypeName(target.callee), pass) {
			for _, mode := range handOverModes {
				if (mode.method == "RLock" && !rw) || !l.handsLockToCaller(target.callee, joinMutexPath(target.calleeReceiver, path), mode.pattern) {
					continue
				}
				spellings := make([]string, 0, len(target.bases))
				for _, base := range target.bases {
					spellings = append(spellings, joinMutexPath(base, path))
				}
				track(spellings, rw)
			}
		}
		return true
	})
	return extended
}

// typeLockPaths returns the mutexes the methods of typeName use on their
// receiver, as paths below it ("" for a mutex the type embeds), mapped to
// whether each is an RWMutex. Any method of the type may hand one of them over,
// directly or through the type's own wrappers and helpers, so these are the
// locks worth asking handsLockToCaller about. Reading them off the whole type,
// rather than off what each method calls, keeps the answer independent of the
// order methods are visited in.
func (s *packageScope) typeLockPaths(typeName string, pass *analysis.Pass) map[string]bool {
	if cached, ok := s.lockPathsByType[typeName]; ok {
		return cached
	}
	if s.lockPathsByType == nil {
		s.lockPathsByType = make(map[string]map[string]bool)
	}
	pkg := pass.ResultOf[primitives.Analyzer].(*primitives.Result)
	paths := make(map[string]bool)
	for _, method := range s.receiverMethods[typeName] {
		receiver := common.ReceiverName(method)
		if receiver == "" || method.Body == nil {
			continue
		}
		fr := primitives.ForFunction(method, pass, pkg)
		for name := range fr.Mutexes {
			if path, ok := receiverRelativePath(name, receiver); ok {
				paths[path] = false
			}
		}
		for name := range fr.RWMutexes {
			if path, ok := receiverRelativePath(name, receiver); ok {
				paths[path] = true
			}
		}
	}
	s.lockPathsByType[typeName] = paths
	return paths
}

// handOverResolverFor returns the package-level resolver used before any
// function is analyzed.
func (s *packageScope) handOverResolverFor(pass *analysis.Pass) *lifecycleResolver {
	if s.handOverResolver == nil {
		s.handOverResolver = newLifecycleResolver(s.receiverMethods, s.functions, pass.TypesInfo, s.explicitTransferCache, s.scanCache, nil)
	}
	return s.handOverResolver
}
