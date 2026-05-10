package waitgroup

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/category"
)

type waitDonePositions struct {
	waits      []token.Pos
	dones      []token.Pos
	deferDones []token.Pos
}

func (wga *Analyzer) checkWaitAndDoneInSameGoroutine() {
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}

		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}

		for wgName := range wga.waitGroupNames {
			positions := wga.collectWaitDonePositions(fnLit.Body, wgName)
			for _, waitPos := range positions.waits {
				if wga.waitDependsOnDoneInSameGoroutine(waitPos, positions) {
					wga.errorCollector.AddError(waitPos, category.WaitDeadlock, "waitgroup '"+wgName+"' Wait will deadlock: same goroutine has pending Done")
				}
			}
		}

		return true
	})
}

func (wga *Analyzer) waitDependsOnDoneInSameGoroutine(waitPos token.Pos, positions waitDonePositions) bool {
	for _, donePos := range positions.dones {
		if donePos > waitPos {
			return true
		}
	}
	for range positions.deferDones {
		return true
	}
	return false
}

func (wga *Analyzer) collectWaitDonePositions(block *ast.BlockStmt, wgName string) waitDonePositions {
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
			if wga.commentFilter.ShouldSkipCall(e) {
				return
			}
			wga.recordWaitDoneCall(e, wgName, &positions)
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
		if stmt == nil || wga.commentFilter.ShouldSkipStatement(stmt) {
			return
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			visitExpr(s.X)
		case *ast.DeferStmt:
			if wga.commentFilter.ShouldSkipCall(s.Call) {
				return
			}
			if wga.deferInvokesDone(s, wgName) {
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
			wga.visitWaitDoneElse(s.Else, visitStmt, visitStmts)
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

func (wga *Analyzer) visitWaitDoneElse(stmt ast.Stmt, visitStmt func(ast.Stmt), visitStmts func([]ast.Stmt)) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		visitStmts(s.List)
	case *ast.IfStmt:
		visitStmt(s)
	}
}

func (wga *Analyzer) recordWaitDoneCall(call *ast.CallExpr, wgName string, positions *waitDonePositions) {
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

func (wga *Analyzer) deferInvokesDone(deferStmt *ast.DeferStmt, wgName string) bool {
	if wga.isSimpleDeferDone(deferStmt, wgName) || wga.isCallbackDeferDone(deferStmt, wgName) {
		return true
	}
	if fnLit, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok && fnLit.Body != nil {
		return wga.containsDoneCall(fnLit.Body, wgName)
	}
	return false
}

func (wga *Analyzer) checkDoneOutsideWorkerGoroutine() {
	for wgName := range wga.waitGroupNames {
		wga.checkDoneOutsideWorkerForWaitGroup(wgName)
	}
}

func (wga *Analyzer) checkDoneOutsideWorkerForWaitGroup(wgName string) {
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
		if stmt == nil || wga.commentFilter.ShouldSkipStatement(stmt) {
			return
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || wga.commentFilter.ShouldSkipCall(call) {
				return
			}
			if delta, ok := wga.addDelta(call, wgName); ok {
				if delta > 0 {
					pendingAdds += delta
					recentPositiveAdd = true
				}
				return
			}
			if wga.callInvokesDone(call, wgName) {
				recentPositiveAdd = false
				if workerGoroutinesWithoutDone > 0 {
					wga.errorCollector.AddError(call.Pos(), category.DoneOutsideGoroutine, "waitgroup '"+wgName+"' Done called outside worker goroutine")
					workerGoroutinesWithoutDone--
				}
				if pendingAdds > 0 {
					pendingAdds--
				}
			}
		case *ast.DeferStmt:
			recentPositiveAdd = false
			if wga.deferInvokesDone(s, wgName) {
				if workerGoroutinesWithoutDone > 0 {
					wga.errorCollector.AddError(s.Call.Pos(), category.DoneOutsideGoroutine, "waitgroup '"+wgName+"' Done called outside worker goroutine")
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
			doneInfo, related := wga.goroutineDoneInfo(s, wgName)
			if related && doneInfo.hasAnyDone {
				return
			}
			if wga.goroutineOnlyWaitsOnWaitGroup(s, wgName) {
				return
			}
			workerGoroutinesWithoutDone++
		case *ast.AssignStmt, *ast.DeclStmt, *ast.ReturnStmt:
			recentPositiveAdd = false
		case *ast.IfStmt:
			visitStmt(s.Init)
			visitStmts(s.Body.List)
			wga.visitMainFlowElse(s.Else, visitStmt, visitStmts)
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

	if wga.function.Body != nil {
		visitStmts(wga.function.Body.List)
	}
}

func (wga *Analyzer) visitMainFlowElse(stmt ast.Stmt, visitStmt func(ast.Stmt), visitStmts func([]ast.Stmt)) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		visitStmts(s.List)
	case *ast.IfStmt:
		visitStmt(s)
	}
}

func (wga *Analyzer) addDelta(call *ast.CallExpr, wgName string) (int, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Add" || common.GetVarName(sel.X) != wgName {
		return 0, false
	}

	addValue := common.GetAddValue(call)
	if len(call.Args) > 0 {
		if constantValue, ok := common.ConstantIntValue(call.Args[0], wga.typesInfo); ok {
			addValue = constantValue
		}
	}
	return addValue, true
}

func (wga *Analyzer) checkWaitGroupGoPanic() {
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || wga.commentFilter.ShouldSkipCall(call) {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Go" {
			return true
		}
		wgName := common.GetVarName(sel.X)
		if !wga.waitGroupNames[wgName] || len(call.Args) == 0 {
			return true
		}

		fnLit, ok := call.Args[0].(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}
		if wga.functionLiteralMayPanic(fnLit) {
			wga.errorCollector.AddError(call.Pos(), category.GoPanic, "waitgroup '"+wgName+"' Go function may panic")
		}
		return true
	})
}

func (wga *Analyzer) functionLiteralMayPanic(fnLit *ast.FuncLit) bool {
	if fnLit == nil || fnLit.Body == nil || wga.functionLiteralRecovers(fnLit) {
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
		if ok && wga.isBuiltinPanic(ident) {
			found = true
			return false
		}
		return true
	})
	return found
}

func (wga *Analyzer) functionLiteralRecovers(fnLit *ast.FuncLit) bool {
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
		if wga.deferCallRecovers(deferStmt.Call) {
			found = true
			return false
		}
		return true
	})
	return found
}

func (wga *Analyzer) deferCallRecovers(call *ast.CallExpr) bool {
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

func (wga *Analyzer) checkAddInsideGoroutine() {
	mainFlowWaits := make(map[string]bool)
	hasMainFlowWait := func(wgName string) bool {
		if cached, ok := mainFlowWaits[wgName]; ok {
			return cached
		}
		found := wga.hasMainFlowWait(wgName)
		mainFlowWaits[wgName] = found
		return found
	}

	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || wga.commentFilter.ShouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}

		ast.Inspect(fnLit.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok || wga.commentFilter.ShouldSkipCall(call) {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Add" {
				return true
			}
			wgName := common.GetVarName(sel.X)
			if !wga.waitGroupNames[wgName] || wga.waitGroupIdentDefinedInside(fnLit.Body, sel.X) {
				return true
			}
			if !hasMainFlowWait(wgName) {
				return true
			}
			wga.errorCollector.AddError(call.Pos(), category.AddInsideGoroutine, "waitgroup '"+wgName+"' Add called inside goroutine, may race with Wait")
			return true
		})

		return true
	})
}

func (wga *Analyzer) hasMainFlowWait(wgName string) bool {
	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || !wga.isInMainFunctionFlow(call.Pos()) {
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

func (wga *Analyzer) waitGroupIdentDefinedInside(body *ast.BlockStmt, expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok || body == nil || wga.typesInfo == nil {
		return false
	}
	obj := wga.typesInfo.Uses[ident]
	if obj == nil {
		obj = wga.typesInfo.Defs[ident]
	}
	return obj != nil && body.Pos() <= obj.Pos() && obj.Pos() <= body.End()
}

func (wga *Analyzer) checkMultipleDoneSameWorkerBranch() {
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || wga.commentFilter.ShouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}
		for wgName := range wga.waitGroupNames {
			wga.checkMultipleDoneInBranch(fnLit.Body.List, wgName, 0)
		}
		return true
	})
}

func (wga *Analyzer) checkMultipleDoneInBranch(stmts []ast.Stmt, wgName string, count int) int {
	current := count
	for _, stmt := range stmts {
		if stmt == nil || wga.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		switch s := stmt.(type) {
		case *ast.DeferStmt:
			if wga.deferInvokesDone(s, wgName) {
				current++
				if current > 1 {
					wga.errorCollector.AddError(s.Call.Pos(), category.MultipleDoneWorker, "waitgroup '"+wgName+"' Done called multiple times in the same worker branch")
				}
			}
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok && wga.callInvokesDone(call, wgName) {
				current++
				if current > 1 {
					wga.errorCollector.AddError(call.Pos(), category.MultipleDoneWorker, "waitgroup '"+wgName+"' Done called multiple times in the same worker branch")
				}
			}
		case *ast.IfStmt:
			wga.checkMultipleDoneInBranch(s.Body.List, wgName, current)
			if s.Else != nil {
				wga.checkMultipleDoneInElse(s.Else, wgName, current)
			}
		case *ast.BlockStmt:
			current = wga.checkMultipleDoneInBranch(s.List, wgName, current)
		case *ast.LabeledStmt:
			current = wga.checkMultipleDoneInBranch([]ast.Stmt{s.Stmt}, wgName, current)
		}
	}
	return current
}

func (wga *Analyzer) checkMultipleDoneInElse(stmt ast.Stmt, wgName string, count int) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		wga.checkMultipleDoneInBranch(s.List, wgName, count)
	case *ast.IfStmt:
		wga.checkMultipleDoneInBranch([]ast.Stmt{s}, wgName, count)
	}
}

func (wga *Analyzer) checkNestedWaitGroupDeadlock() {
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || wga.commentFilter.ShouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}

		outerGroups := wga.workerDoneWaitGroups(fnLit.Body)
		if len(outerGroups) == 0 {
			return true
		}
		wga.reportNestedWaitsForWorker(goStmt, fnLit.Body, outerGroups)
		return true
	})
}

func (wga *Analyzer) workerDoneWaitGroups(body *ast.BlockStmt) map[string]bool {
	outerGroups := make(map[string]bool)
	for wgName := range wga.waitGroupNames {
		if wga.analyzeDoneCallsWithVisited(body, wgName, make(map[token.Pos]bool)).hasAnyDone {
			outerGroups[wgName] = true
		}
	}
	return outerGroups
}

func (wga *Analyzer) reportNestedWaitsForWorker(goStmt *ast.GoStmt, body *ast.BlockStmt, outerGroups map[string]bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || wga.commentFilter.ShouldSkipCall(call) {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Wait" {
			return true
		}
		innerWG := common.GetVarName(sel.X)
		if !wga.waitGroupNames[innerWG] {
			return true
		}
		for outerWG := range outerGroups {
			if outerWG == innerWG {
				continue
			}
			if wga.workerReleasesWaitGroupBefore(body, outerWG, call.Pos()) {
				continue
			}
			if wga.outerWaitBeforeInnerRelease(goStmt.Pos(), outerWG, innerWG) {
				wga.errorCollector.AddError(call.Pos(), category.NestedWaitGroupDeadlock,
					"waitgroup '"+innerWG+"' Wait inside worker for waitgroup '"+outerWG+"' can deadlock")
				return false
			}
		}
		return true
	})
}

func (wga *Analyzer) workerReleasesWaitGroupBefore(body *ast.BlockStmt, wgName string, before token.Pos) bool {
	if body == nil {
		return false
	}
	for _, stmt := range body.List {
		if stmt == nil || stmt.Pos() >= before {
			break
		}
		if wga.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		if wga.topLevelStatementReleasesWaitGroup(stmt, wgName) {
			return true
		}
	}
	return false
}

func (wga *Analyzer) topLevelStatementReleasesWaitGroup(stmt ast.Stmt, wgName string) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		return ok && wga.callReleasesWaitGroup(call, wgName)
	case *ast.LabeledStmt:
		return wga.topLevelStatementReleasesWaitGroup(s.Stmt, wgName)
	default:
		return false
	}
}

func (wga *Analyzer) callReleasesWaitGroup(call *ast.CallExpr, wgName string) bool {
	if call == nil || wga.commentFilter.ShouldSkipCall(call) {
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
	delta, ok := wga.addDelta(call, wgName)
	return ok && delta < 0
}

func (wga *Analyzer) outerWaitBeforeInnerRelease(goPos token.Pos, outerWG, innerWG string) bool {
	var outerWait token.Pos
	var innerRelease token.Pos
	hasInnerAdd := false

	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || wga.isInGoroutine(call.Pos()) {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		wgName := common.GetVarName(sel.X)
		switch {
		case wgName == innerWG && sel.Sel.Name == "Add" && call.Pos() < goPos:
			if delta, ok := wga.addDelta(call, innerWG); ok && delta > 0 {
				hasInnerAdd = true
			}
		case wgName == outerWG && sel.Sel.Name == "Wait" && call.Pos() > goPos && outerWait == token.NoPos:
			outerWait = call.Pos()
		case wgName == innerWG && sel.Sel.Name == "Done" && outerWait != token.NoPos && call.Pos() > outerWait && innerRelease == token.NoPos:
			innerRelease = call.Pos()
		case wgName == innerWG && sel.Sel.Name == "Add" && outerWait != token.NoPos && call.Pos() > outerWait && innerRelease == token.NoPos:
			if delta, ok := wga.addDelta(call, innerWG); ok && delta < 0 {
				innerRelease = call.Pos()
			}
		}
		return true
	})

	return hasInnerAdd && outerWait != token.NoPos && innerRelease != token.NoPos
}
