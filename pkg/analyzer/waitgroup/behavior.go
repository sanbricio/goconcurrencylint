package waitgroup

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
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
					wga.errorCollector.AddError(waitPos, "waitgroup '"+wgName+"' Wait will deadlock: same goroutine has pending Done")
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
					wga.errorCollector.AddError(call.Pos(), "waitgroup '"+wgName+"' Done called outside worker goroutine")
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
					wga.errorCollector.AddError(s.Call.Pos(), "waitgroup '"+wgName+"' Done called outside worker goroutine")
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
			wga.errorCollector.AddError(call.Pos(), "waitgroup '"+wgName+"' Go function may panic")
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
		if ok && ident.Name == "panic" {
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
