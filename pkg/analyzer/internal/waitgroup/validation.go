package waitgroup

import (
	"go/ast"
	"go/constant"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// validateUsage performs validation checks on collected statistics
func (wga *Checker) validateUsage(stats map[string]*Stats) {
	balance := newBalanceValidator(balanceValidatorConfig{
		function:                     wga.function,
		waitGroupNames:               wga.waitGroupNames,
		localWaitGroupNames:          wga.localWaitGroupNames,
		commentFilter:                wga.commentFilter,
		reporter:                     wga.errorCollector,
		typesInfo:                    wga.typesInfo,
		escape:                       wga.escape,
		isInGoroutine:                wga.isInGoroutine,
		isNodeInGoroutine:            wga.isNodeInGoroutine,
		callInvokesDone:              wga.callInvokesDone,
		goroutineDoneInfo:            wga.goroutineDoneInfo,
		isSimpleDeferDone:            wga.isSimpleDeferDone,
		findRelatedAddCall:           wga.findRelatedAddCall,
		hasUnreachableDone:           wga.hasUnreachableDone,
		waitInEarlyExitBranch:        wga.waitInEarlyExitBranch,
		estimateForIterations:        wga.estimateForIterations,
		estimateForIterationsKnown:   wga.estimateForIterationsKnown,
		estimateRangeIterations:      wga.estimateRangeIterations,
		estimateRangeIterationsKnown: wga.estimateRangeIterationsKnown,
	})
	goroutines := newGoroutineInspector(
		wga.waitGroupNames,
		wga.commentFilter,
		wga.errorCollector,
		wga.deferInvokesDone,
		wga.callInvokesDone,
		wga.goroutineDoneInfo,
		balance.goroutineOnlyWaitsOnWaitGroup,
		wga.analyzeDoneCallsWithVisited,
		wga.isInGoroutine,
		wga.typesInfo,
		balance.isInMainFunctionFlow,
		wga.isBuiltinPanic,
	)
	goroutines.checkAddInsideGoroutine(wga.function)
	wga.checkDoneNotDeferredInWorker()
	balance.checkLiteralAddLoopGoroutineMismatch(stats)
	balance.checkWaitWithoutAdd(stats)
	goroutines.checkMultipleDoneSameWorkerBranch(wga.function)
	goroutines.checkNestedWaitGroupDeadlock(wga.function)
	balance.checkAddAfterWait(stats)
	balance.checkWaitBeforeDoneSameGoroutine(stats)
	goroutines.checkWaitAndDoneInSameGoroutine(wga.function)
	goroutines.checkDoneOutsideWorkerGoroutine(wga.function)
	goroutines.checkWaitGroupGoPanic(wga.function)
	balance.checkLoopAddDoneBalance()
	balance.checkUnreachableDone()
	balance.checkWaitGroupBalance(stats)
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
