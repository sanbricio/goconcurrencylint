package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/category"
)

func (ma *Analyzer) reportPotentialPanicWhileLocked(stmt ast.Stmt, stats map[string]*Stats) {
	if ma.rawBodyEffects || stmt == nil || !ma.hasUnprotectedHeldLock(stats) {
		return
	}

	var panicPos token.Pos
	ast.Inspect(stmt, func(n ast.Node) bool {
		if panicPos != token.NoPos {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		index, ok := n.(*ast.IndexExpr)
		if !ok {
			return true
		}
		if ma.indexExprCanPanic(index) {
			panicPos = index.Pos()
			return false
		}
		return true
	})
	if panicPos == token.NoPos {
		return
	}

	for name, st := range stats {
		if st == nil {
			continue
		}
		if ma.mutexNames[name] && ma.remainingLockCount(st.lock, st.deferUnlock) > 0 {
			ma.errorCollector.AddError(panicPos, category.PanicBeforeUnlock, "mutex '"+name+"' may remain locked if index expression panics before unlock")
		}
		if ma.rwMutexNames[name] {
			if ma.remainingLockCount(st.lock, st.deferUnlock) > 0 {
				ma.errorCollector.AddError(panicPos, category.PanicBeforeUnlock, "rwmutex '"+name+"' may remain locked if index expression panics before unlock")
			}
			if ma.remainingLockCount(st.rlock, st.deferRUnlock) > 0 {
				ma.errorCollector.AddError(panicPos, category.PanicBeforeUnlock, "rwmutex '"+name+"' may remain rlocked if index expression panics before runlock")
			}
		}
	}
}

func (ma *Analyzer) hasUnprotectedHeldLock(stats map[string]*Stats) bool {
	for name, st := range stats {
		if st == nil {
			continue
		}
		if ma.mutexNames[name] && ma.remainingLockCount(st.lock, st.deferUnlock) > 0 {
			return true
		}
		if ma.rwMutexNames[name] &&
			(ma.remainingLockCount(st.lock, st.deferUnlock) > 0 ||
				ma.remainingLockCount(st.rlock, st.deferRUnlock) > 0) {
			return true
		}
	}
	return false
}

func (ma *Analyzer) indexExprCanPanic(index *ast.IndexExpr) bool {
	if index == nil {
		return false
	}
	// Map indexing never panics: missing keys return the zero value, and
	// negative or out-of-range keys are valid for any comparable key type.
	if ma.isMapIndex(index) {
		return false
	}
	indexValue, ok := common.ConstantIntValue(index.Index, ma.typesInfo)
	if !ok {
		return false
	}
	if indexValue < 0 {
		return true
	}
	length, ok := ma.staticLength(index.X)
	return ok && indexValue >= length
}

func (ma *Analyzer) isMapIndex(index *ast.IndexExpr) bool {
	if index == nil || ma.typesInfo == nil {
		return false
	}
	typ := ma.typesInfo.TypeOf(index.X)
	if typ == nil {
		return false
	}
	_, ok := typ.Underlying().(*types.Map)
	return ok
}

func (ma *Analyzer) staticLength(expr ast.Expr) (int, bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		length, ok := ma.collectionLengths[e.Name]
		return length, ok
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return 0, false
		}
		value, err := strconv.Unquote(e.Value)
		if err != nil {
			return 0, false
		}
		return len(value), true
	case *ast.CompositeLit:
		return len(e.Elts), true
	case *ast.CallExpr:
		return ma.staticMakeLength(e)
	}

	typ := ma.typesInfo.TypeOf(expr)
	if array, ok := types.Unalias(typ).(*types.Array); ok {
		return int(array.Len()), true
	}
	return 0, false
}

func (ma *Analyzer) staticMakeLength(call *ast.CallExpr) (int, bool) {
	if call == nil || len(call.Args) < 2 {
		return 0, false
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "make" {
		return 0, false
	}
	return common.ConstantIntValue(call.Args[1], ma.typesInfo)
}

func (ma *Analyzer) recordCollectionLengthsFromDecl(stmt *ast.DeclStmt) {
	if stmt == nil {
		return
	}
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
			if i >= len(vs.Values) {
				delete(ma.collectionLengths, name.Name)
				continue
			}
			ma.recordCollectionLength(name.Name, vs.Values[i])
		}
	}
}

func (ma *Analyzer) recordCollectionLengthsFromAssign(stmt *ast.AssignStmt) {
	if stmt == nil || (stmt.Tok != token.ASSIGN && stmt.Tok != token.DEFINE) {
		return
	}
	for i, lhs := range stmt.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || ident.Name == "_" {
			continue
		}
		if i >= len(stmt.Rhs) {
			delete(ma.collectionLengths, ident.Name)
			continue
		}
		ma.recordCollectionLength(ident.Name, stmt.Rhs[i])
	}
}

func (ma *Analyzer) recordCollectionLength(name string, expr ast.Expr) {
	if ma.collectionLengths == nil {
		ma.collectionLengths = make(map[string]int)
	}
	if length, ok := ma.staticLength(expr); ok {
		ma.collectionLengths[name] = length
		return
	}
	delete(ma.collectionLengths, name)
}
