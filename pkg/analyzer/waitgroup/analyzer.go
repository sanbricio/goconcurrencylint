package waitgroup

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
	commnetfilter "github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/report"
	"golang.org/x/tools/go/analysis"
)

// Analyzer handles the analysis of WaitGroup usage
type Analyzer struct {
	waitGroupNames map[string]bool
	errorCollector *report.ErrorCollector
	function       *ast.FuncDecl
	commentFilter  *commnetfilter.CommentFilter
	typesInfo      *types.Info
	functionDecls  map[token.Pos]*ast.FuncDecl
}

// addCall represents an Add() call with its position and value
type addCall struct {
	pos   token.Pos
	value int
}

// Stats tracks the state of a WaitGroup within a function
type Stats struct {
	addCalls       []addCall
	doneCalls      []token.Pos
	deferDoneCalls []token.Pos
	goCalls        []token.Pos
	waitCalls      []token.Pos
	doneCount      int
	totalAdd       int
}

// NewAnalyzer creates a new WaitGroup analyzer
func NewAnalyzer(waitGroupNames map[string]bool, errorCollector *report.ErrorCollector, cf *commnetfilter.CommentFilter, pass *analysis.Pass) *Analyzer {
	return &Analyzer{
		waitGroupNames: waitGroupNames,
		errorCollector: errorCollector,
		commentFilter:  cf,
		typesInfo:      pass.TypesInfo,
		functionDecls:  buildFunctionDeclMap(pass.Files),
	}
}

// AnalyzeFunction analyzes WaitGroup usage in a function
func (wga *Analyzer) AnalyzeFunction(fn *ast.FuncDecl) {
	wga.function = fn
	stats := wga.collectStats()
	wga.validateUsage(stats)
}

func buildFunctionDeclMap(files []*ast.File) map[token.Pos]*ast.FuncDecl {
	functionDecls := make(map[token.Pos]*ast.FuncDecl)
	for _, file := range files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				functionDecls[fn.Pos()] = fn
				functionDecls[fn.Name.Pos()] = fn
			}
		}
	}
	return functionDecls
}

// collectStats collects statistics for all WaitGroups in the function
func (wga *Analyzer) collectStats() map[string]*Stats {
	stats := wga.initializeStats()
	wga.findDeferDoneCalls(stats)
	wga.collectCalls(stats)
	return stats
}

// initializeStats creates initial stats for all known WaitGroups
func (wga *Analyzer) initializeStats() map[string]*Stats {
	stats := make(map[string]*Stats)
	for wgName := range wga.waitGroupNames {
		stats[wgName] = &Stats{
			addCalls:       []addCall{},
			doneCalls:      []token.Pos{},
			deferDoneCalls: []token.Pos{},
			goCalls:        []token.Pos{},
			waitCalls:      []token.Pos{},
		}
	}
	return stats
}

// handleGoCall processes WaitGroup.Go() calls.
func (wga *Analyzer) handleGoCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if wga.commentFilter.ShouldSkipCall(call) {
		return
	}

	stats[wgName].goCalls = append(stats[wgName].goCalls, call.Pos())
}

// handleAddCall processes Add() calls
func (wga *Analyzer) handleAddCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if wga.commentFilter.ShouldSkipCall(call) {
		return
	}

	addValue := common.GetAddValue(call)
	stats[wgName].addCalls = append(stats[wgName].addCalls, addCall{
		pos:   call.Pos(),
		value: addValue,
	})
	stats[wgName].totalAdd += addValue
}

// handleDoneCall processes Done() calls
func (wga *Analyzer) handleDoneCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if wga.commentFilter.ShouldSkipCall(call) {
		return
	}

	stats[wgName].doneCount++
	stats[wgName].doneCalls = append(stats[wgName].doneCalls, call.Pos())
}

// handleWaitCall processes Wait() calls
func (wga *Analyzer) handleWaitCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if wga.commentFilter.ShouldSkipCall(call) {
		return
	}

	stats[wgName].waitCalls = append(stats[wgName].waitCalls, call.Pos())
}

// isWaitGroupArgument checks if an argument represents a WaitGroup being passed
func (wga *Analyzer) isWaitGroupArgument(arg ast.Expr, wgName string) bool {
	if unary, ok := arg.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		if common.GetVarName(unary.X) == wgName {
			return true
		}
	}

	if common.GetVarName(arg) == wgName {
		return true
	}

	if sel, ok := arg.(*ast.SelectorExpr); ok {
		if common.GetVarName(sel.X) == wgName {
			methodName := sel.Sel.Name
			if methodName == "Done" || methodName == "Add" || methodName == "Wait" {
				return true
			}
		}
	}

	if call, ok := arg.(*ast.CallExpr); ok {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if common.GetVarName(sel.X) == wgName {
				return true
			}
		}
	}

	return false
}

// isWaitGroupPassedToOtherFunctions checks if a WaitGroup is passed to other functions
func (wga *Analyzer) isWaitGroupPassedToOtherFunctions(wgName string) bool {
	localCallbacks := wga.collectLocalCallbackExprs()
	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.CallExpr:
			if fn, calleeWGName, related := wga.relatedWaitGroupForCall(node, wgName); related &&
				fn != nil && wga.functionCouldManageWaitGroup(fn, calleeWGName, make(map[token.Pos]bool)) {
				found = true
				return false
			}
			for _, arg := range node.Args {
				if wga.exprEscapesWaitGroup(arg, wgName, localCallbacks, make(map[string]bool)) {
					found = true
					return false
				}
			}
		case *ast.AssignStmt:
			for _, rhs := range node.Rhs {
				if wga.exprEscapesWaitGroup(rhs, wgName, localCallbacks, make(map[string]bool)) {
					found = true
					return false
				}
			}
		case *ast.ValueSpec:
			for _, value := range node.Values {
				if wga.exprEscapesWaitGroup(value, wgName, localCallbacks, make(map[string]bool)) {
					found = true
					return false
				}
			}
		case *ast.ReturnStmt:
			for _, result := range node.Results {
				if wga.exprEscapesWaitGroup(result, wgName, localCallbacks, make(map[string]bool)) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func (wga *Analyzer) collectLocalCallbackExprs() map[string]ast.Expr {
	callbacks := make(map[string]ast.Expr)

	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
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

func (wga *Analyzer) exprEscapesWaitGroup(expr ast.Expr, wgName string, callbacks map[string]ast.Expr, seen map[string]bool) bool {
	if expr == nil {
		return false
	}

	if wga.isWaitGroupArgument(expr, wgName) {
		return true
	}

	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if fn, calleeWGName, related := wga.relatedWaitGroupForCall(call, wgName); related &&
				fn != nil && wga.functionCouldManageWaitGroup(fn, calleeWGName, make(map[token.Pos]bool)) {
				found = true
				return false
			}
		}

		switch node := n.(type) {
		case *ast.UnaryExpr:
			if node.Op == token.AND && common.GetVarName(node.X) == wgName {
				found = true
				return false
			}
		case *ast.FuncLit:
			if wga.containsDoneCall(node.Body, wgName) {
				found = true
				return false
			}
		case *ast.Ident:
			if seen[node.Name] {
				return true
			}
			if callbackExpr, ok := callbacks[node.Name]; ok {
				seen[node.Name] = true
				if wga.exprEscapesWaitGroup(callbackExpr, wgName, callbacks, seen) {
					found = true
					return false
				}
			}
		case *ast.SelectorExpr:
			if wga.methodValueContainsDoneForWaitGroup(node, wgName) {
				found = true
				return false
			}
		}
		return !found
	})
	return found
}

func (wga *Analyzer) functionCouldManageWaitGroup(fn *ast.FuncDecl, wgName string, visited map[token.Pos]bool) bool {
	if fn == nil || fn.Body == nil || wgName == "" {
		return false
	}
	if visited[fn.Pos()] {
		return false
	}

	visited[fn.Pos()] = true
	defer delete(visited, fn.Pos())

	return wga.analyzeDoneCallsWithVisited(fn.Body, wgName, visited).hasAnyDone
}

func (wga *Analyzer) resolveCalledFunction(call *ast.CallExpr) *ast.FuncDecl {
	return wga.resolveFunctionExpr(call.Fun)
}

func (wga *Analyzer) resolveFunctionExpr(fun ast.Expr) *ast.FuncDecl {
	switch fun := fun.(type) {
	case *ast.Ident:
		if obj, ok := wga.typesInfo.Uses[fun].(*types.Func); ok {
			return wga.functionDecls[obj.Pos()]
		}
	case *ast.SelectorExpr:
		if sel := wga.typesInfo.Selections[fun]; sel != nil {
			if obj, ok := sel.Obj().(*types.Func); ok {
				return wga.functionDecls[obj.Pos()]
			}
		}
		if obj, ok := wga.typesInfo.Uses[fun.Sel].(*types.Func); ok {
			return wga.functionDecls[obj.Pos()]
		}
	}
	return nil
}

func (wga *Analyzer) methodValueContainsDoneForWaitGroup(sel *ast.SelectorExpr, wgName string) bool {
	fn := wga.resolveFunctionExpr(sel)
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

	calleeReceiverName := receiverName(fn)
	if calleeReceiverName == "" {
		return false
	}

	suffix := strings.TrimPrefix(wgName, receiverExprName)
	calleeWGName := calleeReceiverName + suffix
	return wga.functionCouldManageWaitGroup(fn, calleeWGName, make(map[token.Pos]bool))
}

func receiverName(fn *ast.FuncDecl) string {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	if len(fn.Recv.List[0].Names) == 0 {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}

func (wga *Analyzer) relatedWaitGroupForCall(call *ast.CallExpr, wgName string) (*ast.FuncDecl, string, bool) {
	fn := wga.resolveCalledFunction(call)
	if fn == nil || fn.Body == nil {
		return nil, "", false
	}

	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		receiverExprName := common.GetVarName(sel.X)
		if receiverExprName != "" && receiverExprName != "?" {
			prefix := receiverExprName + "."
			if strings.HasPrefix(wgName, prefix) {
				if calleeReceiverName := receiverName(fn); calleeReceiverName != "" {
					suffix := strings.TrimPrefix(wgName, receiverExprName)
					return fn, calleeReceiverName + suffix, true
				}
			}
		}
	}

	if fn.Type == nil || fn.Type.Params == nil {
		return nil, "", false
	}

	argIndex := 0
	for _, field := range fn.Type.Params.List {
		fieldArity := len(field.Names)
		if fieldArity == 0 {
			fieldArity = 1
		}

		for i := 0; i < fieldArity && argIndex < len(call.Args); i++ {
			if !wga.isWaitGroupArgument(call.Args[argIndex], wgName) {
				argIndex++
				continue
			}
			if i < len(field.Names) {
				return fn, field.Names[i].Name, true
			}
			return nil, "", false
		}
	}

	return nil, "", false
}
