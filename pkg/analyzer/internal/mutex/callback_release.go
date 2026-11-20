package mutex

import (
	"go/ast"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// lockReleasedByEscapingCallback reports whether the function hands the
// release of mutexName to a callback it passes on, the asynchronous
// completion shape:
//
//	mu.Lock()
//	done := func(err error) { mu.Unlock(); onDone(err) }
//	send(req, done)
//
// The lock is still held when the function returns, on purpose: whoever runs
// the callback releases it. The callback must be a function literal that
// releases the lock without taking it, so it gives back what the function
// holds rather than running a critical section of its own, and it must leave
// the function: passed as a call argument, directly or through the local
// variable it is stored in, or called from another literal that leaves.
// Goroutine and deferred literals are the call itself, not an argument, and
// run their release in view of the analysis, so they do not count.
func (c *Checker) lockReleasedByEscapingCallback(mutexName string, pattern LockPattern) bool {
	if c.function == nil || c.function.Body == nil || c.typesInfo == nil {
		return false
	}

	stored := make(map[types.Object]*ast.FuncLit)
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != len(assign.Rhs) {
			return true
		}
		for index, rhs := range assign.Rhs {
			lit, ok := common.UnwrapParenExpr(rhs).(*ast.FuncLit)
			if !ok {
				continue
			}
			if obj := c.identObject(assign.Lhs[index]); obj != nil {
				stored[obj] = lit
			}
		}
		return true
	})

	var pending []*ast.FuncLit
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, arg := range call.Args {
			if lit := c.callbackLiteral(arg, stored); lit != nil {
				pending = append(pending, lit)
			}
		}
		return true
	})

	seen := make(map[*ast.FuncLit]bool)
	for len(pending) > 0 {
		lit := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[lit] {
			continue
		}
		seen[lit] = true
		if functionBodyContainsFieldCall(lit.Body, mutexName, pattern.UnlockMethods) &&
			!functionBodyContainsFieldCall(lit.Body, mutexName, anyAcquireMethods) {
			return true
		}
		// A literal that leaves carries every stored literal it calls.
		ast.Inspect(lit.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if inner := c.callbackLiteral(call.Fun, stored); inner != nil {
					pending = append(pending, inner)
				}
			}
			return true
		})
	}
	return false
}

// callbackLiteral returns the function literal expr is, or the one stored in
// the local variable expr names.
func (c *Checker) callbackLiteral(expr ast.Expr, stored map[types.Object]*ast.FuncLit) *ast.FuncLit {
	switch node := common.UnwrapParenExpr(expr).(type) {
	case *ast.FuncLit:
		return node
	case *ast.Ident:
		return stored[c.typesInfo.Uses[node]]
	}
	return nil
}

// identObject returns the variable an assignment target names.
func (c *Checker) identObject(expr ast.Expr) types.Object {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return nil
	}
	if obj := c.typesInfo.Defs[ident]; obj != nil {
		return obj
	}
	return c.typesInfo.Uses[ident]
}
