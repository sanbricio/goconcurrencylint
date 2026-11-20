package mutex

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A plain function can release a package-level lock that its caller holds, the
// same way a method releases its receiver's (applyLocalMethodLifecycleEffects):
//
//	var mu sync.Mutex
//
//	func release() { mu.Unlock() }
//
//	func update() {
//		mu.Lock()
//		...
//		release()
//	}
//
// A package-level variable is the same object in every function of its
// package, so the callee needs no parameter to name the lock: it spells it the
// same way, and the type checker says whether that spelling is the variable or
// a local that shadows it. Both sides of the split are modelled on the method
// rule: the caller simulates the callee on the lock it holds, and the callee's
// unlock without a lock is accepted when every caller in the package takes the
// lock before the call (computeCallerManagedReleaseFor).

// applyPackageLockReleaseEffect simulates callee on mutexName when mutexName is
// rooted at a package-level variable and callee releases that same variable.
func (c *Checker) applyPackageLockReleaseEffect(call *ast.CallExpr, callee *ast.FuncDecl, mutexName string, isRWMutex bool, stats map[string]*Stats) bool {
	if !c.packageLockReleasedBy(callee, mutexName) {
		return false
	}

	simulated := c.simulateMethodEffect(callee, mutexName, isRWMutex, stats[mutexName], c.callArgumentFacts(call, callee))
	if simulated == nil {
		return false
	}
	attributeToCall(simulated, stats[mutexName], call.Pos())
	stats[mutexName] = simulated
	return true
}

// packageLockReleasedBy reports whether mutexName is rooted at a package-level
// variable and callee releases that same variable under the same spelling.
func (c *Checker) packageLockReleasedBy(callee *ast.FuncDecl, mutexName string) bool {
	root := c.packageLockRoot(mutexName)
	if root == nil {
		return false
	}
	ref := packageLockRef{name: mutexName, root: root, info: c.typesInfo}
	return ref.releasedOnlyAsItselfIn(callee)
}

// packageLockRoot returns the package-level variable at the root of mutexName
// as the current function spells it, or nil when the name is local. The answer
// is fixed for the function, so it is computed once per name.
func (c *Checker) packageLockRoot(mutexName string) types.Object {
	if c.typesInfo == nil || c.function == nil || c.function.Body == nil {
		return nil
	}
	if root, ok := c.packageLockRoots[mutexName]; ok {
		return root
	}

	var root types.Object
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		if root != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !isSyncLockerMethod(sel.Sel.Name) || c.resolveLockAlias(common.GetVarName(sel.X)) != mutexName {
			return true
		}
		root = packageLevelRoot(c.typesInfo, sel.X)
		return root == nil
	})

	if c.packageLockRoots == nil {
		c.packageLockRoots = make(map[string]types.Object)
	}
	c.packageLockRoots[mutexName] = root
	return root
}

// releasedOnlyAsItselfIn reports whether fn releases this lock and every lock
// call fn makes under the same spelling is on this very variable. A local or a
// parameter named like the package variable would otherwise be simulated as
// if it were the caller's lock.
func (r packageLockRef) releasedOnlyAsItselfIn(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Body == nil {
		return false
	}

	releases, foreign := false, false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if foreign {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !isSyncLockerMethod(sel.Sel.Name) || common.GetVarName(sel.X) != r.name {
			return true
		}
		if packageLevelRoot(r.info, sel.X) != r.root {
			foreign = true
			return false
		}
		if containsMethod(argumentReleaseMethods, sel.Sel.Name) {
			releases = true
		}
		return true
	})
	return releases && !foreign
}

// isCallerManagedPackageReleaseFor reports whether the plain function under
// analysis releases a package-level lock on behalf of its callers: it never
// takes the lock itself, and every call to it in the package sits where the
// caller has taken the lock before.
func (l *lifecycleResolver) isCallerManagedPackageReleaseFor(mutexName string, acquireMethods []string) bool {
	fn := l.function
	if fn == nil || fn.Name == nil || fn.Recv != nil || fn.Body == nil || l.typesInfo == nil {
		return false
	}

	var ref packageLockRef
	found := false
	for _, release := range matchingUnlockMethods(acquireMethods) {
		if ref, found = packageLockRefFor(l.typesInfo, fn, mutexName, release); found {
			break
		}
	}
	if !found || !ref.releasedOnlyAsItselfIn(fn) || packageLockAcquiredIn(fn.Body, ref, acquireMethods) {
		return false
	}

	self := l.typesInfo.Defs[fn.Name]
	if self == nil {
		return false
	}

	callSites := 0
	for _, caller := range l.functions {
		if caller == fn {
			continue
		}
		for _, call := range callsOfFunction(l.typesInfo, caller, self) {
			callSites++
			// A call inside a function literal runs when the literal does: the
			// lock it hands over must be taken in there.
			body := innermostLiteralBody(caller.Body, call.Pos())
			if _, ok := l.explicitTransferCallPositions(body)[call.Pos()]; !ok {
				return false
			}
			if !packageLockAcquiredBefore(body, ref, acquireMethods, call.Pos()) {
				return false
			}
		}
	}
	return callSites > 0
}

func packageLockAcquiredIn(body *ast.BlockStmt, ref packageLockRef, acquireMethods []string) bool {
	return packageLockAcquiredBefore(body, ref, acquireMethods, body.End())
}

func packageLockAcquiredBefore(body *ast.BlockStmt, ref packageLockRef, acquireMethods []string, before token.Pos) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Pos() >= before {
			return true
		}
		if ref.matches(call, acquireMethods) {
			found = true
			return false
		}
		return true
	})
	return found
}
