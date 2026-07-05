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
	iteration                  *iterationEstimator
	worker                     *workerDoneAnalyzer
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
func (c *Checker) AnalyzeFunction(fn *ast.FuncDecl) {
	c.function = fn
	c.worker = newWorkerDoneAnalyzer(fn, c.waitGroupNames, c.commentFilter, c.typesInfo, c.errorCollector)
	c.iteration = newIterationEstimator(fn, c.typesInfo, c.commentFilter)
	c.escape = newEscapeAnalyzer(
		fn,
		c.relatedWaitGroupForCall,
		c.functionCouldManageWaitGroup,
		c.analyzeDoneCallsWithVisited,
		c.worker.isLocallyCreatedChannel,
		func(fun ast.Expr) *ast.FuncDecl { return resolveFunctionExpr(fun, c.typesInfo, c.functionDecls) },
	)
	stats := c.collectStats()
	c.validateUsage(stats)
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
func (c *Checker) collectStats() map[string]*Stats {
	stats := c.initializeStats()
	c.findDeferDoneCalls(stats)
	c.collectCalls(stats)
	return stats
}

// initializeStats creates initial stats for all known WaitGroups
func (c *Checker) initializeStats() map[string]*Stats {
	stats := make(map[string]*Stats)
	for wgName := range c.waitGroupNames {
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
func (c *Checker) handleGoCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if c.commentFilter.ShouldSkipCall(call) {
		return
	}

	stats[wgName].goCalls = append(stats[wgName].goCalls, call.Pos())
}

// handleAddCall processes Add() calls
func (c *Checker) handleAddCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if c.commentFilter.ShouldSkipCall(call) {
		return
	}

	addValue := common.GetAddValue(call)
	addKnown := false
	if len(call.Args) > 0 {
		if constantValue, ok := c.addValueAt(call.Args[0], call.Pos()); ok {
			// Keep exact typed constants so balance and literal loop checks see
			// wg.Add(workers) the same way they see wg.Add(4).
			addValue = constantValue
			addKnown = true
			if addValue < 0 {
				c.errorCollector.AddError(call.Pos(), category.AddNegative, "waitgroup '"+wgName+"' has negative Add("+strconv.Itoa(addValue)+")")
			}
			// Require a compile-time constant: the len(ident) heuristic above
			// can underestimate when the collection is mutated through a closure.
			if addValue == 0 && common.IsConstantIntExpr(call.Args[0], c.typesInfo) {
				c.errorCollector.AddError(call.Pos(), category.AddZero, "waitgroup '"+wgName+"' Add(0) is a no-op")
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

func (c *Checker) addValueAt(expr ast.Expr, pos token.Pos) (int, bool) {
	if value, ok := common.ConstantIntValue(expr, c.typesInfo); ok {
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
	if c.iteration == nil {
		return 0, false
	}
	return c.iteration.collectionLengthBefore(argIdent.Name, pos)
}

// handleDoneCall processes Done() calls
func (c *Checker) handleDoneCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if c.commentFilter.ShouldSkipCall(call) {
		return
	}

	stats[wgName].doneCount++
	stats[wgName].doneCalls = append(stats[wgName].doneCalls, call.Pos())
}

// handleWaitCall processes Wait() calls
func (c *Checker) handleWaitCall(call *ast.CallExpr, wgName string, stats map[string]*Stats) {
	if c.commentFilter.ShouldSkipCall(call) {
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

func (c *Checker) functionCouldManageWaitGroup(fn *ast.FuncDecl, wgName string, visited map[token.Pos]bool) bool {
	if fn == nil || fn.Body == nil || wgName == "" {
		return false
	}
	if visited[fn.Pos()] {
		return false
	}

	visited[fn.Pos()] = true
	defer delete(visited, fn.Pos())

	return c.analyzeDoneCallsWithVisited(fn.Body, wgName, visited).hasAnyDone
}

func resolveCalledFunction(call *ast.CallExpr, typesInfo *types.Info, functionDecls map[token.Pos]*ast.FuncDecl) *ast.FuncDecl {
	return resolveFunctionExpr(call.Fun, typesInfo, functionDecls)
}

func resolveFunctionExpr(fun ast.Expr, typesInfo *types.Info, functionDecls map[token.Pos]*ast.FuncDecl) *ast.FuncDecl {
	switch fun := fun.(type) {
	case *ast.Ident:
		if obj, ok := typesInfo.Uses[fun].(*types.Func); ok {
			return functionDecls[obj.Pos()]
		}
	case *ast.SelectorExpr:
		if sel := typesInfo.Selections[fun]; sel != nil {
			if obj, ok := sel.Obj().(*types.Func); ok {
				return functionDecls[obj.Pos()]
			}
		}
		if obj, ok := typesInfo.Uses[fun.Sel].(*types.Func); ok {
			return functionDecls[obj.Pos()]
		}
	}
	return nil
}

func (c *Checker) currentFunctionShadowsPackageLevelWaitGroup(wgName string) bool {
	if c.localWaitGroupNames[wgName] {
		return true
	}

	if c.function == nil || c.function.Type == nil || c.function.Type.Params == nil {
		return false
	}

	for _, field := range c.function.Type.Params.List {
		typ := c.typesInfo.TypeOf(field.Type)
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

func (c *Checker) relatedWaitGroupForCall(call *ast.CallExpr, wgName string) (*ast.FuncDecl, string, bool) {
	fn := resolveCalledFunction(call, c.typesInfo, c.functionDecls)
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
					calleeWGName := calleeReceiverName + suffix
					if functionBodyUsesWaitGroupName(fn.Body, calleeWGName) {
						return fn, calleeWGName, true
					}
				}
			}
		}
		if rootName := rootVarName(sel.X); rootName != "" {
			prefix := rootName + "."
			if strings.HasPrefix(wgName, prefix) {
				if calleeReceiverName := common.ReceiverName(fn); calleeReceiverName != "" {
					suffix := strings.TrimPrefix(wgName, rootName)
					if calleeWGName := calleeWaitGroupNameForReceiverAlias(fn, calleeReceiverName, suffix); calleeWGName != "" {
						return fn, calleeWGName, true
					}
				}
			}
		}
	}

	if fn.Type == nil || fn.Type.Params == nil {
		if c.packageLevelWaitGroupNames[wgName] &&
			!c.currentFunctionShadowsPackageLevelWaitGroup(wgName) &&
			c.functionCouldManageWaitGroup(fn, wgName, make(map[token.Pos]bool)) {
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
				if calleeWGName, ok := calleeWaitGroupNameForArg(call.Args[argIndex], wgName, field, i); ok {
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

	if c.packageLevelWaitGroupNames[wgName] &&
		!c.currentFunctionShadowsPackageLevelWaitGroup(wgName) &&
		c.functionCouldManageWaitGroup(fn, wgName, make(map[token.Pos]bool)) {
		return fn, wgName, true
	}

	return nil, "", false
}

func rootVarName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return rootVarName(e.X)
	case *ast.IndexExpr:
		return rootVarName(e.X)
	case *ast.IndexListExpr:
		return rootVarName(e.X)
	case *ast.ParenExpr:
		return rootVarName(e.X)
	case *ast.StarExpr:
		return rootVarName(e.X)
	}
	return ""
}

func calleeWaitGroupNameForReceiverAlias(fn *ast.FuncDecl, receiverName, suffix string) string {
	if fn == nil || fn.Body == nil || suffix == "" {
		return ""
	}

	candidates := map[string]bool{receiverName + suffix: true}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				if i >= len(node.Rhs) {
					continue
				}
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name == "_" {
					continue
				}
				rhsName := common.GetVarName(node.Rhs[i])
				if strings.HasPrefix(rhsName, receiverName+".") {
					candidates[ident.Name+suffix] = true
				}
			}
		case *ast.ValueSpec:
			for i, ident := range node.Names {
				if i >= len(node.Values) || ident == nil || ident.Name == "_" {
					continue
				}
				rhsName := common.GetVarName(node.Values[i])
				if strings.HasPrefix(rhsName, receiverName+".") {
					candidates[ident.Name+suffix] = true
				}
			}
		}
		return true
	})

	for candidate := range candidates {
		if functionBodyUsesWaitGroupName(fn.Body, candidate) {
			return candidate
		}
	}
	return ""
}

func functionBodyUsesWaitGroupName(body *ast.BlockStmt, wgName string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok &&
			common.GetVarName(sel.X) == wgName &&
			(sel.Sel.Name == "Add" || sel.Sel.Name == "Done" || sel.Sel.Name == "Wait" || sel.Sel.Name == "Go") {
			found = true
			return false
		}
		// The waitgroup may be delegated to a helper — `helper(wg)` or
		// `helper(&wg)` — that performs the Done on the caller's behalf. Treat
		// passing the waitgroup as an argument as a use so the interprocedural
		// resolution keeps following it into the helper.
		for _, arg := range call.Args {
			if isWaitGroupArgument(arg, wgName) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func calleeWaitGroupNameForArg(arg ast.Expr, wgName string, field *ast.Field, fieldIndex int) (string, bool) {
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
