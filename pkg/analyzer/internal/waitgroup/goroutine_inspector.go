package waitgroup

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

type deferDoneDetector func(*ast.DeferStmt, string) bool
type doneCallChecker func(*ast.CallExpr, string) bool
type goroutineDoneAnalyzer func(*ast.GoStmt, string) (doneCallInfo, bool)
type waitOnlyChecker func(*ast.GoStmt, string) bool
type doneBlockAnalyzer func(*ast.BlockStmt, string, map[token.Pos]bool) doneCallInfo
type inGoroutineChecker func(token.Pos) bool
type mainFlowChecker func(token.Pos) bool
type builtinPanicChecker func(*ast.Ident) bool

type waitDonePositions struct {
	waits      []token.Pos
	dones      []token.Pos
	deferDones []token.Pos
}

// goroutineInspector groups diagnostics that reason about WaitGroup behavior
// inside worker goroutines.
type goroutineInspector struct {
	waitGroupNames     map[string]bool
	commentFilter      *commentfilter.CommentFilter
	reporter           report.Reporter
	deferInvokesDone   deferDoneDetector
	callInvokesDone    doneCallChecker
	goroutineDoneInfo  goroutineDoneAnalyzer
	goroutineOnlyWaits waitOnlyChecker
	analyzeDoneCalls   doneBlockAnalyzer
	isInGoroutine      inGoroutineChecker
	typesInfo          *types.Info
	isInMainFlow       mainFlowChecker
	isBuiltinPanic     builtinPanicChecker
}

func newGoroutineInspector(
	waitGroupNames map[string]bool,
	cf *commentfilter.CommentFilter,
	reporter report.Reporter,
	deferInvokesDone deferDoneDetector,
	callInvokesDone doneCallChecker,
	goroutineDoneInfo goroutineDoneAnalyzer,
	goroutineOnlyWaits waitOnlyChecker,
	analyzeDoneCalls doneBlockAnalyzer,
	isInGoroutine inGoroutineChecker,
	typesInfo *types.Info,
	isInMainFlow mainFlowChecker,
	isBuiltinPanic builtinPanicChecker,
) *goroutineInspector {
	return &goroutineInspector{
		waitGroupNames:     waitGroupNames,
		commentFilter:      cf,
		reporter:           reporter,
		deferInvokesDone:   deferInvokesDone,
		callInvokesDone:    callInvokesDone,
		goroutineDoneInfo:  goroutineDoneInfo,
		goroutineOnlyWaits: goroutineOnlyWaits,
		analyzeDoneCalls:   analyzeDoneCalls,
		isInGoroutine:      isInGoroutine,
		typesInfo:          typesInfo,
		isInMainFlow:       isInMainFlow,
		isBuiltinPanic:     isBuiltinPanic,
	}
}

func (g *goroutineInspector) checkDoneOutsideWorkerGoroutine(fn *ast.FuncDecl) {
	if g == nil || fn == nil || fn.Body == nil {
		return
	}

	for wgName := range g.waitGroupNames {
		g.checkDoneOutsideWorkerForWaitGroup(fn, wgName)
	}
}

func (g *goroutineInspector) checkDoneOutsideWorkerForWaitGroup(fn *ast.FuncDecl, wgName string) {
	pendingAdds := 0
	// Keep this intentionally narrow: only an Add immediately followed by a
	// worker goroutine is treated as an ownership handoff.
	recentPositiveAdd := false
	workerGoroutinesWithoutDone := 0

	var (
		visitStmt  func(ast.Stmt)
		visitStmts func([]ast.Stmt)
	)

	visitStmts = func(stmts []ast.Stmt) {
		for _, stmt := range stmts {
			visitStmt(stmt)
		}
	}

	visitStmt = func(stmt ast.Stmt) {
		if stmt == nil || g.shouldSkipStatement(stmt) {
			return
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || g.shouldSkipCall(call) {
				return
			}
			if delta, ok := g.addDelta(call, wgName); ok {
				if delta > 0 {
					pendingAdds += delta
					recentPositiveAdd = true
				}
				return
			}
			if g.callInvokesDone != nil && g.callInvokesDone(call, wgName) {
				recentPositiveAdd = false
				if workerGoroutinesWithoutDone > 0 {
					g.reporter.AddError(call.Pos(), category.DoneOutsideGoroutine, "waitgroup '"+wgName+"' Done called outside worker goroutine")
					workerGoroutinesWithoutDone--
				}
				if pendingAdds > 0 {
					pendingAdds--
				}
			}
		case *ast.DeferStmt:
			recentPositiveAdd = false
			if g.deferInvokesDone != nil && g.deferInvokesDone(s, wgName) {
				if workerGoroutinesWithoutDone > 0 {
					g.reporter.AddError(s.Call.Pos(), category.DoneOutsideGoroutine, "waitgroup '"+wgName+"' Done called outside worker goroutine")
					workerGoroutinesWithoutDone--
				}
				if pendingAdds > 0 {
					pendingAdds--
				}
			}
		case *ast.GoStmt:
			if pendingAdds <= 0 || !recentPositiveAdd {
				recentPositiveAdd = false
				return
			}
			recentPositiveAdd = false
			if g.goroutineDoneInfo != nil {
				doneInfo, related := g.goroutineDoneInfo(s, wgName)
				if related && doneInfo.hasAnyDone {
					return
				}
			}
			if g.goroutineOnlyWaits != nil && g.goroutineOnlyWaits(s, wgName) {
				return
			}
			workerGoroutinesWithoutDone++
		case *ast.AssignStmt, *ast.DeclStmt, *ast.ReturnStmt:
			recentPositiveAdd = false
		case *ast.IfStmt:
			visitStmt(s.Init)
			visitStmts(s.Body.List)
			visitMainFlowElse(s.Else, visitStmt, visitStmts)
			recentPositiveAdd = false
		case *ast.ForStmt:
			visitStmt(s.Init)
			visitStmts(s.Body.List)
			visitStmt(s.Post)
			recentPositiveAdd = false
		case *ast.RangeStmt:
			visitStmts(s.Body.List)
			recentPositiveAdd = false
		case *ast.BlockStmt:
			visitStmts(s.List)
		case *ast.LabeledStmt:
			visitStmt(s.Stmt)
		case *ast.SwitchStmt:
			visitStmt(s.Init)
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					visitStmts(cc.Body)
				}
			}
			recentPositiveAdd = false
		case *ast.TypeSwitchStmt:
			visitStmt(s.Init)
			visitStmt(s.Assign)
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					visitStmts(cc.Body)
				}
			}
			recentPositiveAdd = false
		case *ast.SelectStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CommClause); ok {
					visitStmt(cc.Comm)
					visitStmts(cc.Body)
				}
			}
			recentPositiveAdd = false
		}
	}

	visitStmts(fn.Body.List)
}

func (g *goroutineInspector) checkAddInsideGoroutine(fn *ast.FuncDecl) {
	if g == nil || fn == nil || fn.Body == nil {
		return
	}

	mainFlowWaits := make(map[string]bool)
	hasMainFlowWait := func(wgName string) bool {
		if cached, ok := mainFlowWaits[wgName]; ok {
			return cached
		}
		found := g.hasMainFlowWait(fn, wgName)
		mainFlowWaits[wgName] = found
		return found
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || g.shouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}

		ast.Inspect(fnLit.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok || g.shouldSkipCall(call) {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Add" {
				return true
			}
			wgName := common.GetVarName(sel.X)
			if !g.waitGroupNames[wgName] || g.waitGroupIdentDefinedInside(fnLit.Body, sel.X) {
				return true
			}
			if !hasMainFlowWait(wgName) {
				return true
			}
			g.reporter.AddError(call.Pos(), category.AddInsideGoroutine, "waitgroup '"+wgName+"' Add called inside goroutine, may race with Wait")
			return true
		})

		return true
	})
}

func (g *goroutineInspector) checkWaitAndDoneInSameGoroutine(fn *ast.FuncDecl) {
	if g == nil || fn == nil || fn.Body == nil {
		return
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}

		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}

		for wgName := range g.waitGroupNames {
			positions := g.collectWaitDonePositions(fnLit.Body, wgName)
			for _, waitPos := range positions.waits {
				if waitDependsOnDoneInSameGoroutine(waitPos, positions) {
					g.reporter.AddError(waitPos, category.WaitDeadlock, "waitgroup '"+wgName+"' Wait will deadlock: same goroutine has pending Done")
				}
			}
		}

		return true
	})
}

func (g *goroutineInspector) checkWaitGroupGoPanic(fn *ast.FuncDecl) {
	if g == nil || fn == nil || fn.Body == nil {
		return
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || g.shouldSkipCall(call) {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Go" {
			return true
		}
		wgName := common.GetVarName(sel.X)
		if !g.waitGroupNames[wgName] || len(call.Args) == 0 {
			return true
		}

		fnLit, ok := call.Args[0].(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}
		if g.functionLiteralMayPanic(fnLit) {
			g.reporter.AddError(call.Pos(), category.GoPanic, "waitgroup '"+wgName+"' Go function may panic")
		}
		return true
	})
}

func (g *goroutineInspector) checkMultipleDoneSameWorkerBranch(fn *ast.FuncDecl) {
	if g == nil || fn == nil || fn.Body == nil {
		return
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || g.shouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}
		for wgName := range g.waitGroupNames {
			g.checkMultipleDoneInBranch(fnLit.Body.List, wgName, 0)
		}
		return true
	})
}

func (g *goroutineInspector) checkNestedWaitGroupDeadlock(fn *ast.FuncDecl) {
	if g == nil || fn == nil || fn.Body == nil {
		return
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || g.shouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}

		outerGroups := g.workerDoneWaitGroups(fnLit.Body)
		if len(outerGroups) == 0 {
			return true
		}
		g.reportNestedWaitsForWorker(fn, goStmt, fnLit.Body, outerGroups)
		return true
	})
}

func (g *goroutineInspector) hasMainFlowWait(fn *ast.FuncDecl, wgName string) bool {
	if fn == nil || fn.Body == nil {
		return false
	}

	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || !g.isMainFlow(call.Pos()) {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Wait" || common.GetVarName(sel.X) != wgName {
			return true
		}
		found = true
		return false
	})
	return found
}

func (g *goroutineInspector) waitGroupIdentDefinedInside(body *ast.BlockStmt, expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok || body == nil || g.typesInfo == nil {
		return false
	}
	obj := g.typesInfo.Uses[ident]
	if obj == nil {
		obj = g.typesInfo.Defs[ident]
	}
	return obj != nil && body.Pos() <= obj.Pos() && obj.Pos() <= body.End()
}

func (g *goroutineInspector) workerDoneWaitGroups(body *ast.BlockStmt) map[string]bool {
	outerGroups := make(map[string]bool)
	if g.analyzeDoneCalls == nil {
		return outerGroups
	}
	for wgName := range g.waitGroupNames {
		if g.analyzeDoneCalls(body, wgName, make(map[token.Pos]bool)).hasAnyDone {
			outerGroups[wgName] = true
		}
	}
	return outerGroups
}

func (g *goroutineInspector) reportNestedWaitsForWorker(fn *ast.FuncDecl, goStmt *ast.GoStmt, body *ast.BlockStmt, outerGroups map[string]bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || g.shouldSkipCall(call) {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Wait" {
			return true
		}
		innerWG := common.GetVarName(sel.X)
		if !g.waitGroupNames[innerWG] {
			return true
		}
		for outerWG := range outerGroups {
			if outerWG == innerWG {
				continue
			}
			if g.workerReleasesWaitGroupBefore(body, outerWG, call.Pos()) {
				continue
			}
			if g.outerWaitBeforeInnerRelease(fn, goStmt.Pos(), outerWG, innerWG) {
				g.reporter.AddError(call.Pos(), category.NestedWaitGroupDeadlock,
					"waitgroup '"+innerWG+"' Wait inside worker for waitgroup '"+outerWG+"' can deadlock")
				return false
			}
		}
		return true
	})
}

func (g *goroutineInspector) functionLiteralMayPanic(fnLit *ast.FuncLit) bool {
	if fnLit == nil || fnLit.Body == nil || g.functionLiteralRecovers(fnLit) {
		return false
	}

	found := false
	ast.Inspect(fnLit.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		if nested, ok := n.(*ast.FuncLit); ok && nested != fnLit {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if ok && g.callIsBuiltinPanic(ident) {
			found = true
			return false
		}
		return true
	})
	return found
}

func (g *goroutineInspector) functionLiteralRecovers(fnLit *ast.FuncLit) bool {
	if fnLit == nil || fnLit.Body == nil {
		return false
	}

	found := false
	ast.Inspect(fnLit.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		if nested, ok := n.(*ast.FuncLit); ok && nested != fnLit {
			return false
		}
		deferStmt, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		if g.deferCallRecovers(deferStmt.Call) {
			found = true
			return false
		}
		return true
	})
	return found
}

func (g *goroutineInspector) deferCallRecovers(call *ast.CallExpr) bool {
	if call == nil {
		return false
	}

	if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "recover" {
		return true
	}

	fnLit, ok := call.Fun.(*ast.FuncLit)
	if !ok || fnLit.Body == nil {
		return false
	}

	found := false
	ast.Inspect(fnLit.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		if nested, ok := n.(*ast.FuncLit); ok && nested != fnLit {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if ok && ident.Name == "recover" {
			found = true
			return false
		}
		return true
	})
	return found
}

func (g *goroutineInspector) checkMultipleDoneInBranch(stmts []ast.Stmt, wgName string, count int) int {
	current := count
	for _, stmt := range stmts {
		if stmt == nil || g.shouldSkipStatement(stmt) {
			continue
		}
		switch s := stmt.(type) {
		case *ast.DeferStmt:
			if g.deferInvokesDone != nil && g.deferInvokesDone(s, wgName) {
				current++
				if current > 1 {
					g.reporter.AddError(s.Call.Pos(), category.MultipleDoneWorker, "waitgroup '"+wgName+"' Done called multiple times in the same worker branch")
				}
			}
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok && g.callInvokesDone != nil && g.callInvokesDone(call, wgName) {
				current++
				if current > 1 {
					g.reporter.AddError(call.Pos(), category.MultipleDoneWorker, "waitgroup '"+wgName+"' Done called multiple times in the same worker branch")
				}
			}
		case *ast.IfStmt:
			g.checkMultipleDoneInBranch(s.Body.List, wgName, current)
			if s.Else != nil {
				g.checkMultipleDoneInElse(s.Else, wgName, current)
			}
		case *ast.BlockStmt:
			current = g.checkMultipleDoneInBranch(s.List, wgName, current)
		case *ast.LabeledStmt:
			current = g.checkMultipleDoneInBranch([]ast.Stmt{s.Stmt}, wgName, current)
		}
	}
	return current
}

func (g *goroutineInspector) checkMultipleDoneInElse(stmt ast.Stmt, wgName string, count int) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		g.checkMultipleDoneInBranch(s.List, wgName, count)
	case *ast.IfStmt:
		g.checkMultipleDoneInBranch([]ast.Stmt{s}, wgName, count)
	}
}

func (g *goroutineInspector) workerReleasesWaitGroupBefore(body *ast.BlockStmt, wgName string, before token.Pos) bool {
	if body == nil {
		return false
	}
	for _, stmt := range body.List {
		if stmt == nil || stmt.Pos() >= before {
			break
		}
		if g.shouldSkipStatement(stmt) {
			continue
		}
		if g.topLevelStatementReleasesWaitGroup(stmt, wgName) {
			return true
		}
	}
	return false
}

func (g *goroutineInspector) topLevelStatementReleasesWaitGroup(stmt ast.Stmt, wgName string) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		return ok && g.callReleasesWaitGroup(call, wgName)
	case *ast.LabeledStmt:
		return g.topLevelStatementReleasesWaitGroup(s.Stmt, wgName)
	default:
		return false
	}
}

func (g *goroutineInspector) callReleasesWaitGroup(call *ast.CallExpr, wgName string) bool {
	if call == nil || g.shouldSkipCall(call) {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || common.GetVarName(sel.X) != wgName {
		return false
	}
	if sel.Sel.Name == "Done" {
		return true
	}
	if sel.Sel.Name != "Add" {
		return false
	}
	delta, ok := g.addDelta(call, wgName)
	return ok && delta < 0
}

func (g *goroutineInspector) outerWaitBeforeInnerRelease(fn *ast.FuncDecl, goPos token.Pos, outerWG, innerWG string) bool {
	if fn == nil || fn.Body == nil {
		return false
	}

	var outerWait token.Pos
	var innerRelease token.Pos
	hasInnerAdd := false

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || g.isInsideGoroutine(call.Pos()) {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		wgName := common.GetVarName(sel.X)
		switch {
		case wgName == innerWG && sel.Sel.Name == "Add" && call.Pos() < goPos:
			if delta, ok := g.addDelta(call, innerWG); ok && delta > 0 {
				hasInnerAdd = true
			}
		case wgName == outerWG && sel.Sel.Name == "Wait" && call.Pos() > goPos && outerWait == token.NoPos:
			outerWait = call.Pos()
		case wgName == innerWG && sel.Sel.Name == "Done" && outerWait != token.NoPos && call.Pos() > outerWait && innerRelease == token.NoPos:
			innerRelease = call.Pos()
		case wgName == innerWG && sel.Sel.Name == "Add" && outerWait != token.NoPos && call.Pos() > outerWait && innerRelease == token.NoPos:
			if delta, ok := g.addDelta(call, innerWG); ok && delta < 0 {
				innerRelease = call.Pos()
			}
		}
		return true
	})

	return hasInnerAdd && outerWait != token.NoPos && innerRelease != token.NoPos
}

func (g *goroutineInspector) addDelta(call *ast.CallExpr, wgName string) (int, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Add" || common.GetVarName(sel.X) != wgName {
		return 0, false
	}

	addValue := common.GetAddValue(call)
	if len(call.Args) > 0 {
		if constantValue, ok := common.ConstantIntValue(call.Args[0], g.typesInfo); ok {
			addValue = constantValue
		}
	}
	return addValue, true
}

func waitDependsOnDoneInSameGoroutine(waitPos token.Pos, positions waitDonePositions) bool {
	for _, donePos := range positions.dones {
		if donePos > waitPos {
			return true
		}
	}
	return len(positions.deferDones) > 0
}

func (g *goroutineInspector) collectWaitDonePositions(block *ast.BlockStmt, wgName string) waitDonePositions {
	positions := waitDonePositions{}

	var visitStmt func(ast.Stmt)
	var visitStmts func([]ast.Stmt)
	var visitExpr func(ast.Expr)

	visitStmts = func(stmts []ast.Stmt) {
		for _, stmt := range stmts {
			visitStmt(stmt)
		}
	}

	visitExpr = func(expr ast.Expr) {
		switch e := expr.(type) {
		case *ast.CallExpr:
			if g.shouldSkipCall(e) {
				return
			}
			recordWaitDoneCall(e, wgName, &positions)
			if fnLit, ok := e.Fun.(*ast.FuncLit); ok && fnLit.Body != nil {
				visitStmts(fnLit.Body.List)
			}
		case *ast.UnaryExpr:
			visitExpr(e.X)
		case *ast.BinaryExpr:
			visitExpr(e.X)
			visitExpr(e.Y)
		case *ast.ParenExpr:
			visitExpr(e.X)
		}
	}

	visitStmt = func(stmt ast.Stmt) {
		if stmt == nil || g.shouldSkipStatement(stmt) {
			return
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			visitExpr(s.X)
		case *ast.DeferStmt:
			if g.shouldSkipCall(s.Call) {
				return
			}
			if g.deferInvokesDone != nil && g.deferInvokesDone(s, wgName) {
				positions.deferDones = append(positions.deferDones, s.Call.Pos())
			}
		case *ast.AssignStmt:
			for _, expr := range s.Rhs {
				visitExpr(expr)
			}
		case *ast.DeclStmt:
			if gen, ok := s.Decl.(*ast.GenDecl); ok {
				for _, spec := range gen.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, expr := range vs.Values {
							visitExpr(expr)
						}
					}
				}
			}
		case *ast.ReturnStmt:
			for _, expr := range s.Results {
				visitExpr(expr)
			}
		case *ast.IfStmt:
			visitStmt(s.Init)
			visitExpr(s.Cond)
			visitStmts(s.Body.List)
			visitWaitDoneElse(s.Else, visitStmt, visitStmts)
		case *ast.ForStmt:
			visitStmt(s.Init)
			visitExpr(s.Cond)
			visitStmt(s.Post)
			visitStmts(s.Body.List)
		case *ast.RangeStmt:
			visitExpr(s.X)
			visitStmts(s.Body.List)
		case *ast.BlockStmt:
			visitStmts(s.List)
		case *ast.LabeledStmt:
			visitStmt(s.Stmt)
		case *ast.SwitchStmt:
			visitStmt(s.Init)
			visitExpr(s.Tag)
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					for _, expr := range cc.List {
						visitExpr(expr)
					}
					visitStmts(cc.Body)
				}
			}
		case *ast.TypeSwitchStmt:
			visitStmt(s.Init)
			visitStmt(s.Assign)
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					visitStmts(cc.Body)
				}
			}
		case *ast.SelectStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CommClause); ok {
					visitStmt(cc.Comm)
					visitStmts(cc.Body)
				}
			}
		}
	}

	if block != nil {
		visitStmts(block.List)
	}
	return positions
}

func visitWaitDoneElse(stmt ast.Stmt, visitStmt func(ast.Stmt), visitStmts func([]ast.Stmt)) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		visitStmts(s.List)
	case *ast.IfStmt:
		visitStmt(s)
	}
}

func visitMainFlowElse(stmt ast.Stmt, visitStmt func(ast.Stmt), visitStmts func([]ast.Stmt)) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		visitStmts(s.List)
	case *ast.IfStmt:
		visitStmt(s)
	}
}

func recordWaitDoneCall(call *ast.CallExpr, wgName string, positions *waitDonePositions) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || common.GetVarName(sel.X) != wgName {
		return
	}

	switch sel.Sel.Name {
	case "Wait":
		positions.waits = append(positions.waits, call.Pos())
	case "Done":
		positions.dones = append(positions.dones, call.Pos())
	}
}

func (g *goroutineInspector) shouldSkipCall(call *ast.CallExpr) bool {
	return g.commentFilter != nil && g.commentFilter.ShouldSkipCall(call)
}

func (g *goroutineInspector) shouldSkipStatement(stmt ast.Stmt) bool {
	return g.commentFilter != nil && g.commentFilter.ShouldSkipStatement(stmt)
}

func (g *goroutineInspector) isMainFlow(pos token.Pos) bool {
	return g.isInMainFlow == nil || g.isInMainFlow(pos)
}

func (g *goroutineInspector) isInsideGoroutine(pos token.Pos) bool {
	return g.isInGoroutine != nil && g.isInGoroutine(pos)
}

func (g *goroutineInspector) callIsBuiltinPanic(ident *ast.Ident) bool {
	if g.isBuiltinPanic == nil {
		return ident != nil && ident.Name == "panic"
	}
	return g.isBuiltinPanic(ident)
}
