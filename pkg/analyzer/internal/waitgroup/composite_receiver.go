package waitgroup

import (
	"go/ast"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// calleeWaitGroupNameForCompositeReceiver resolves the name a method sees when
// its receiver is built at the call site — `go (&handler{wg: &wg}).run()` — where
// the WaitGroup travels as a field and the callee calls it `h.wg`.
func calleeWaitGroupNameForCompositeReceiver(receiver ast.Expr, fn *ast.FuncDecl, wgName string) (string, bool) {
	lit := compositeLiteralReceiver(receiver)
	if lit == nil || fn == nil {
		return "", false
	}

	calleeReceiverName := common.ReceiverName(fn)
	if calleeReceiverName == "" {
		return "", false
	}

	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || !isWaitGroupArgument(kv.Value, wgName) {
			continue
		}
		return calleeReceiverName + "." + key.Name, true
	}
	return "", false
}

// compositeLiteralReceiver unwraps a receiver down to the literal it builds,
// addressed or not.
func compositeLiteralReceiver(expr ast.Expr) *ast.CompositeLit {
	expr = common.UnwrapParenExpr(expr)
	if unary, ok := expr.(*ast.UnaryExpr); ok {
		expr = common.UnwrapParenExpr(unary.X)
	}
	lit, _ := expr.(*ast.CompositeLit)
	return lit
}
