package waitgroup

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"golang.org/x/tools/go/ast/astutil"
)

// validateUsage performs validation checks on collected statistics
func (wga *Checker) validateUsage(stats map[string]*Stats) {
	goroutines := newGoroutineInspector(wga.waitGroupNames, wga.commentFilter, wga.errorCollector, wga.deferInvokesDone, wga.typesInfo, wga.isInMainFunctionFlow, wga.isBuiltinPanic)
	goroutines.checkAddInsideGoroutine(wga.function)
	wga.checkDoneNotDeferredInWorker()
	wga.checkLiteralAddLoopGoroutineMismatch(stats)
	wga.checkWaitWithoutAdd(stats)
	wga.checkMultipleDoneSameWorkerBranch()
	wga.checkNestedWaitGroupDeadlock()
	wga.checkAddAfterWait(stats)
	wga.checkWaitBeforeDoneSameGoroutine(stats)
	goroutines.checkWaitAndDoneInSameGoroutine(wga.function)
	wga.checkDoneOutsideWorkerGoroutine()
	goroutines.checkWaitGroupGoPanic(wga.function)
	wga.checkLoopAddDoneBalance()
	wga.checkUnreachableDone()
	wga.checkWaitGroupBalance(stats)
}

// validateBalance performs the actual balance validation for a WaitGroup
func (wga *Checker) validateBalance(wgName string, stats *Stats) {
	// Count Done calls from main flow (not in goroutines)
	mainFlowDoneCount := wga.countMainFlowDoneCalls(wgName)

	totalDone := mainFlowDoneCount

	for _, deferDonePos := range stats.deferDoneCalls {
		if !wga.isInGoroutine(deferDonePos) {
			totalDone++
		}
	}

	// Add guaranteed Done calls from goroutines (but don't double count)
	guaranteedFromGoroutines := wga.countGuaranteedDoneInGoroutines(wgName)
	totalDone += guaranteedFromGoroutines

	if stats.totalAdd > totalDone {
		wga.reportUnmatchedAdds(wgName, stats, totalDone)
	}

	if totalDone > stats.totalAdd {
		wga.reportExcessDones(wgName, stats, totalDone, mainFlowDoneCount)
	}
}

func (wga *Checker) countMainFlowDoneCalls(wgName string) int {
	if wga.function == nil || wga.function.Body == nil {
		return 0
	}
	return wga.countMainFlowDoneInStatements(wga.function.Body.List, wgName, 1)
}

func (wga *Checker) countMainFlowDoneInStatements(stmts []ast.Stmt, wgName string, multiplier int) int {
	count := 0
	for _, stmt := range stmts {
		if stmt == nil || wga.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if ok && wga.callInvokesDone(call, wgName) {
				count += multiplier
			}
		case *ast.GoStmt:
			continue
		case *ast.BlockStmt:
			count += wga.countMainFlowDoneInStatements(s.List, wgName, multiplier)
		case *ast.IfStmt:
			count += wga.countMainFlowDoneInStatements(s.Body.List, wgName, multiplier)
			if s.Else != nil {
				count += wga.countMainFlowDoneInElse(s.Else, wgName, multiplier)
			}
		case *ast.ForStmt:
			count += wga.countMainFlowDoneInStatements(s.Body.List, wgName, multiplier*wga.estimateForIterations(s))
		case *ast.RangeStmt:
			count += wga.countMainFlowDoneInStatements(s.Body.List, wgName, multiplier*wga.estimateRangeIterations(s))
		case *ast.SwitchStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					count += wga.countMainFlowDoneInStatements(cc.Body, wgName, multiplier)
				}
			}
		case *ast.TypeSwitchStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					count += wga.countMainFlowDoneInStatements(cc.Body, wgName, multiplier)
				}
			}
		case *ast.SelectStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CommClause); ok {
					count += wga.countMainFlowDoneInStatements(cc.Body, wgName, multiplier)
				}
			}
		case *ast.LabeledStmt:
			count += wga.countMainFlowDoneInStatements([]ast.Stmt{s.Stmt}, wgName, multiplier)
		}
	}
	return count
}

func (wga *Checker) countMainFlowDoneInElse(stmt ast.Stmt, wgName string, multiplier int) int {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return wga.countMainFlowDoneInStatements(s.List, wgName, multiplier)
	case *ast.IfStmt:
		return wga.countMainFlowDoneInStatements([]ast.Stmt{s}, wgName, multiplier)
	default:
		return 0
	}
}

// countGuaranteedDoneInGoroutines counts Done calls that are guaranteed to execute in goroutines
func (wga *Checker) countGuaranteedDoneInGoroutines(wgName string) int {
	return wga.countGuaranteedDoneInStatements(wga.function.Body.List, wgName, 1)
}

// checkWaitGroupBalance validates that Add and Done calls are properly balanced
func (wga *Checker) checkWaitGroupBalance(stats map[string]*Stats) {
	for wgName, st := range stats {
		if wga.isBorrowedWaitGroupField(wgName, st) {
			continue
		}
		if wga.isLikelyExternalLifecycleWaitGroup(wgName, st) {
			continue
		}
		if wga.escape != nil && wga.escape.isWaitGroupPassedToOtherFunctions(wgName) {
			if len(st.addCalls) > 0 {
				continue
			}
		}
		wga.validateBalance(wgName, st)
	}
}

func (wga *Checker) isBorrowedWaitGroupField(wgName string, st *Stats) bool {
	return strings.Contains(wgName, ".") && st.totalAdd == 0 && len(st.waitCalls) == 0 && (st.doneCount > 0 || len(st.deferDoneCalls) > 0)
}

func (wga *Checker) isLikelyExternalLifecycleWaitGroup(wgName string, st *Stats) bool {
	if !strings.Contains(wgName, ".") {
		return false
	}
	if st.totalAdd == 0 || st.doneCount > 0 || len(st.deferDoneCalls) > 0 || len(st.waitCalls) > 0 || len(st.goCalls) > 0 {
		return false
	}
	return true
}

func (wga *Checker) countGuaranteedDoneInStatements(stmts []ast.Stmt, wgName string, multiplier int) int {
	count := 0

	for _, stmt := range stmts {
		if wga.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.GoStmt:
			if fnLit, ok := s.Call.Fun.(*ast.FuncLit); ok {
				nestedCount := wga.countGuaranteedDoneInStatements(fnLit.Body.List, wgName, multiplier)
				if nestedCount > 0 {
					count += nestedCount
					continue
				}
			}

			doneInfo, related := wga.goroutineDoneInfo(s, wgName)
			if !related {
				continue
			}
			if doneInfo.hasGuaranteedDone {
				count += multiplier
				continue
			}
			if wga.goroutineOnlyWaitsOnWaitGroup(s, wgName) {
				continue
			}
			if !doneInfo.hasAnyDone {
				relatedAdd := wga.findRelatedAddCall(s, wgName)
				if relatedAdd != token.NoPos {
					wga.errorCollector.AddError(relatedAdd, category.AddWithoutDone,
						"waitgroup '"+wgName+"' has Add without corresponding Done")
				}
			}

		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			if fnLit, ok := call.Fun.(*ast.FuncLit); ok && fnLit.Body != nil {
				count += wga.countGuaranteedDoneInStatements(fnLit.Body.List, wgName, multiplier)
			}

		case *ast.BlockStmt:
			count += wga.countGuaranteedDoneInStatements(s.List, wgName, multiplier)

		case *ast.IfStmt:
			count += wga.countGuaranteedDoneInStatements(s.Body.List, wgName, multiplier)
			if elseBlock, ok := s.Else.(*ast.BlockStmt); ok {
				count += wga.countGuaranteedDoneInStatements(elseBlock.List, wgName, multiplier)
			} else if elseIf, ok := s.Else.(*ast.IfStmt); ok {
				count += wga.countGuaranteedDoneInStatements([]ast.Stmt{elseIf}, wgName, multiplier)
			}

		case *ast.ForStmt:
			factor := multiplier * wga.estimateForIterations(s)
			count += wga.countGuaranteedDoneInStatements(s.Body.List, wgName, factor)

		case *ast.RangeStmt:
			factor := multiplier * wga.estimateRangeIterations(s)
			count += wga.countGuaranteedDoneInStatements(s.Body.List, wgName, factor)

		case *ast.SwitchStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					count += wga.countGuaranteedDoneInStatements(cc.Body, wgName, multiplier)
				}
			}

		case *ast.TypeSwitchStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					count += wga.countGuaranteedDoneInStatements(cc.Body, wgName, multiplier)
				}
			}

		case *ast.SelectStmt:
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CommClause); ok {
					count += wga.countGuaranteedDoneInStatements(cc.Body, wgName, multiplier)
				}
			}

		case *ast.LabeledStmt:
			count += wga.countGuaranteedDoneInStatements([]ast.Stmt{s.Stmt}, wgName, multiplier)
		}
	}

	return count
}

func (wga *Checker) estimateForIterations(forStmt *ast.ForStmt) int {
	if iterations, ok := wga.estimateForIterationsKnown(forStmt); ok {
		return iterations
	}
	return 1
}

func (wga *Checker) estimateForIterationsKnown(forStmt *ast.ForStmt) (int, bool) {
	if forStmt == nil {
		return 0, false
	}

	start := 0
	counterName := ""

	if init, ok := forStmt.Init.(*ast.AssignStmt); ok && len(init.Lhs) == 1 && len(init.Rhs) == 1 {
		if ident, ok := init.Lhs[0].(*ast.Ident); ok {
			if value, ok := common.ConstantIntValue(init.Rhs[0], wga.typesInfo); ok {
				start = value
				counterName = ident.Name
			}
		}
	}

	if counterName == "" {
		return 0, false
	}

	cond, ok := forStmt.Cond.(*ast.BinaryExpr)
	if !ok {
		return 0, false
	}
	left, ok := cond.X.(*ast.Ident)
	if !ok || left.Name != counterName {
		return 0, false
	}
	limit, ok := common.ConstantIntValue(cond.Y, wga.typesInfo)
	if !ok {
		return 0, false
	}

	if !wga.loopIncrementsCounterByOne(forStmt, counterName) {
		return 0, false
	}

	switch cond.Op {
	case token.LSS:
		if limit <= start {
			return 1, true
		}
		return limit - start, true
	case token.LEQ:
		if limit < start {
			return 1, true
		}
		return limit - start + 1, true
	default:
		return 0, false
	}
}

func (wga *Checker) estimateRangeIterations(rangeStmt *ast.RangeStmt) int {
	if iterations, ok := wga.estimateRangeIterationsKnown(rangeStmt); ok {
		return iterations
	}
	return 1
}

func (wga *Checker) estimateRangeIterationsKnown(rangeStmt *ast.RangeStmt) (int, bool) {
	if rangeStmt == nil || rangeStmt.X == nil {
		return 0, false
	}

	if lit, ok := rangeStmt.X.(*ast.CompositeLit); ok {
		return len(lit.Elts), true
	}
	if ident, ok := rangeStmt.X.(*ast.Ident); ok {
		if length, ok := wga.collectionLengthBefore(ident.Name, rangeStmt.Pos()); ok {
			return length, true
		}
	}

	if tv, ok := wga.typesInfo.Types[rangeStmt.X]; ok && tv.Value != nil {
		if value, ok := constant.Int64Val(tv.Value); ok && value > 0 {
			return int(value), true
		}
	}

	return 0, false
}

func (wga *Checker) loopIncrementsCounterByOne(forStmt *ast.ForStmt, counterName string) bool {
	if forStmt == nil || counterName == "" {
		return false
	}
	if forStmt.Post != nil {
		return wga.statementIncrementsCounterByOne(forStmt.Post, counterName)
	}
	for _, stmt := range forStmt.Body.List {
		if wga.statementIncrementsCounterByOne(stmt, counterName) {
			return true
		}
	}
	return false
}

func (wga *Checker) statementIncrementsCounterByOne(stmt ast.Stmt, counterName string) bool {
	switch post := stmt.(type) {
	case *ast.IncDecStmt:
		ident, ok := post.X.(*ast.Ident)
		return ok && ident.Name == counterName && post.Tok == token.INC
	case *ast.AssignStmt:
		if len(post.Lhs) != 1 || len(post.Rhs) != 1 || post.Tok != token.ADD_ASSIGN {
			return false
		}
		ident, ok := post.Lhs[0].(*ast.Ident)
		if !ok || ident.Name != counterName {
			return false
		}
		lit, ok := post.Rhs[0].(*ast.BasicLit)
		return ok && lit.Kind == token.INT && parseIntLiteral(lit) == 1
	default:
		return false
	}
}

func (wga *Checker) collectionLengthBefore(name string, before token.Pos) (int, bool) {
	if wga.function == nil || wga.function.Body == nil || name == "" {
		return 0, false
	}

	lengths := make(map[string]int)
	known := make(map[string]bool)
	wga.collectCollectionLengthsBefore(wga.function.Body.List, before, 1, lengths, known)
	length, ok := lengths[name]
	return length, ok && known[name]
}

func (wga *Checker) collectCollectionLengthsBefore(stmts []ast.Stmt, before token.Pos, multiplier int, lengths map[string]int, known map[string]bool) bool {
	for _, stmt := range stmts {
		if stmt == nil || stmt.Pos() >= before {
			return false
		}
		if wga.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		switch s := stmt.(type) {
		case *ast.DeclStmt:
			wga.recordCollectionDeclLengths(s, lengths, known)
		case *ast.AssignStmt:
			wga.recordCollectionAssignLengths(s, multiplier, lengths, known)
		case *ast.ForStmt:
			iterations, ok := wga.estimateForIterationsKnown(s)
			if !ok || s.Body == nil {
				continue
			}
			if !wga.collectCollectionLengthsBefore(s.Body.List, before, multiplier*iterations, lengths, known) {
				return false
			}
		case *ast.RangeStmt:
			iterations, ok := wga.estimateRangeIterationsKnown(s)
			if !ok || s.Body == nil {
				continue
			}
			if !wga.collectCollectionLengthsBefore(s.Body.List, before, multiplier*iterations, lengths, known) {
				return false
			}
		case *ast.BlockStmt:
			if !wga.collectCollectionLengthsBefore(s.List, before, multiplier, lengths, known) {
				return false
			}
		case *ast.LabeledStmt:
			if !wga.collectCollectionLengthsBefore([]ast.Stmt{s.Stmt}, before, multiplier, lengths, known) {
				return false
			}
		}
	}
	return true
}

func (wga *Checker) recordCollectionDeclLengths(stmt *ast.DeclStmt, lengths map[string]int, known map[string]bool) {
	gen, ok := stmt.Decl.(*ast.GenDecl)
	if !ok {
		return
	}
	for _, spec := range gen.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range vs.Names {
			if i < len(vs.Values) {
				wga.setCollectionLength(name.Name, vs.Values[i], lengths, known)
				continue
			}
			if length, ok := wga.collectionLengthFromType(vs.Type); ok {
				lengths[name.Name] = length
				known[name.Name] = true
			}
		}
	}
}

func (wga *Checker) recordCollectionAssignLengths(stmt *ast.AssignStmt, multiplier int, lengths map[string]int, known map[string]bool) {
	for i, lhs := range stmt.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || i >= len(stmt.Rhs) {
			continue
		}
		if wga.recordAppendLength(ident.Name, stmt.Rhs[i], multiplier, lengths, known) {
			continue
		}
		wga.setCollectionLength(ident.Name, stmt.Rhs[i], lengths, known)
	}
}

func (wga *Checker) setCollectionLength(name string, expr ast.Expr, lengths map[string]int, known map[string]bool) {
	length, ok := wga.collectionLengthFromExpr(expr, lengths, known)
	if !ok {
		delete(lengths, name)
		delete(known, name)
		return
	}
	lengths[name] = length
	known[name] = true
}

func (wga *Checker) recordAppendLength(name string, expr ast.Expr, multiplier int, lengths map[string]int, known map[string]bool) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "append" || len(call.Args) < 2 {
		return false
	}
	target, ok := call.Args[0].(*ast.Ident)
	if !ok || target.Name != name || !known[name] || call.Ellipsis.IsValid() {
		delete(lengths, name)
		delete(known, name)
		return true
	}
	lengths[name] += (len(call.Args) - 1) * multiplier
	return true
}

func (wga *Checker) collectionLengthFromExpr(expr ast.Expr, lengths map[string]int, known map[string]bool) (int, bool) {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return len(e.Elts), true
	case *ast.Ident:
		length, ok := lengths[e.Name]
		return length, ok && known[e.Name]
	case *ast.SliceExpr:
		return wga.sliceExprLength(e, lengths, known)
	case *ast.CallExpr:
		return wga.makeCollectionLength(e)
	default:
		return 0, false
	}
}

func (wga *Checker) sliceExprLength(expr *ast.SliceExpr, lengths map[string]int, known map[string]bool) (int, bool) {
	low := 0
	if expr.Low != nil {
		value, ok := common.ConstantIntValue(expr.Low, wga.typesInfo)
		if !ok {
			return 0, false
		}
		low = value
	}
	if expr.High != nil {
		high, ok := common.ConstantIntValue(expr.High, wga.typesInfo)
		if !ok || high < low {
			return 0, false
		}
		return high - low, true
	}
	if ident, ok := expr.X.(*ast.Ident); ok {
		length, ok := lengths[ident.Name]
		return length - low, ok && known[ident.Name] && length >= low
	}
	return 0, false
}

func (wga *Checker) makeCollectionLength(call *ast.CallExpr) (int, bool) {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "make" || len(call.Args) < 2 {
		return 0, false
	}
	length, ok := common.ConstantIntValue(call.Args[1], wga.typesInfo)
	return length, ok
}

func (wga *Checker) collectionLengthFromType(expr ast.Expr) (int, bool) {
	switch typ := expr.(type) {
	case *ast.ArrayType:
		// `var x []T` starts at length 0 but can be mutated through control-flow
		// branches the walker doesn't descend into; treating it as known-zero
		// would zero out the loop multiplier and drop per-iteration Dones.
		if typ.Len == nil {
			return 0, false
		}
		return common.ConstantIntValue(typ.Len, wga.typesInfo)
	default:
		return 0, false
	}
}

func parseIntLiteral(lit *ast.BasicLit) int {
	if lit == nil {
		return 0
	}
	value := 0
	for _, ch := range lit.Value {
		if ch < '0' || ch > '9' {
			break
		}
		value = value*10 + int(ch-'0')
	}
	return value
}

// reportUnmatchedAdds reports Add calls that don't have corresponding Done calls
func (wga *Checker) reportUnmatchedAdds(wgName string, stats *Stats, totalExpectedDone int) {
	sort.Slice(stats.addCalls, func(i, j int) bool {
		return stats.addCalls[i].pos < stats.addCalls[j].pos
	})

	remainingDone := totalExpectedDone
	for _, addCall := range stats.addCalls {
		if remainingDone >= addCall.value {
			remainingDone -= addCall.value
		} else if !addCall.known && wga.addCoveredByVariableDoneLoop(addCall.pos, wgName) {
			continue
		} else {
			wga.errorCollector.AddError(addCall.pos, category.AddWithoutDone, "waitgroup '"+wgName+"' has Add without corresponding Done")
		}
	}
}

func (wga *Checker) addCoveredByVariableDoneLoop(addPos token.Pos, wgName string) bool {
	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch loop := n.(type) {
		case *ast.ForStmt:
			if nodeContainsPos(loop.Body, addPos) && wga.loopBodyHasVariableDoneWorker(loop.Body, addPos, wgName) {
				found = true
				return false
			}
		case *ast.RangeStmt:
			if nodeContainsPos(loop.Body, addPos) && wga.loopBodyHasVariableDoneWorker(loop.Body, addPos, wgName) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func (wga *Checker) loopBodyHasVariableDoneWorker(body *ast.BlockStmt, after token.Pos, wgName string) bool {
	if body == nil {
		return false
	}
	for _, stmt := range body.List {
		if stmt == nil || stmt.Pos() <= after {
			continue
		}
		goStmt, ok := stmt.(*ast.GoStmt)
		if !ok {
			continue
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			continue
		}
		if wga.blockHasVariableDoneLoop(fnLit.Body, wgName) {
			return true
		}
	}
	return false
}

func (wga *Checker) blockHasVariableDoneLoop(body *ast.BlockStmt, wgName string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch loop := n.(type) {
		case *ast.ForStmt:
			if _, ok := wga.estimateForIterationsKnown(loop); ok {
				return true
			}
			if wga.containsDoneCall(loop.Body, wgName) {
				found = true
				return false
			}
		case *ast.RangeStmt:
			if _, ok := wga.estimateRangeIterationsKnown(loop); ok {
				return true
			}
			if wga.containsDoneCall(loop.Body, wgName) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// reportExcessDones reports Done calls that don't have corresponding Add calls
func (wga *Checker) reportExcessDones(wgName string, stats *Stats, totalExpectedDone int, _ int) {
	if totalExpectedDone <= stats.totalAdd {
		return
	}

	// Only report excess for main flow Done calls (not goroutine Done calls)
	var mainFlowDoneCalls []token.Pos
	for _, donePos := range stats.doneCalls {
		if !wga.isInGoroutine(donePos) && !wga.isInBranchingControlFlow(donePos) {
			mainFlowDoneCalls = append(mainFlowDoneCalls, donePos)
		}
	}

	if len(mainFlowDoneCalls) <= stats.totalAdd {
		return
	}

	slices.Sort(mainFlowDoneCalls)

	excessCount := len(mainFlowDoneCalls) - stats.totalAdd
	startIndex := len(mainFlowDoneCalls) - excessCount

	for i := startIndex; i < len(mainFlowDoneCalls) && i >= 0; i++ {
		wga.errorCollector.AddError(mainFlowDoneCalls[i], category.DoneWithoutAdd, "waitgroup '"+wgName+"' has Done without corresponding Add")
	}
}

// checkAddAfterWait detects Add calls that occur after Wait calls
func (wga *Checker) checkAddAfterWait(stats map[string]*Stats) {
	for wgName, st := range stats {
		wga.checkAddAfterWaitInGoroutines(wgName, st)
		wga.checkAddAfterWaitInMainFlow(wgName, st)
	}
}

func (wga *Checker) checkWaitBeforeDoneSameGoroutine(stats map[string]*Stats) {
	for wgName, st := range stats {
		for _, waitPos := range st.waitCalls {
			if wga.isInGoroutine(waitPos) || wga.hasRelatedGoroutineBeforeWait(wgName, waitPos) {
				continue
			}
			if wga.pendingMainFlowAddsBeforeWait(st, waitPos) > 0 && wga.hasMainFlowReleaseAfterWait(st, waitPos) {
				wga.errorCollector.AddError(waitPos, category.WaitDeadlock, "waitgroup '"+wgName+"' waits with pending Add in the same goroutine")
			}
		}
	}
}

func (wga *Checker) pendingMainFlowAddsBeforeWait(st *Stats, waitPos token.Pos) int {
	pending := 0
	for _, add := range st.addCalls {
		if add.pos < waitPos && wga.isInMainFunctionFlow(add.pos) {
			pending += add.value
		}
	}
	for _, done := range st.doneCalls {
		if done < waitPos && wga.isInMainFunctionFlow(done) {
			pending--
		}
	}
	if pending < 0 {
		return 0
	}
	return pending
}

func (wga *Checker) hasMainFlowReleaseAfterWait(st *Stats, waitPos token.Pos) bool {
	for _, done := range st.doneCalls {
		if done > waitPos && wga.isInMainFunctionFlow(done) {
			return true
		}
	}
	for _, deferDone := range st.deferDoneCalls {
		if deferDone < waitPos && wga.isInMainFunctionFlow(deferDone) {
			return true
		}
	}
	return false
}

func (wga *Checker) isInMainFunctionFlow(pos token.Pos) bool {
	return !wga.isInGoroutine(pos) && !wga.isInNestedFunctionLiteral(pos)
}

func (wga *Checker) isInNestedFunctionLiteral(pos token.Pos) bool {
	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		fnLit, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		if nodeContainsPos(fnLit.Body, pos) {
			found = true
			return false
		}
		return true
	})
	return found
}

func (wga *Checker) hasRelatedGoroutineBeforeWait(wgName string, waitPos token.Pos) bool {
	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || goStmt.Pos() > waitPos {
			return true
		}
		doneInfo, related := wga.goroutineDoneInfo(goStmt, wgName)
		if related && doneInfo.hasAnyDone {
			found = true
			return false
		}
		return true
	})
	return found
}

// checkAddAfterWaitInGoroutines checks for Add after Wait in goroutines
func (wga *Checker) checkAddAfterWaitInGoroutines(wgName string, st *Stats) {
	for _, waitPos := range st.waitCalls {
		ast.Inspect(wga.function.Body, func(n ast.Node) bool {
			if goStmt, ok := n.(*ast.GoStmt); ok {
				if goStmt.Pos() > waitPos {
					wga.checkAddInGoroutine(goStmt, wgName)
				}
			}
			return true
		})
	}
}

// checkAddInGoroutine checks for Add calls within a specific goroutine
func (wga *Checker) checkAddInGoroutine(goStmt *ast.GoStmt, wgName string) {
	if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
		ast.Inspect(fnLit.Body, func(inner ast.Node) bool {
			if call, ok := inner.(*ast.CallExpr); ok {
				if wga.commentFilter.ShouldSkipCall(call) {
					return true
				}

				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if common.GetVarName(sel.X) != wgName {
						return true
					}

					switch sel.Sel.Name {
					case "Add":
						wga.errorCollector.AddError(call.Pos(), category.AddAfterWait, "waitgroup '"+wgName+"' Add called after Wait")
					case "Go":
						wga.errorCollector.AddError(call.Pos(), category.GoAfterWait, "waitgroup '"+wgName+"' Go called after Wait")
					}
				}
			}
			return true
		})
	}
}

// checkAddAfterWaitInMainFlow detects Add calls in the main execution flow that occur after Wait
func (wga *Checker) checkAddAfterWaitInMainFlow(wgName string, st *Stats) {
	if strings.Contains(wgName, ".") {
		return
	}
	for _, wait := range st.waitCalls {
		// Early-exit Waits don't gate later Adds (their branch never returns
		// to the surrounding flow).
		if wga.waitInEarlyExitBranch(wait) {
			continue
		}

		// Check if this Wait has any Add or Done operations before it in main flow
		hasOperationsBefore := false

		// Check for Add calls before this Wait (main flow only)
		for _, add := range st.addCalls {
			if add.pos < wait && !wga.isInGoroutine(add.pos) {
				hasOperationsBefore = true
				break
			}
		}

		// Check for Done calls before this Wait (main flow only)
		if !hasOperationsBefore {
			for _, done := range st.doneCalls {
				if done < wait && !wga.isInGoroutine(done) {
					hasOperationsBefore = true
					break
				}
			}
		}

		// WaitGroup.Go is also a task-starting operation and must be treated like Add
		// for the "empty Wait followed by new work" pattern.
		if !hasOperationsBefore {
			for _, goPos := range st.goCalls {
				if goPos < wait && !wga.isInGoroutine(goPos) {
					hasOperationsBefore = true
					break
				}
			}
		}

		// If this is an "empty" Wait (no operations before it), flag subsequent Add calls
		if !hasOperationsBefore {
			for _, add := range st.addCalls {
				// Only report Add calls in main flow after this empty Wait
				if add.pos > wait && !wga.isInGoroutine(add.pos) {
					if add.value == 1 && wga.hasDeferredDoneAfter(wgName, add.pos) {
						continue
					}
					wga.errorCollector.AddError(add.pos, category.AddAfterWait, "waitgroup '"+wgName+"' Add called after Wait")
				}
			}
			for _, goPos := range st.goCalls {
				if goPos > wait && !wga.isInGoroutine(goPos) {
					wga.errorCollector.AddError(goPos, category.GoAfterWait, "waitgroup '"+wgName+"' Go called after Wait")
				}
			}
		}
	}
}

func (wga *Checker) goroutineOnlyWaitsOnWaitGroup(goStmt *ast.GoStmt, wgName string) bool {
	fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
	if !ok {
		return false
	}

	hasWait := false
	hasTaskLikeUse := false

	ast.Inspect(fnLit.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || common.GetVarName(sel.X) != wgName {
			return true
		}

		switch sel.Sel.Name {
		case "Wait":
			hasWait = true
		case "Add", "Done", "Go":
			hasTaskLikeUse = true
			return false
		}

		return true
	})

	return hasWait && !hasTaskLikeUse
}

func (wga *Checker) hasDeferredDoneAfter(wgName string, after token.Pos) bool {
	found := false

	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}

		deferStmt, ok := n.(*ast.DeferStmt)
		if !ok || deferStmt.Pos() <= after {
			return true
		}
		if wga.isNodeInGoroutine(deferStmt) {
			return true
		}
		if wga.isSimpleDeferDone(deferStmt, wgName) {
			found = true
			return false
		}
		return true
	})

	return found
}

// checkLoopAddDoneBalance checks for Add/Done balance issues in loops
func (wga *Checker) checkLoopAddDoneBalance() {
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if forStmt, ok := n.(*ast.ForStmt); ok {
			wga.analyzeLoopBalance(forStmt)
		}
		return true
	})
}

// loopAnalysis tracks Add/Done calls within a loop
type loopAnalysis struct {
	addCalls           []token.Pos
	unconditionalDones int
	conditionalDones   int
}

// analyzeLoopBalance analyzes Add/Done balance within a single loop
func (wga *Checker) analyzeLoopBalance(forStmt *ast.ForStmt) {
	loopStats := make(map[string]*loopAnalysis)

	ast.Inspect(forStmt.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ExprStmt:
			if call, ok := node.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					wgName := common.GetVarName(sel.X)
					if wga.waitGroupNames[wgName] {
						if loopStats[wgName] == nil {
							loopStats[wgName] = &loopAnalysis{}
						}

						switch sel.Sel.Name {
						case "Add":
							loopStats[wgName].addCalls = append(loopStats[wgName].addCalls, call.Pos())
						case "Done":
							if wga.isInConditional(call, forStmt.Body) {
								loopStats[wgName].conditionalDones++
							} else {
								loopStats[wgName].unconditionalDones++
							}
						}
					}
				}
			}
		}
		return true
	})

	for wgName, stats := range loopStats {
		if len(stats.addCalls) > 0 {
			if stats.unconditionalDones == 0 && stats.conditionalDones > 0 {
				for _, addPos := range stats.addCalls {
					wga.errorCollector.AddError(addPos, category.AddWithoutDone,
						"waitgroup '"+wgName+"' has Add without corresponding Done")
				}
			}
		}
	}
}

// isInConditional checks if a node is inside an if statement
func (wga *Checker) isInConditional(target ast.Node, scope ast.Node) bool {
	inConditional := false

	ast.Inspect(scope, func(n ast.Node) bool {
		if n == target {
			return false
		}

		if ifStmt, ok := n.(*ast.IfStmt); ok {
			ast.Inspect(ifStmt, func(inner ast.Node) bool {
				if inner == target {
					inConditional = true
					return false
				}
				return true
			})
		}

		return !inConditional
	})

	return inConditional
}

func (wga *Checker) isInBranchingControlFlow(pos token.Pos) bool {
	inBranch := false

	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if inBranch || n == nil {
			return false
		}

		switch node := n.(type) {
		case *ast.IfStmt:
			if nodeContainsPos(node.Body, pos) || nodeContainsPos(node.Else, pos) {
				inBranch = true
				return false
			}
		case *ast.SwitchStmt:
			if nodeContainsPos(node.Body, pos) {
				inBranch = true
				return false
			}
		case *ast.TypeSwitchStmt:
			if nodeContainsPos(node.Body, pos) {
				inBranch = true
				return false
			}
		case *ast.SelectStmt:
			if nodeContainsPos(node.Body, pos) {
				inBranch = true
				return false
			}
		}

		return true
	})

	return inBranch
}

func nodeContainsPos(n ast.Node, pos token.Pos) bool {
	if n == nil {
		return false
	}
	return n.Pos() <= pos && pos <= n.End()
}

func (wga *Checker) checkLiteralAddLoopGoroutineMismatch(stats map[string]*Stats) {
	for wgName, st := range stats {
		var positiveAdds []addCall
		for _, add := range st.addCalls {
			if add.value > 0 && !wga.isInGoroutine(add.pos) {
				positiveAdds = append(positiveAdds, add)
			}
		}
		if len(positiveAdds) != 1 {
			continue
		}
		launched := wga.countLoopWorkerGoroutinesAfter(positiveAdds[0].pos, wgName)
		if launched <= 1 || launched == positiveAdds[0].value {
			continue
		}
		wga.errorCollector.AddError(positiveAdds[0].pos, category.AddLoopCountMismatch,
			"waitgroup '"+wgName+"' Add count "+strconv.Itoa(positiveAdds[0].value)+" does not match "+strconv.Itoa(launched)+" goroutines launched")
	}
}

func (wga *Checker) countLoopWorkerGoroutinesAfter(after token.Pos, wgName string) int {
	total := 0
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		switch loop := n.(type) {
		case *ast.ForStmt:
			if loop.Pos() <= after {
				return true
			}
			iterations := wga.estimateForIterations(loop)
			if iterations <= 1 {
				return true
			}
			total += iterations * wga.countWorkerGoroutines(loop.Body, wgName)
		case *ast.RangeStmt:
			if loop.Pos() <= after {
				return true
			}
			iterations := wga.estimateRangeIterationsForMismatch(loop)
			if iterations <= 1 {
				return true
			}
			total += iterations * wga.countWorkerGoroutines(loop.Body, wgName)
		}
		return true
	})
	return total
}

func (wga *Checker) estimateRangeIterationsForMismatch(rangeStmt *ast.RangeStmt) int {
	if rangeStmt == nil || rangeStmt.X == nil {
		return 1
	}
	if lit, ok := rangeStmt.X.(*ast.CompositeLit); ok {
		return len(lit.Elts)
	}
	return wga.estimateRangeIterations(rangeStmt)
}

func (wga *Checker) countWorkerGoroutines(body *ast.BlockStmt, wgName string) int {
	if body == nil {
		return 0
	}
	count := 0
	ast.Inspect(body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		doneInfo, related := wga.goroutineDoneInfo(goStmt, wgName)
		if related && doneInfo.hasAnyDone {
			count++
		}
		return true
	})
	return count
}

func (wga *Checker) checkWaitWithoutAdd(stats map[string]*Stats) {
	for wgName, st := range stats {
		if !wga.localWaitGroupNames[wgName] || strings.Contains(wgName, ".") ||
			len(st.addCalls) > 0 || len(st.goCalls) > 0 || wga.waitGroupInitializedFromAnother(wgName) {
			continue
		}
		for _, waitPos := range st.waitCalls {
			targetObj := wga.waitGroupReceiverObjectAt(wgName, "Wait", waitPos)
			// The Add may live in a helper function the WaitGroup is passed to,
			// or in a closure assigned to a local variable that is invoked later.
			// Both checks must consider only references that appear before the
			// Wait, since later code cannot supply the missing Add.
			if targetObj != nil &&
				(wga.isWaitGroupPassedToOtherFunctionsForWait(targetObj, waitPos) ||
					wga.hasAddInLocalClosure(targetObj, waitPos)) {
				continue
			}
			wga.errorCollector.AddError(waitPos, category.WaitWithoutAdd, "waitgroup '"+wgName+"' Wait called without any Add")
		}
	}
}

// hasAddInLocalClosure reports whether a WaitGroup has Add called inside a
// function literal assigned to a local variable. This is intentionally
// permissive: it does not prove the closure is invoked. Only closures whose
// definition appears before waitPos are considered, since a closure defined
// later cannot have run before the Wait.
func (wga *Checker) hasAddInLocalClosure(target types.Object, waitPos token.Pos) bool {
	if wga.function == nil || wga.function.Body == nil || target == nil {
		return false
	}

	found := false
	funcLitDepth := 0
	astutil.Apply(wga.function.Body, func(c *astutil.Cursor) bool {
		if found {
			return false
		}
		node := c.Node()
		if node == nil {
			return true
		}
		if fnLit, ok := node.(*ast.FuncLit); ok {
			if fnLit.Pos() >= waitPos {
				return false
			}
			funcLitDepth++
			return true
		}
		call, ok := node.(*ast.CallExpr)
		if !ok || funcLitDepth == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Add" {
			return true
		}
		if wga.exprReferencesObject(sel.X, target) {
			found = true
			return false
		}
		return true
	}, func(c *astutil.Cursor) bool {
		if _, ok := c.Node().(*ast.FuncLit); ok {
			funcLitDepth--
		}
		return true
	})
	return found
}

// isWaitGroupPassedToOtherFunctionsForWait reports whether the WaitGroup
// referred to by target is referenced (passed, assigned, returned, etc.)
// somewhere in the enclosing function before waitPos. References after the
// Wait cannot supply its missing Add.
func (wga *Checker) isWaitGroupPassedToOtherFunctionsForWait(target types.Object, waitPos token.Pos) bool {
	if wga.function == nil || wga.function.Body == nil || target == nil {
		return false
	}

	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found || n == nil || n.Pos() >= waitPos {
			return false
		}
		switch node := n.(type) {
		case *ast.CallExpr:
			for _, arg := range node.Args {
				if wga.exprReferencesObject(arg, target) {
					found = true
					return false
				}
			}
		case *ast.AssignStmt:
			for _, rhs := range node.Rhs {
				if wga.exprReferencesObject(rhs, target) {
					found = true
					return false
				}
			}
		case *ast.ValueSpec:
			for _, value := range node.Values {
				if wga.exprReferencesObject(value, target) {
					found = true
					return false
				}
			}
		case *ast.ReturnStmt:
			for _, result := range node.Results {
				if wga.exprReferencesObject(result, target) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func (wga *Checker) waitGroupReceiverObjectAt(wgName, method string, pos token.Pos) types.Object {
	if wga.function == nil || wga.function.Body == nil {
		return nil
	}
	var obj types.Object
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if obj != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Pos() != pos {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != method || common.GetVarName(sel.X) != wgName {
			return true
		}
		obj = wga.receiverObject(sel.X)
		return false
	})
	return obj
}

func (wga *Checker) exprReferencesObject(expr ast.Expr, target types.Object) bool {
	if expr == nil || target == nil {
		return false
	}
	if obj := wga.receiverObject(expr); obj != nil {
		return obj == target
	}

	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		exprNode, ok := n.(ast.Expr)
		if !ok {
			return true
		}
		if obj := wga.receiverObject(exprNode); obj != nil && obj == target {
			found = true
			return false
		}
		return true
	})
	return found
}

func (wga *Checker) receiverObject(expr ast.Expr) types.Object {
	switch e := expr.(type) {
	case *ast.Ident:
		if wga.typesInfo == nil {
			return nil
		}
		return wga.typesInfo.ObjectOf(e)
	case *ast.ParenExpr:
		return wga.receiverObject(e.X)
	case *ast.UnaryExpr:
		if e.Op == token.AND || e.Op == token.MUL {
			return wga.receiverObject(e.X)
		}
	}
	return nil
}

func (wga *Checker) waitGroupInitializedFromAnother(wgName string) bool {
	found := false
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name != wgName || i >= len(node.Rhs) {
					continue
				}
				if wga.isWaitGroupAliasedOrCopiedExpr(node.Rhs[i]) {
					found = true
					return false
				}
			}
		case *ast.ValueSpec:
			for i, name := range node.Names {
				if name.Name != wgName || i >= len(node.Values) {
					continue
				}
				if wga.isWaitGroupAliasedOrCopiedExpr(node.Values[i]) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// isWaitGroupAliasedOrCopiedExpr reports whether expr initializes a local
// WaitGroup handle from another WaitGroup, either by value or by address.
func (wga *Checker) isWaitGroupAliasedOrCopiedExpr(expr ast.Expr) bool {
	if expr == nil {
		return false
	}
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return false
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return wga.isWaitGroupFieldExpr(e.X)
		}
		return wga.isWaitGroupAliasedOrCopiedExpr(e.X)
	}
	return common.IsWaitGroup(wga.typesInfo.TypeOf(expr))
}

func (wga *Checker) isWaitGroupFieldExpr(expr ast.Expr) bool {
	_, ok := expr.(*ast.SelectorExpr)
	return ok && common.IsWaitGroup(wga.typesInfo.TypeOf(expr))
}

// checkUnreachableDone checks for Done calls that are unreachable due to early returns
func (wga *Checker) checkUnreachableDone() {
	for wgName := range wga.waitGroupNames {
		ast.Inspect(wga.function.Body, func(n ast.Node) bool {
			goStmt, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}

			if fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit); ok {
				if wga.hasUnreachableDone(fnLit.Body, wgName) {
					addPos := wga.findRelatedAddCall(goStmt, wgName)
					if addPos != token.NoPos {
						wga.errorCollector.AddError(addPos, category.AddWithoutDone,
							"waitgroup '"+wgName+"' has Add without corresponding Done")
					}
				}
			}

			return true
		})
	}
}
