package mutex

import (
	"go/ast"
	"go/token"
	"slices"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// An acquire-or-fail helper may lock an object it is handed rather than its
// receiver:
//
//	func (s *Store) freeze(b *Bucket) error {
//		if s.closed { return errClosed }
//		b.flushLock.Lock()
//		return nil
//	}
//
//	if err := s.freeze(b); err != nil { return err }
//	defer b.flushLock.Unlock()
//
// The contract is read the same way as for a receiver lock (see
// acquiresOnSuccess); the lock is named through the argument at each call.
// Every caller must release it afterwards on its own path, which is what
// tells the helper from one that forgot to unlock.

// lockRoot returns the variable a lock path starts from.
func lockRoot(mutexName string) string {
	root, _, _ := strings.Cut(mutexName, ".")
	return root
}

// parameterIndex returns the position of fn's parameter called name, or -1.
func parameterIndex(fn *ast.FuncDecl, name string) int {
	if fn == nil || fn.Type.Params == nil || name == "" {
		return -1
	}
	index := 0
	for _, field := range fn.Type.Params.List {
		if len(field.Names) == 0 {
			index++
			continue
		}
		for _, ident := range field.Names {
			if ident.Name == name {
				return index
			}
			index++
		}
	}
	return -1
}

// argumentLockFor maps mutexName, a lock below a parameter of callee, to the
// caller's spelling through the argument of call in that position.
func argumentLockFor(call *ast.CallExpr, callee *ast.FuncDecl, mutexName string) (string, bool) {
	root, path, ok := strings.Cut(mutexName, ".")
	if !ok || call.Ellipsis.IsValid() {
		return "", false
	}
	index := parameterIndex(callee, root)
	if index < 0 || index >= len(call.Args) {
		return "", false
	}
	base := common.GetVarName(call.Args[index])
	if base == "?" || base == "" {
		return "", false
	}
	return base + "." + path, true
}

// callersReleaseParamLockAfter reports whether fn hands mutexName, a lock
// below one of its parameters, to its callers on success, and every call of
// fn in the package releases that lock afterwards on its own path.
func (l *lifecycleResolver) callersReleaseParamLockAfter(fn *ast.FuncDecl, mutexName string, pattern LockPattern) bool {
	if fn == nil || parameterIndex(fn, lockRoot(mutexName)) < 0 || !l.acquiresOnSuccess(fn, mutexName, pattern) {
		return false
	}
	calls := 0
	for _, site := range l.callSitesOf(fn) {
		if site.enclosing == fn {
			continue
		}
		// A release in a closure the caller sets up afterwards (a deferred
		// once-guarded unlock) is the caller taking the lock over just as
		// well; only the caller's own flow needs it on its path.
		callerLock, ok := argumentLockFor(site.call, fn, mutexName)
		if !ok || !releasedLater(site.enclosing.Body, site.call.End(), callerLock, pattern.UnlockMethods) {
			return false
		}
		calls++
	}
	return calls > 0
}

// locksTakenThroughLockParams is locksHandedOverBy for a helper that locks an
// object passed to it (see callersReleaseParamLockAfter): the caller counts the
// lock as held on success when it releases it later on its own path.
func (c *Checker) locksTakenThroughLockParams(call *ast.CallExpr) ([]handedOverLock, successResult, int) {
	callee := c.lifecycle.calledFuncDecl(call)
	if callee == nil || callee == c.function || callee.Body == nil {
		return nil, noSuccessResult, 0
	}
	kind := c.lifecycle.successResultOf(callee)
	if kind == noSuccessResult {
		return nil, noSuccessResult, 0
	}

	var locks []handedOverLock
	collect := func(names map[string]bool, rw bool) {
		for name := range names {
			for index, arg := range call.Args {
				base := common.GetVarName(arg)
				path, ok := strings.CutPrefix(name, base+".")
				if base == "?" || !ok {
					continue
				}
				param := parameterName(callee, index)
				if param == "" {
					continue
				}
				calleeLock := param + "." + path
				for _, mode := range handOverModes {
					if mode.method == "RLock" && !rw {
						continue
					}
					if c.lifecycle.acquiresOnSuccess(callee, calleeLock, mode.pattern) &&
						releasedLaterOnOwnPath(c.function.Body, call.End(), name, mode.pattern.UnlockMethods) {
						locks = append(locks, handedOverLock{varName: name, method: mode.method})
					}
				}
			}
		}
	}
	collect(c.mutexNames, false)
	collect(c.rwMutexNames, true)
	if len(locks) == 0 {
		return nil, noSuccessResult, 0
	}
	return locks, kind, resultCount(callee)
}

// parameterName returns the name of fn's parameter at index, or "".
func parameterName(fn *ast.FuncDecl, index int) string {
	if fn.Type.Params == nil {
		return ""
	}
	position := 0
	for _, field := range fn.Type.Params.List {
		if len(field.Names) == 0 {
			position++
			continue
		}
		for _, ident := range field.Names {
			if position == index {
				if ident.Name == "_" {
					return ""
				}
				return ident.Name
			}
			position++
		}
	}
	return ""
}

// releasedLater reports whether body releases mutexName anywhere after pos,
// function literals included.
func releasedLater(body *ast.BlockStmt, pos token.Pos, mutexName string, unlockMethods []string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Pos() <= pos {
			return true
		}
		sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr)
		if ok && slices.Contains(unlockMethods, sel.Sel.Name) && common.GetVarName(sel.X) == mutexName {
			found = true
		}
		return !found
	})
	return found
}
