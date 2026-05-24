package waitgroup

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/report"
	"golang.org/x/tools/go/analysis"
)

// Analyzer handles the analysis of WaitGroup usage
type Analyzer struct {
	waitGroupNames             map[string]bool
	localWaitGroupNames        map[string]bool
	packageLevelWaitGroupNames map[string]bool
	errorCollector             report.Reporter
	function                   *ast.FuncDecl
	commentFilter              *commentfilter.CommentFilter
	typesInfo                  *types.Info
	functionDecls              map[token.Pos]*ast.FuncDecl
}

// addCall represents an Add() call with its position and value
type addCall struct {
	pos   token.Pos
	value int
	known bool
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
func NewAnalyzer(waitGroupNames, localWaitGroupNames, packageLevelWaitGroupNames map[string]bool, errorCollector report.Reporter, cf *commentfilter.CommentFilter, pass *analysis.Pass) *Analyzer {
	return &Analyzer{
		waitGroupNames:             waitGroupNames,
		localWaitGroupNames:        localWaitGroupNames,
		packageLevelWaitGroupNames: packageLevelWaitGroupNames,
		errorCollector:             errorCollector,
		commentFilter:              cf,
		// analysis.Pass normally provides TypesInfo; abort detection keeps
		// conservative fallbacks for direct tests and defensive callers.
		typesInfo:     pass.TypesInfo,
		functionDecls: buildFunctionDeclMap(pass.Files),
	}
}

// AnalyzeFunction analyzes WaitGroup usage in a function
func (wga *Analyzer) AnalyzeFunction(fn *ast.FuncDecl) {
	wga.function = fn
	stats := wga.collectStats()
	wga.validateUsage(stats)
}

// Includes generated files so relatedWaitGroupForCall can resolve helpers
// that complete the Add/Done pair across the boundary.
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
	addKnown := false
	if len(call.Args) > 0 {
		if constantValue, ok := wga.addValueAt(call.Args[0], call.Pos()); ok {
			// Keep exact typed constants so balance and literal loop checks see
			// wg.Add(workers) the same way they see wg.Add(4).
			addValue = constantValue
			addKnown = true
			if addValue < 0 {
				wga.errorCollector.AddError(call.Pos(), category.AddNegative, "waitgroup '"+wgName+"' has negative Add("+strconv.Itoa(addValue)+")")
			}
			// Require a compile-time constant: the len(ident) heuristic above
			// can underestimate when the collection is mutated through a closure.
			if addValue == 0 && common.IsConstantIntExpr(call.Args[0], wga.typesInfo) {
				wga.errorCollector.AddError(call.Pos(), category.AddZero, "waitgroup '"+wgName+"' Add(0) is a no-op")
			}
		}
	}
	stats[wgName].addCalls = append(stats[wgName].addCalls, addCall{
		pos:   call.Pos(),
		value: addValue,
		known: addKnown,
	})
	stats[wgName].totalAdd += addValue
}

func (wga *Analyzer) addValueAt(expr ast.Expr, pos token.Pos) (int, bool) {
	if value, ok := common.ConstantIntValue(expr, wga.typesInfo); ok {
		return value, true
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return 0, false
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "len" {
		return 0, false
	}
	argIdent, ok := call.Args[0].(*ast.Ident)
	if !ok {
		return 0, false
	}
	return wga.collectionLengthBefore(argIdent.Name, pos)
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
		case *ast.SendStmt:
			if wga.sendEscapesWaitGroup(node, wgName, localCallbacks) {
				found = true
				return false
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

func (wga *Analyzer) sendEscapesWaitGroup(send *ast.SendStmt, wgName string, callbacks map[string]ast.Expr) bool {
	if send == nil || !wga.exprEscapesWaitGroup(send.Value, wgName, callbacks, make(map[string]bool)) {
		return false
	}
	chanName := common.GetVarName(send.Chan)
	if chanName == "" || chanName == "?" {
		return true
	}
	return !wga.isLocallyCreatedChannel(chanName)
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

	if fnLit, ok := expr.(*ast.FuncLit); ok {
		return wga.functionLiteralEscapesWaitGroup(fnLit, wgName, callbacks, seen)
	}

	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if exprNode, ok := n.(ast.Expr); ok && wga.isWaitGroupArgument(exprNode, wgName) {
			found = true
			return false
		}

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
			if wga.functionLiteralEscapesWaitGroup(node, wgName, callbacks, seen) {
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

func (wga *Analyzer) functionLiteralEscapesWaitGroup(fn *ast.FuncLit, wgName string, callbacks map[string]ast.Expr, seen map[string]bool) bool {
	if fn == nil || fn.Body == nil {
		return false
	}

	if wga.analyzeDoneCallsWithVisited(fn.Body, wgName, make(map[token.Pos]bool)).hasAnyDone {
		return true
	}

	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}

		if exprNode, ok := n.(ast.Expr); ok && wga.isWaitGroupArgument(exprNode, wgName) {
			found = true
			return false
		}

		if ident, ok := n.(*ast.Ident); ok {
			if callbackExpr, ok := callbacks[ident.Name]; ok && !seen[ident.Name] {
				seen[ident.Name] = true
				found = wga.exprEscapesWaitGroup(callbackExpr, wgName, callbacks, seen)
				delete(seen, ident.Name)
				return !found
			}
		}

		return true
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

func (wga *Analyzer) currentFunctionShadowsPackageLevelWaitGroup(wgName string) bool {
	if wga.localWaitGroupNames[wgName] {
		return true
	}

	if wga.function == nil || wga.function.Type == nil || wga.function.Type.Params == nil {
		return false
	}

	for _, field := range wga.function.Type.Params.List {
		typ := wga.typesInfo.TypeOf(field.Type)
		if !common.IsWaitGroup(typ) {
			continue
		}
		for _, name := range field.Names {
			if name.Name == wgName {
				return true
			}
		}
	}

	return false
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
		if wga.packageLevelWaitGroupNames[wgName] &&
			!wga.currentFunctionShadowsPackageLevelWaitGroup(wgName) &&
			wga.functionCouldManageWaitGroup(fn, wgName, make(map[token.Pos]bool)) {
			return fn, wgName, true
		}
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
				if calleeWGName, ok := wga.calleeWaitGroupNameForArg(call.Args[argIndex], wgName, field, i); ok {
					return fn, calleeWGName, true
				}
				argIndex++
				continue
			}
			if i < len(field.Names) {
				return fn, field.Names[i].Name, true
			}
			return nil, "", false
		}
	}

	if wga.packageLevelWaitGroupNames[wgName] &&
		!wga.currentFunctionShadowsPackageLevelWaitGroup(wgName) &&
		wga.functionCouldManageWaitGroup(fn, wgName, make(map[token.Pos]bool)) {
		return fn, wgName, true
	}

	return nil, "", false
}

func (wga *Analyzer) calleeWaitGroupNameForArg(arg ast.Expr, wgName string, field *ast.Field, fieldIndex int) (string, bool) {
	argName := common.GetVarName(arg)
	if argName == "" || argName == "?" {
		return "", false
	}

	prefix := argName + "."
	if !strings.HasPrefix(wgName, prefix) || fieldIndex >= len(field.Names) {
		return "", false
	}

	return field.Names[fieldIndex].Name + strings.TrimPrefix(wgName, argName), true
}
