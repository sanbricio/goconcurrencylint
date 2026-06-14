package mutex

import (
	"go/ast"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// lockAcquiredInCallbackArgument reports whether the mutex is locked inside a
// function literal passed as an argument to a call in the current function —
// the synchronous-callback pattern. A concurrent map's LoadOrStoreNew, for
// instance, runs exactly one of its load/store callbacks and leaves the stream
// locked for the caller to release:
//
//	s, _, _ := streams.LoadOrStoreNew(key,
//	    func() (*stream, error) { s.chunkMtx.Lock(); ... },
//	    func(s *stream) error   { s.chunkMtx.Lock(); ... },
//	)
//	s.chunkMtx.Unlock() // released here
//
// The lifecycle analysis can't see locks taken inside such callbacks, so an
// unlock in the caller is not provably unmatched — the borrowed-unlock
// diagnostic is suppressed.
//
// Goroutine and deferred closures are deliberately excluded: their FuncLit is
// the call's Fun, not an argument, so a `go func(){ mu.Lock() }()` does not
// synchronously precede the caller's unlock and stays flagged.
func (c *Checker) lockAcquiredInCallbackArgument(mutexName string, lockMethods []string) bool {
	if c.function == nil || c.function.Body == nil {
		return false
	}
	found := false
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, arg := range call.Args {
			fnlit, ok := arg.(*ast.FuncLit)
			if !ok {
				continue
			}
			if closureLocksMutex(fnlit.Body, mutexName, lockMethods) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// closureLocksMutex reports whether body contains a `mutexName.<lockMethod>()`
// call for one of the supplied lock methods.
func closureLocksMutex(body *ast.BlockStmt, mutexName string, lockMethods []string) bool {
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
		if !ok {
			return true
		}
		if common.GetVarName(sel.X) == mutexName && slices.Contains(lockMethods, sel.Sel.Name) {
			found = true
			return false
		}
		return true
	})
	return found
}
