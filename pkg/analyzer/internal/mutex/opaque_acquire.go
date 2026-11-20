package mutex

import (
	"go/ast"
	"go/types"
	"slices"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A package can take a lock only through its bound methods, handed to code
// that calls them later (`try = j.mu.TryLock; ...; try()`), and give it back
// with plain calls from other methods:
//
//	func (j *flock) acquire(write bool) { try := j.mu.TryRLock; if write { try = j.mu.TryLock }; for !try() {} }
//	func (j *flock) Unlock()  { ...; j.mu.Unlock() }
//
// No acquisition of the lock is in view anywhere, so a release of it has
// nothing to be paired with by name. It is not reported when the package
// acquires that lock, on values of the releasing method's receiver type,
// only through bound acquiring methods.

// lockAcquiredOnlyOpaquely reports whether mutexName, a lock below the
// receiver of the method under analysis, is acquired in the package only
// through bound methods of it.
func (l *lifecycleResolver) lockAcquiredOnlyOpaquely(mutexName string, acquireMethods []string) bool {
	fn := l.function
	if fn == nil || l.typesInfo == nil {
		return false
	}
	path, ok := receiverRelativePath(mutexName, common.ReceiverName(fn))
	if !ok || path == "" || common.ReceiverName(fn) == "" {
		return false
	}
	ownerType := common.ReceiverTypeName(fn)

	owns := func(sel *ast.SelectorExpr) bool {
		owner, _, ok := splitOwnerPath(sel.X, path)
		if !ok || common.BaseTypeNameFromType(l.typesInfo.TypeOf(owner)) != ownerType {
			return false
		}
		selection := l.typesInfo.Selections[sel]
		return selection != nil && selection.Kind() == types.MethodVal
	}
	bound, direct := 0, 0
	for _, method := range acquireMethods {
		for _, b := range l.boundSelectorsNamed(method) {
			if owns(b.sel) {
				bound++
			}
		}
	}
	if bound == 0 {
		return false
	}
	for _, method := range acquireMethods {
		for _, site := range l.callSitesNamed(method) {
			if sel, ok := common.UnwrapParenExpr(site.call.Fun).(*ast.SelectorExpr); ok && owns(sel) {
				direct++
			}
		}
	}
	return bound > 0 && direct == 0
}

// splitOwnerPath splits a lock expression `x.a.b` into its owner x and checks
// that the rest of it is path ("a.b").
func splitOwnerPath(expr ast.Expr, path string) (ast.Expr, string, bool) {
	parts := strings.Split(path, ".")
	current := common.UnwrapParenExpr(expr)
	for _, part := range slices.Backward(parts) {
		sel, ok := current.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != part {
			return nil, "", false
		}
		current = common.UnwrapParenExpr(sel.X)
	}
	return current, path, true
}
