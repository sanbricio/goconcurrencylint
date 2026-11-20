package mutex

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// loopMutexDetector reports sync.Mutex / sync.RWMutex variables that are
// declared directly inside a loop body. Each iteration creates a fresh mutex
// that is invisible to other iterations and therefore cannot protect shared
// state. It depends only on a reporter and type information, so it can be
// constructed and exercised without a full Checker.
type loopMutexDetector struct {
	reporter  report.Reporter
	typesInfo *types.Info
}

func newLoopMutexDetector(reporter report.Reporter, typesInfo *types.Info) *loopMutexDetector {
	return &loopMutexDetector{reporter: reporter, typesInfo: typesInfo}
}

// check examines the top-level statements of a loop's body. loop is the whole
// for/range statement — its position range bounds what counts as
// iteration-local (range and init variables included). Nested loops are
// handled when they themselves are analysed as for/range statements. Function
// literals inside the loop are skipped to avoid false positives for patterns
// like `for { go func() { var mu sync.Mutex; … }() }`.
func (d *loopMutexDetector) check(loop ast.Stmt, loopBody *ast.BlockStmt) {
	if loopBody == nil || d.typesInfo == nil {
		return
	}
	for _, stmt := range loopBody.List {
		switch s := stmt.(type) {
		case *ast.DeclStmt:
			gen, ok := s.Decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				d.reportValueSpec(vs, loop, loopBody)
			}
		case *ast.AssignStmt:
			if s.Tok == token.DEFINE {
				d.reportAssign(s, loop, loopBody)
			}
		}
	}
}

// mutexRef identifies the loop-declared mutex: by its object when type
// information resolved the declaration, by name otherwise. Matching by object
// keeps a field of the same name (`x.mu`) or a shadowing variable apart.
type mutexRef struct {
	name string
	obj  types.Object
	info *types.Info
}

func (m mutexRef) is(ident *ast.Ident) bool {
	if ident == nil || ident.Name != m.name {
		return false
	}
	if m.obj == nil || m.info == nil {
		return true
	}
	return m.info.ObjectOf(ident) == m.obj
}

func (d *loopMutexDetector) reportValueSpec(vs *ast.ValueSpec, loop ast.Stmt, loopBody *ast.BlockStmt) {
	for _, name := range vs.Names {
		obj := d.typesInfo.Defs[name]
		if obj == nil {
			continue
		}
		d.reportMutexDecl(obj.Type(), d.refTo(name, obj), name.Pos(), loop, loopBody)
	}
}

func (d *loopMutexDetector) reportAssign(s *ast.AssignStmt, loop ast.Stmt, loopBody *ast.BlockStmt) {
	for i, lhs := range s.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || i >= len(s.Rhs) {
			continue
		}
		obj := d.typesInfo.Defs[ident]
		if obj == nil {
			continue
		}
		typ := d.typesInfo.TypeOf(s.Rhs[i])
		if typ == nil {
			continue
		}
		// A pointer names a fresh mutex only when the right side builds one;
		// &arr[i], &s.mu or a call's result point at a mutex that outlives the
		// iteration.
		if _, isPtr := typ.Underlying().(*types.Pointer); isPtr && !buildsNewValue(s.Rhs[i], d.typesInfo) {
			continue
		}
		d.reportMutexDecl(common.DerefOnceAndUnalias(typ), d.refTo(ident, obj), ident.Pos(), loop, loopBody)
	}
}

func (d *loopMutexDetector) refTo(ident *ast.Ident, obj types.Object) mutexRef {
	return mutexRef{name: ident.Name, obj: obj, info: d.typesInfo}
}

// buildsNewValue reports whether expr allocates a new value: &T{...} or
// new(T).
func buildsNewValue(expr ast.Expr, info *types.Info) bool {
	switch e := ast.Unparen(expr).(type) {
	case *ast.UnaryExpr:
		_, ok := ast.Unparen(e.X).(*ast.CompositeLit)
		return e.Op == token.AND && ok
	case *ast.CallExpr:
		ident, ok := ast.Unparen(e.Fun).(*ast.Ident)
		if !ok || len(e.Args) != 1 {
			return false
		}
		builtin, ok := info.Uses[ident].(*types.Builtin)
		return ok && builtin.Name() == "new"
	}
	return false
}

// reportMutexDecl flags a mutex/rwmutex declared inside a loop, unless the mutex
// is shared with per-iteration goroutines that are joined before the iteration
// ends, in which case a fresh mutex per iteration is intentional.
func (d *loopMutexDetector) reportMutexDecl(typ types.Type, mutex mutexRef, pos token.Pos, loop ast.Stmt, loopBody *ast.BlockStmt) {
	// A fresh lock per iteration is the bug this check describes. A domain
	// object that happens to embed one is not a lock, and building one per
	// iteration is how table-driven tests and per-connection state are written.
	kind := common.ClassifyMutexValue(typ)
	lockOnly := common.IsLockOnlyValue(typ)
	isMutex := lockOnly && kind == common.MutexValue
	isRWMutex := lockOnly && kind == common.RWMutexValue
	if !isMutex && !isRWMutex {
		return
	}

	if mutexProtectsJoinedWorkers(loopBody, mutex) {
		return
	}

	if d.mutexGuardsOnlyIterationLocalState(loop, loopBody, mutex) {
		return
	}

	mutexType := "mutex"
	if isRWMutex {
		mutexType = "rwmutex"
	}
	d.reporter.AddError(pos, category.MutexInLoop,
		mutexType+" '"+mutex.name+"' declared inside loop, each iteration creates a new mutex that cannot protect shared state")
}

var loopMutexCallMethods = []string{"Lock", "Unlock", "RLock", "RUnlock", "TryLock", "TryRLock"}

// mutexGuardsOnlyIterationLocalState reports whether every critical section of
// the loop-declared mutex writes only variables declared inside the loop
// statement, and references at least one. A fresh mutex per iteration is only a
// bug when it pretends to guard state shared ACROSS iterations; reads of outer
// variables do not make it shared, since races need a writer. An empty critical
// section, an escaping mutex, or a write to an outer variable keeps the report.
func (d *loopMutexDetector) mutexGuardsOnlyIterationLocalState(loop ast.Stmt, loopBody *ast.BlockStmt, mutex mutexRef) bool {
	if d.usesMutexBeyondDirectCalls(loopBody, mutex) {
		return false
	}

	sawState := false
	for _, region := range d.criticalRegions(loopBody, mutex) {
		localWrites, refs := d.regionWritesOnlyIterationLocals(region, mutex, loop)
		if !localWrites {
			return false
		}
		sawState = sawState || refs > 0
	}
	return sawState
}

// usesMutexBeyondDirectCalls reports whether the mutex is referenced in any way
// other than `name.<mutex method>()` — e.g. &name escaping, assignment, or an
// argument position. Such uses put the mutex outside what the region analysis
// can see.
func (d *loopMutexDetector) usesMutexBeyondDirectCalls(loopBody *ast.BlockStmt, mutex mutexRef) bool {
	allowed := make(map[*ast.Ident]bool)
	ast.Inspect(loopBody, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !containsMethod(loopMutexCallMethods, sel.Sel.Name) {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && mutex.is(ident) {
			allowed[ident] = true
		}
		return true
	})

	escaped := false
	ast.Inspect(loopBody, func(n ast.Node) bool {
		if escaped {
			return false
		}
		if ident, ok := n.(*ast.Ident); ok && mutex.is(ident) && !allowed[ident] {
			// The declaring identifier itself is not a use.
			if d.typesInfo.Defs[ident] == nil {
				escaped = true
				return false
			}
		}
		return true
	})
	return escaped
}

// criticalRegions returns the AST regions guarded by the mutex: every linear
// Lock..Unlock statement window in the loop's statement lists, and for each
// function literal that references it either its own windows, when it takes
// every lock it releases, or the whole literal (a closure releasing a lock
// taken outside it, stored or deferred).
func (d *loopMutexDetector) criticalRegions(loopBody *ast.BlockStmt, mutex mutexRef) []ast.Node {
	regions, _ := d.lockWindowsUnder(loopBody, mutex)
	return regions
}

// lockWindowsUnder collects the lock windows of every statement list under
// root, stopping at function literals (see criticalRegions). unopened reports
// a release met before any acquire in its list.
func (d *loopMutexDetector) lockWindowsUnder(root ast.Node, mutex mutexRef) (regions []ast.Node, unopened bool) {
	ast.Inspect(root, func(n ast.Node) bool {
		var stmts []ast.Stmt
		switch node := n.(type) {
		case *ast.FuncLit:
			if !referencesMutex(node, mutex) {
				return false
			}
			inner, innerUnopened := d.lockWindowsUnder(node.Body, mutex)
			if innerUnopened {
				regions = append(regions, node)
			} else {
				regions = append(regions, inner...)
			}
			return false
		case *ast.BlockStmt:
			stmts = node.List
		case *ast.CaseClause:
			stmts = node.Body
		case *ast.CommClause:
			stmts = node.Body
		default:
			return true
		}
		windows, listUnopened := linearLockWindows(stmts, mutex)
		regions = append(regions, windows...)
		unopened = unopened || listUnopened
		return true
	})
	return regions, unopened
}

// linearLockWindows collects the statements between a `name.Lock()` (or RLock)
// and the matching unlock in the same statement list. A deferred unlock extends
// the window to the end of the list. unopened reports a release met while no
// window was open.
func linearLockWindows(stmts []ast.Stmt, mutex mutexRef) (regions []ast.Node, unopened bool) {
	open := false
	for _, stmt := range stmts {
		switch {
		case !open && statementIsMutexCall(stmt, mutex, "Lock", "RLock", "TryLock", "TryRLock"):
			open = true
		case statementIsMutexCall(stmt, mutex, "Unlock", "RUnlock"):
			if !open {
				unopened = true
			}
			if _, deferred := stmt.(*ast.DeferStmt); !deferred {
				open = false
			}
		case open:
			regions = append(regions, stmt)
		}
	}
	return regions, unopened
}

func statementIsMutexCall(stmt ast.Stmt, mutex mutexRef, methods ...string) bool {
	var call *ast.CallExpr
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		call, _ = s.X.(*ast.CallExpr)
	case *ast.DeferStmt:
		call = s.Call
	}
	if call == nil {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !containsMethod(methods, sel.Sel.Name) {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && mutex.is(ident)
}

// regionWritesOnlyIterationLocals reports whether every variable the region
// writes (assignment target, ++/--, or address-taken — a pointer escape is a
// potential write) is declared within the loop statement, along with how many
// variable references (reads included) the region contains. Struct fields
// inherit the locality of their base variable; non-variable objects (functions,
// types, constants, packages) are ignored.
func (d *loopMutexDetector) regionWritesOnlyIterationLocals(region ast.Node, mutex mutexRef, loop ast.Stmt) (bool, int) {
	localWrites := true
	refs := 0

	isLoopLocal := func(ident *ast.Ident) bool {
		obj := d.typesInfo.ObjectOf(ident)
		v, isVar := obj.(*types.Var)
		if !isVar || v.IsField() {
			return true
		}
		return v.Pos() >= loop.Pos() && v.Pos() <= loop.End()
	}

	checkWrite := func(expr ast.Expr) {
		ident := rootIdent(expr)
		if ident == nil || mutex.is(ident) {
			return
		}
		if !isLoopLocal(ident) {
			localWrites = false
		}
	}

	ast.Inspect(region, func(n ast.Node) bool {
		if !localWrites {
			return false
		}
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				checkWrite(lhs)
			}
		case *ast.IncDecStmt:
			checkWrite(node.X)
		case *ast.UnaryExpr:
			if node.Op == token.AND {
				checkWrite(node.X)
			}
		case *ast.Ident:
			if mutex.is(node) {
				return true
			}
			if v, ok := d.typesInfo.ObjectOf(node).(*types.Var); ok && !v.IsField() {
				refs++
			}
		}
		return true
	})
	return localWrites, refs
}

// rootIdent unwraps selectors, indexes, parens and stars down to the base
// identifier of an expression (`s.a[i].b` → `s`), or nil when there is none.
func rootIdent(expr ast.Expr) *ast.Ident {
	for {
		switch e := expr.(type) {
		case *ast.Ident:
			return e
		case *ast.SelectorExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.StarExpr:
			expr = e.X
		default:
			return nil
		}
	}
}

// mutexProtectsJoinedWorkers reports whether the loop-declared mutex is captured
// by a goroutine in the same iteration that is then joined (a `.Wait()` call), so
// the workers cannot outlive the iteration.
func mutexProtectsJoinedWorkers(loopBody *ast.BlockStmt, mutex mutexRef) bool {
	return blockContainsWaitCall(loopBody) && goroutineCapturesMutex(loopBody, mutex)
}

// blockContainsWaitCall reports whether block contains a `.Wait()` call
// (sync.WaitGroup, errgroup.Group, etc.).
func blockContainsWaitCall(block *ast.BlockStmt) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if found {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Wait" {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// goroutineCapturesMutex reports whether block launches a goroutine — via `go`
// or X.Go(...) (WaitGroup.Go, errgroup) — whose call references the mutex.
func goroutineCapturesMutex(block *ast.BlockStmt, mutex mutexRef) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.GoStmt:
			if referencesMutex(node.Call, mutex) {
				found = true
			}
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Go" {
				for _, arg := range node.Args {
					if referencesMutex(arg, mutex) {
						found = true
						break
					}
				}
			}
		}
		return !found
	})
	return found
}

// referencesMutex reports whether node's subtree refers to the mutex.
func referencesMutex(node ast.Node, mutex mutexRef) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		if ident, ok := n.(*ast.Ident); ok && mutex.is(ident) {
			found = true
			return false
		}
		return true
	})
	return found
}
