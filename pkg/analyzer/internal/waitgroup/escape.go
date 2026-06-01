package waitgroup

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

type relatedWaitGroupResolver func(*ast.CallExpr, string) (*ast.FuncDecl, string, bool)
type waitGroupManager func(*ast.FuncDecl, string, map[token.Pos]bool) bool
type doneCallAnalyzer func(*ast.BlockStmt, string, map[token.Pos]bool) doneCallInfo
type localChannelChecker func(string) bool
type functionResolver func(ast.Expr) *ast.FuncDecl

// escapeAnalyzer decides whether the current WaitGroup ownership is handed to
// another function, callback, channel, or returned value. It is per-function:
// callback locals and local channel checks are scoped to the function currently
// being analyzed.
type escapeAnalyzer struct {
	function                     *ast.FuncDecl
	relatedWaitGroupForCall      relatedWaitGroupResolver
	functionCouldManageWaitGroup waitGroupManager
	analyzeDoneCalls             doneCallAnalyzer
	isLocallyCreatedChannel      localChannelChecker
	resolveFunction              functionResolver
}

func newEscapeAnalyzer(
	function *ast.FuncDecl,
	relatedWaitGroupForCall relatedWaitGroupResolver,
	functionCouldManageWaitGroup waitGroupManager,
	analyzeDoneCalls doneCallAnalyzer,
	isLocallyCreatedChannel localChannelChecker,
	resolveFunction functionResolver,
) *escapeAnalyzer {
	return &escapeAnalyzer{
		function:                     function,
		relatedWaitGroupForCall:      relatedWaitGroupForCall,
		functionCouldManageWaitGroup: functionCouldManageWaitGroup,
		analyzeDoneCalls:             analyzeDoneCalls,
		isLocallyCreatedChannel:      isLocallyCreatedChannel,
		resolveFunction:              resolveFunction,
	}
}

// isWaitGroupPassedToOtherFunctions checks if a WaitGroup is passed to other
// functions or otherwise escapes this function's local lifecycle.
func (e *escapeAnalyzer) isWaitGroupPassedToOtherFunctions(wgName string) bool {
	if e == nil || e.function == nil || e.function.Body == nil {
		return false
	}

	localCallbacks := e.collectLocalCallbackExprs()
	found := false
	ast.Inspect(e.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.CallExpr:
			if e.relatedCallCanManageWaitGroup(node, wgName) {
				found = true
				return false
			}
			for _, arg := range node.Args {
				if e.exprEscapesWaitGroup(arg, wgName, localCallbacks, make(map[string]bool)) {
					found = true
					return false
				}
			}
		case *ast.AssignStmt:
			for _, rhs := range node.Rhs {
				if e.exprEscapesWaitGroup(rhs, wgName, localCallbacks, make(map[string]bool)) {
					found = true
					return false
				}
			}
		case *ast.ValueSpec:
			for _, value := range node.Values {
				if e.exprEscapesWaitGroup(value, wgName, localCallbacks, make(map[string]bool)) {
					found = true
					return false
				}
			}
		case *ast.SendStmt:
			if e.sendEscapesWaitGroup(node, wgName, localCallbacks) {
				found = true
				return false
			}
		case *ast.ReturnStmt:
			for _, result := range node.Results {
				if e.exprEscapesWaitGroup(result, wgName, localCallbacks, make(map[string]bool)) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func (e *escapeAnalyzer) sendEscapesWaitGroup(send *ast.SendStmt, wgName string, callbacks map[string]ast.Expr) bool {
	if send == nil || !e.exprEscapesWaitGroup(send.Value, wgName, callbacks, make(map[string]bool)) {
		return false
	}
	chanName := common.GetVarName(send.Chan)
	if chanName == "" || chanName == "?" {
		return true
	}
	return e.isLocallyCreatedChannel == nil || !e.isLocallyCreatedChannel(chanName)
}

func (e *escapeAnalyzer) collectLocalCallbackExprs() map[string]ast.Expr {
	callbacks := make(map[string]ast.Expr)
	if e.function == nil || e.function.Body == nil {
		return callbacks
	}

	ast.Inspect(e.function.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || i >= len(node.Rhs) {
					continue
				}
				callbacks[ident.Name] = node.Rhs[i]
			}
		case *ast.ValueSpec:
			for i, name := range node.Names {
				if i >= len(node.Values) {
					continue
				}
				callbacks[name.Name] = node.Values[i]
			}
		}
		return true
	})

	return callbacks
}

func (e *escapeAnalyzer) exprEscapesWaitGroup(expr ast.Expr, wgName string, callbacks map[string]ast.Expr, seen map[string]bool) bool {
	if expr == nil {
		return false
	}

	if isWaitGroupArgument(expr, wgName) {
		return true
	}

	if fnLit, ok := expr.(*ast.FuncLit); ok {
		return e.functionLiteralEscapesWaitGroup(fnLit, wgName, callbacks, seen)
	}

	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if exprNode, ok := n.(ast.Expr); ok && isWaitGroupArgument(exprNode, wgName) {
			found = true
			return false
		}

		if call, ok := n.(*ast.CallExpr); ok && e.relatedCallCanManageWaitGroup(call, wgName) {
			found = true
			return false
		}

		switch node := n.(type) {
		case *ast.UnaryExpr:
			if node.Op == token.AND && common.GetVarName(node.X) == wgName {
				found = true
				return false
			}
		case *ast.FuncLit:
			if e.functionLiteralEscapesWaitGroup(node, wgName, callbacks, seen) {
				found = true
				return false
			}
		case *ast.Ident:
			if seen[node.Name] {
				return true
			}
			if callbackExpr, ok := callbacks[node.Name]; ok {
				seen[node.Name] = true
				if e.exprEscapesWaitGroup(callbackExpr, wgName, callbacks, seen) {
					found = true
					return false
				}
			}
		case *ast.SelectorExpr:
			if e.methodValueContainsDoneForWaitGroup(node, wgName) {
				found = true
				return false
			}
		}
		return !found
	})
	return found
}

func (e *escapeAnalyzer) functionLiteralEscapesWaitGroup(fn *ast.FuncLit, wgName string, callbacks map[string]ast.Expr, seen map[string]bool) bool {
	if fn == nil || fn.Body == nil {
		return false
	}

	if e.analyzeDoneCalls != nil && e.analyzeDoneCalls(fn.Body, wgName, make(map[token.Pos]bool)).hasAnyDone {
		return true
	}

	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}

		if exprNode, ok := n.(ast.Expr); ok && isWaitGroupArgument(exprNode, wgName) {
			found = true
			return false
		}

		if ident, ok := n.(*ast.Ident); ok {
			if callbackExpr, ok := callbacks[ident.Name]; ok && !seen[ident.Name] {
				seen[ident.Name] = true
				found = e.exprEscapesWaitGroup(callbackExpr, wgName, callbacks, seen)
				delete(seen, ident.Name)
				return !found
			}
		}

		return true
	})

	return found
}

func (e *escapeAnalyzer) relatedCallCanManageWaitGroup(call *ast.CallExpr, wgName string) bool {
	if e.relatedWaitGroupForCall == nil || e.functionCouldManageWaitGroup == nil {
		return false
	}

	fn, calleeWGName, related := e.relatedWaitGroupForCall(call, wgName)
	return related && fn != nil && e.functionCouldManageWaitGroup(fn, calleeWGName, make(map[token.Pos]bool))
}

func (e *escapeAnalyzer) methodValueContainsDoneForWaitGroup(sel *ast.SelectorExpr, wgName string) bool {
	if e.resolveFunction == nil || e.functionCouldManageWaitGroup == nil {
		return false
	}

	fn := e.resolveFunction(sel)
	if fn == nil || fn.Body == nil {
		return false
	}

	receiverExprName := common.GetVarName(sel.X)
	if receiverExprName == "" || receiverExprName == "?" {
		return false
	}

	prefix := receiverExprName + "."
	if !strings.HasPrefix(wgName, prefix) {
		return false
	}

	calleeReceiverName := common.ReceiverName(fn)
	if calleeReceiverName == "" {
		return false
	}

	suffix := strings.TrimPrefix(wgName, receiverExprName)
	calleeWGName := calleeReceiverName + suffix
	return e.functionCouldManageWaitGroup(fn, calleeWGName, make(map[token.Pos]bool))
}
