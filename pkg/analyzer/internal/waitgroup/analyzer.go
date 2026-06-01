package waitgroup

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/primitives"
	"golang.org/x/tools/go/analysis"
)

// Checker handles the analysis of WaitGroup usage
type Checker struct {
	waitGroupNames             map[string]bool
	localWaitGroupNames        map[string]bool
	packageLevelWaitGroupNames map[string]bool
	errorCollector             report.Reporter
	function                   *ast.FuncDecl
	commentFilter              *commentfilter.CommentFilter
	typesInfo                  *types.Info
	functionDecls              map[token.Pos]*ast.FuncDecl
	escape                     *escapeAnalyzer
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

// NewChecker creates a new WaitGroup checker. fr supplies the WaitGroup
// names visible inside the function being analyzed, with the locals and
// the package-level subset kept apart so Add/Wait pairing can be
// validated against the right scope.
func NewChecker(fr *primitives.FunctionResult, errorCollector report.Reporter, cf *commentfilter.CommentFilter, pass *analysis.Pass) *Checker {
	return &Checker{
		waitGroupNames:             fr.WaitGroups,
		localWaitGroupNames:        fr.LocalWaitGroups,
		packageLevelWaitGroupNames: fr.PackageWaitGroups,
		errorCollector:             errorCollector,
		commentFilter:              cf,
		// analysis.Pass normally provides TypesInfo; abort detection keeps
		// conservative fallbacks for direct tests and defensive callers.
		typesInfo:     pass.TypesInfo,
		functionDecls: buildFunctionDeclMap(pass.Files),
	}
}

// AnalyzeFunction analyzes WaitGroup usage in a function
func (wga *Checker) AnalyzeFunction(fn *ast.FuncDecl) {
	wga.function = fn
	wga.escape = newEscapeAnalyzer(
		fn,
		wga.relatedWaitGroupForCall,
		wga.functionCouldManageWaitGroup,
		wga.analyzeDoneCallsWithVisited,
		wga.isLocallyCreatedChannel,
		wga.resolveFunctionExpr,
	)
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
func (wga *Checker) collectStats() map[string]*Stats {
	stats := wga.initializeStats()
	wga.findDeferDoneCalls(stats)
	wga.collectCalls(stats)
	return stats
}

// initializeStats creates initial stats for all known WaitGroups
func (wga *Checker) initializeStats() map[string]*Stats {
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
func (wga *Checker) handleGoCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if wga.commentFilter.ShouldSkipCall(call) {
		return
	}

	stats[wgName].goCalls = append(stats[wgName].goCalls, call.Pos())
}

// handleAddCall processes Add() calls
func (wga *Checker) handleAddCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
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

func (wga *Checker) addValueAt(expr ast.Expr, pos token.Pos) (int, bool) {
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
func (wga *Checker) handleDoneCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if wga.commentFilter.ShouldSkipCall(call) {
		return
	}

	stats[wgName].doneCount++
	stats[wgName].doneCalls = append(stats[wgName].doneCalls, call.Pos())
}

// handleWaitCall processes Wait() calls
func (wga *Checker) handleWaitCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if wga.commentFilter.ShouldSkipCall(call) {
		return
	}

	stats[wgName].waitCalls = append(stats[wgName].waitCalls, call.Pos())
}

// isWaitGroupArgument checks if an argument represents a WaitGroup being passed.
func isWaitGroupArgument(arg ast.Expr, wgName string) bool {
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

func (wga *Checker) functionCouldManageWaitGroup(fn *ast.FuncDecl, wgName string, visited map[token.Pos]bool) bool {
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

func (wga *Checker) resolveCalledFunction(call *ast.CallExpr) *ast.FuncDecl {
	return wga.resolveFunctionExpr(call.Fun)
}

func (wga *Checker) resolveFunctionExpr(fun ast.Expr) *ast.FuncDecl {
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

func (wga *Checker) currentFunctionShadowsPackageLevelWaitGroup(wgName string) bool {
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

func (wga *Checker) relatedWaitGroupForCall(call *ast.CallExpr, wgName string) (*ast.FuncDecl, string, bool) {
	fn := wga.resolveCalledFunction(call)
	if fn == nil || fn.Body == nil {
		return nil, "", false
	}

	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		receiverExprName := common.GetVarName(sel.X)
		if receiverExprName != "" && receiverExprName != "?" {
			prefix := receiverExprName + "."
			if strings.HasPrefix(wgName, prefix) {
				if calleeReceiverName := common.ReceiverName(fn); calleeReceiverName != "" {
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
			if !isWaitGroupArgument(call.Args[argIndex], wgName) {
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

func (wga *Checker) calleeWaitGroupNameForArg(arg ast.Expr, wgName string, field *ast.Field, fieldIndex int) (string, bool) {
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
