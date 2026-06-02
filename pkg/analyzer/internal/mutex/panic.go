package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// lockedPanicDetector reports statements that may panic (out-of-range index
// access) while a mutex is still held, tracking statically-known collection
// lengths to decide whether an index expression can actually panic. It owns
// the per-function collectionLengths state.
type lockedPanicDetector struct {
	mutexNames     map[string]bool
	rwMutexNames   map[string]bool
	typesInfo      *types.Info
	reporter       report.Reporter
	rawBodyEffects bool

	collectionLengths map[string]int
}

func newLockedPanicDetector(mutexNames, rwMutexNames map[string]bool, typesInfo *types.Info, reporter report.Reporter, rawBodyEffects bool) *lockedPanicDetector {
	return &lockedPanicDetector{
		mutexNames:        mutexNames,
		rwMutexNames:      rwMutexNames,
		typesInfo:         typesInfo,
		reporter:          reporter,
		rawBodyEffects:    rawBodyEffects,
		collectionLengths: make(map[string]int),
	}
}

func (d *lockedPanicDetector) reportPotentialPanicWhileLocked(stmt ast.Stmt, stats map[string]*Stats) {
	if d.rawBodyEffects || stmt == nil || !d.hasUnprotectedHeldLock(stats) {
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
		if d.indexExprCanPanic(index) {
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
		if d.mutexNames[name] && remainingLockCount(st.lock, st.deferUnlock) > 0 {
			d.reporter.AddError(panicPos, category.PanicBeforeUnlock, "mutex '"+name+"' may remain locked if index expression panics before unlock")
		}
		if d.rwMutexNames[name] {
			if remainingLockCount(st.lock, st.deferUnlock) > 0 {
				d.reporter.AddError(panicPos, category.PanicBeforeUnlock, "rwmutex '"+name+"' may remain locked if index expression panics before unlock")
			}
			if remainingLockCount(st.rlock, st.deferRUnlock) > 0 {
				d.reporter.AddError(panicPos, category.PanicBeforeUnlock, "rwmutex '"+name+"' may remain rlocked if index expression panics before runlock")
			}
		}
	}
}

func (d *lockedPanicDetector) hasUnprotectedHeldLock(stats map[string]*Stats) bool {
	for name, st := range stats {
		if st == nil {
			continue
		}
		if d.mutexNames[name] && remainingLockCount(st.lock, st.deferUnlock) > 0 {
			return true
		}
		if d.rwMutexNames[name] &&
			(remainingLockCount(st.lock, st.deferUnlock) > 0 ||
				remainingLockCount(st.rlock, st.deferRUnlock) > 0) {
			return true
		}
	}
	return false
}

func (d *lockedPanicDetector) indexExprCanPanic(index *ast.IndexExpr) bool {
	if index == nil {
		return false
	}
	// Map indexing never panics: missing keys return the zero value, and
	// negative or out-of-range keys are valid for any comparable key type.
	if d.isMapIndex(index) {
		return false
	}
	indexValue, ok := common.ConstantIntValue(index.Index, d.typesInfo)
	if !ok {
		return false
	}
	if indexValue < 0 {
		return true
	}
	length, ok := d.staticLength(index.X)
	return ok && indexValue >= length
}

func (d *lockedPanicDetector) isMapIndex(index *ast.IndexExpr) bool {
	if index == nil || d.typesInfo == nil {
		return false
	}
	typ := d.typesInfo.TypeOf(index.X)
	if typ == nil {
		return false
	}
	_, ok := typ.Underlying().(*types.Map)
	return ok
}

func (d *lockedPanicDetector) staticLength(expr ast.Expr) (int, bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		length, ok := d.collectionLengths[e.Name]
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
		return d.staticMakeLength(e)
	}

	typ := d.typesInfo.TypeOf(expr)
	if array, ok := types.Unalias(typ).(*types.Array); ok {
		return int(array.Len()), true
	}
	return 0, false
}

func (d *lockedPanicDetector) staticMakeLength(call *ast.CallExpr) (int, bool) {
	if call == nil || len(call.Args) < 2 {
		return 0, false
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "make" {
		return 0, false
	}
	return common.ConstantIntValue(call.Args[1], d.typesInfo)
}

func (d *lockedPanicDetector) recordCollectionLengthsFromDecl(stmt *ast.DeclStmt) {
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
				delete(d.collectionLengths, name.Name)
				continue
			}
			d.recordCollectionLength(name.Name, vs.Values[i])
		}
	}
}

func (d *lockedPanicDetector) recordCollectionLengthsFromAssign(stmt *ast.AssignStmt) {
	if stmt == nil || (stmt.Tok != token.ASSIGN && stmt.Tok != token.DEFINE) {
		return
	}
	for i, lhs := range stmt.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || ident.Name == "_" {
			continue
		}
		if i >= len(stmt.Rhs) {
			delete(d.collectionLengths, ident.Name)
			continue
		}
		d.recordCollectionLength(ident.Name, stmt.Rhs[i])
	}
}

func (d *lockedPanicDetector) recordCollectionLength(name string, expr ast.Expr) {
	if d.collectionLengths == nil {
		d.collectionLengths = make(map[string]int)
	}
	if length, ok := d.staticLength(expr); ok {
		d.collectionLengths[name] = length
		return
	}
	delete(d.collectionLengths, name)
}
