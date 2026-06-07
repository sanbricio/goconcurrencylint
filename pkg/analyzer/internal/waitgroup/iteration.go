package waitgroup

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
)

// iterationEstimator keeps the loop and collection length heuristics used by
// WaitGroup balance checks and len(...) Add values.
type iterationEstimator struct {
	function      *ast.FuncDecl
	typesInfo     *types.Info
	commentFilter *commentfilter.CommentFilter
}

func newIterationEstimator(fn *ast.FuncDecl, typesInfo *types.Info, cf *commentfilter.CommentFilter) *iterationEstimator {
	return &iterationEstimator{
		function:      fn,
		typesInfo:     typesInfo,
		commentFilter: cf,
	}
}

func (e *iterationEstimator) estimateForIterations(forStmt *ast.ForStmt) int {
	if iterations, ok := e.estimateForIterationsKnown(forStmt); ok {
		return iterations
	}
	return 1
}

func (e *iterationEstimator) estimateForIterationsKnown(forStmt *ast.ForStmt) (int, bool) {
	if forStmt == nil {
		return 0, false
	}

	start := 0
	counterName := ""

	if init, ok := forStmt.Init.(*ast.AssignStmt); ok && len(init.Lhs) == 1 && len(init.Rhs) == 1 {
		if ident, ok := init.Lhs[0].(*ast.Ident); ok {
			if value, ok := common.ConstantIntValue(init.Rhs[0], e.typesInfo); ok {
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
	limit, ok := common.ConstantIntValue(cond.Y, e.typesInfo)
	if !ok {
		return 0, false
	}

	if !e.loopIncrementsCounterByOne(forStmt, counterName) {
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

func (e *iterationEstimator) estimateRangeIterations(rangeStmt *ast.RangeStmt) int {
	if iterations, ok := e.estimateRangeIterationsKnown(rangeStmt); ok {
		return iterations
	}
	return 1
}

func (e *iterationEstimator) estimateRangeIterationsKnown(rangeStmt *ast.RangeStmt) (int, bool) {
	if rangeStmt == nil || rangeStmt.X == nil {
		return 0, false
	}

	if lit, ok := rangeStmt.X.(*ast.CompositeLit); ok {
		return len(lit.Elts), true
	}
	if ident, ok := rangeStmt.X.(*ast.Ident); ok {
		if length, ok := e.collectionLengthBefore(ident.Name, rangeStmt.Pos()); ok {
			return length, true
		}
	}

	if e.typesInfo != nil {
		if tv, ok := e.typesInfo.Types[rangeStmt.X]; ok && tv.Value != nil {
			if value, ok := constant.Int64Val(tv.Value); ok && value > 0 {
				return int(value), true
			}
		}
	}

	return 0, false
}

func (e *iterationEstimator) loopIncrementsCounterByOne(forStmt *ast.ForStmt, counterName string) bool {
	if forStmt == nil || counterName == "" {
		return false
	}
	if forStmt.Post != nil {
		return e.statementIncrementsCounterByOne(forStmt.Post, counterName)
	}
	for _, stmt := range forStmt.Body.List {
		if e.statementIncrementsCounterByOne(stmt, counterName) {
			return true
		}
	}
	return false
}

func (e *iterationEstimator) statementIncrementsCounterByOne(stmt ast.Stmt, counterName string) bool {
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

func (e *iterationEstimator) collectionLengthBefore(name string, before token.Pos) (int, bool) {
	if e.function == nil || e.function.Body == nil || name == "" {
		return 0, false
	}

	lengths := make(map[string]int)
	known := make(map[string]bool)
	e.collectCollectionLengthsBefore(e.function.Body.List, before, 1, lengths, known)
	length, ok := lengths[name]
	return length, ok && known[name]
}

func (e *iterationEstimator) collectCollectionLengthsBefore(stmts []ast.Stmt, before token.Pos, multiplier int, lengths map[string]int, known map[string]bool) bool {
	for _, stmt := range stmts {
		if stmt == nil || stmt.Pos() >= before {
			return false
		}
		if e.commentFilter != nil && e.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		switch s := stmt.(type) {
		case *ast.DeclStmt:
			e.recordCollectionDeclLengths(s, lengths, known)
		case *ast.AssignStmt:
			e.recordCollectionAssignLengths(s, multiplier, lengths, known)
		case *ast.ForStmt:
			iterations, ok := e.estimateForIterationsKnown(s)
			if !ok || s.Body == nil {
				if s.Body != nil {
					e.invalidateIndexedCollectionLengthsInStatements(s.Body.List, lengths, known)
				}
				continue
			}
			if !e.collectCollectionLengthsBefore(s.Body.List, before, multiplier*iterations, lengths, known) {
				return false
			}
		case *ast.RangeStmt:
			iterations, ok := e.estimateRangeIterationsKnown(s)
			if !ok || s.Body == nil {
				if s.Body != nil {
					e.invalidateIndexedCollectionLengthsInStatements(s.Body.List, lengths, known)
				}
				continue
			}
			if !e.collectCollectionLengthsBefore(s.Body.List, before, multiplier*iterations, lengths, known) {
				return false
			}
		case *ast.BlockStmt:
			if !e.collectCollectionLengthsBefore(s.List, before, multiplier, lengths, known) {
				return false
			}
		case *ast.LabeledStmt:
			if !e.collectCollectionLengthsBefore([]ast.Stmt{s.Stmt}, before, multiplier, lengths, known) {
				return false
			}
		}
	}
	return true
}

func (e *iterationEstimator) recordCollectionDeclLengths(stmt *ast.DeclStmt, lengths map[string]int, known map[string]bool) {
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
				e.setCollectionLength(name.Name, vs.Values[i], lengths, known)
				continue
			}
			if length, ok := e.collectionLengthFromType(vs.Type); ok {
				lengths[name.Name] = length
				known[name.Name] = true
			}
		}
	}
}

func (e *iterationEstimator) invalidateIndexedCollectionLengthsInStatements(stmts []ast.Stmt, lengths map[string]int, known map[string]bool) {
	for _, stmt := range stmts {
		ast.Inspect(stmt, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range assign.Lhs {
				e.invalidateIndexedCollectionLength(lhs, lengths, known)
			}
			return true
		})
	}
}

func (e *iterationEstimator) recordCollectionAssignLengths(stmt *ast.AssignStmt, multiplier int, lengths map[string]int, known map[string]bool) {
	for i, lhs := range stmt.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok {
			e.invalidateIndexedCollectionLength(lhs, lengths, known)
			continue
		}
		if i >= len(stmt.Rhs) {
			continue
		}
		if e.recordAppendLength(ident.Name, stmt.Rhs[i], multiplier, lengths, known) {
			continue
		}
		e.setCollectionLength(ident.Name, stmt.Rhs[i], lengths, known)
	}
}

func (e *iterationEstimator) invalidateIndexedCollectionLength(lhs ast.Expr, lengths map[string]int, known map[string]bool) {
	var target ast.Expr
	switch expr := lhs.(type) {
	case *ast.IndexExpr:
		target = expr.X
	case *ast.IndexListExpr:
		target = expr.X
	default:
		return
	}

	ident, ok := target.(*ast.Ident)
	if !ok {
		return
	}
	delete(lengths, ident.Name)
	delete(known, ident.Name)
}

func (e *iterationEstimator) setCollectionLength(name string, expr ast.Expr, lengths map[string]int, known map[string]bool) {
	length, ok := e.collectionLengthFromExpr(expr, lengths, known)
	if !ok {
		delete(lengths, name)
		delete(known, name)
		return
	}
	lengths[name] = length
	known[name] = true
}

func (e *iterationEstimator) recordAppendLength(name string, expr ast.Expr, multiplier int, lengths map[string]int, known map[string]bool) bool {
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

func (e *iterationEstimator) collectionLengthFromExpr(expr ast.Expr, lengths map[string]int, known map[string]bool) (int, bool) {
	switch value := expr.(type) {
	case *ast.CompositeLit:
		return len(value.Elts), true
	case *ast.Ident:
		length, ok := lengths[value.Name]
		return length, ok && known[value.Name]
	case *ast.SliceExpr:
		return e.sliceExprLength(value, lengths, known)
	case *ast.CallExpr:
		return e.makeCollectionLength(value)
	default:
		return 0, false
	}
}

func (e *iterationEstimator) sliceExprLength(expr *ast.SliceExpr, lengths map[string]int, known map[string]bool) (int, bool) {
	low := 0
	if expr.Low != nil {
		value, ok := common.ConstantIntValue(expr.Low, e.typesInfo)
		if !ok {
			return 0, false
		}
		low = value
	}
	if expr.High != nil {
		high, ok := common.ConstantIntValue(expr.High, e.typesInfo)
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

func (e *iterationEstimator) makeCollectionLength(call *ast.CallExpr) (int, bool) {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "make" || len(call.Args) < 2 {
		return 0, false
	}
	length, ok := common.ConstantIntValue(call.Args[1], e.typesInfo)
	return length, ok
}

func (e *iterationEstimator) collectionLengthFromType(expr ast.Expr) (int, bool) {
	switch typ := expr.(type) {
	case *ast.ArrayType:
		// `var x []T` starts at length 0 but can be mutated through control-flow
		// branches the walker doesn't descend into; treating it as known-zero
		// would zero out the loop multiplier and drop per-iteration Dones.
		if typ.Len == nil {
			return 0, false
		}
		return common.ConstantIntValue(typ.Len, e.typesInfo)
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
