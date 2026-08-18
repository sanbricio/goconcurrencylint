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

func (d *loopMutexDetector) reportValueSpec(vs *ast.ValueSpec, loop ast.Stmt, loopBody *ast.BlockStmt) {
	for _, name := range vs.Names {
		obj := d.typesInfo.Defs[name]
		if obj == nil {
			continue
		}
		d.reportMutexDecl(obj.Type(), name.Name, name.Pos(), loop, loopBody)
	}
}

func (d *loopMutexDetector) reportAssign(s *ast.AssignStmt, loop ast.Stmt, loopBody *ast.BlockStmt) {
	for i, lhs := range s.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || i >= len(s.Rhs) {
			continue
		}
		typ := d.typesInfo.TypeOf(s.Rhs[i])
		if typ == nil {
			continue
		}
		d.reportMutexDecl(common.DerefOnceAndUnalias(typ), ident.Name, ident.Pos(), loop, loopBody)
	}
}

// reportMutexDecl flags a mutex/rwmutex declared inside a loop, unless the mutex
// is shared with per-iteration goroutines that are joined before the iteration
// ends, in which case a fresh mutex per iteration is intentional.
func (d *loopMutexDetector) reportMutexDecl(typ types.Type, name string, pos token.Pos, loop ast.Stmt, loopBody *ast.BlockStmt) {
	isMutex := common.IsMutex(typ)
	isRWMutex := common.IsRWMutex(typ)
	if !isMutex && !isRWMutex {
		return
	}

	if mutexProtectsJoinedWorkers(loopBody, name) {
		return
	}

	if d.mutexGuardsOnlyIterationLocalState(loop, loopBody, name) {
		return
	}

	mutexType := "mutex"
	if isRWMutex {
		mutexType = "rwmutex"
	}
	d.reporter.AddError(pos, category.MutexInLoop,
		mutexType+" '"+name+"' declared inside loop, each iteration creates a new mutex that cannot protect shared state")
}

var loopMutexCallMethods = []string{"Lock", "Unlock", "RLock", "RUnlock", "TryLock", "TryRLock"}

// mutexGuardsOnlyIterationLocalState reports whether every critical section of
// the loop-declared mutex writes only variables declared inside the loop
// statement, and references at least one. A fresh mutex per iteration is only a
// bug when it pretends to guard state shared ACROSS iterations; reads of outer
// variables do not make it shared, since races need a writer. An empty critical
// section, an escaping mutex, or a write to an outer variable keeps the report.
func (d *loopMutexDetector) mutexGuardsOnlyIterationLocalState(loop ast.Stmt, loopBody *ast.BlockStmt, name string) bool {
	if d.usesMutexBeyondDirectCalls(loopBody, name) {
		return false
	}

	sawState := false
	for _, region := range d.criticalRegions(loopBody, name) {
		localWrites, refs := d.regionWritesOnlyIterationLocals(region, name, loop)
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
func (d *loopMutexDetector) usesMutexBeyondDirectCalls(loopBody *ast.BlockStmt, name string) bool {
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
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == name {
			allowed[ident] = true
		}
		return true
	})

	escaped := false
	ast.Inspect(loopBody, func(n ast.Node) bool {
		if escaped {
			return false
		}
		if ident, ok := n.(*ast.Ident); ok && ident.Name == name && !allowed[ident] {
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

// criticalRegions returns the AST regions guarded by the mutex: every function
// literal that references it (a closure holding lock/unlock pairs, stored or
// deferred), and every linear Lock..Unlock statement window in the loop's own
// statement lists.
func (d *loopMutexDetector) criticalRegions(loopBody *ast.BlockStmt, name string) []ast.Node {
	var regions []ast.Node

	ast.Inspect(loopBody, func(n ast.Node) bool {
		if fnLit, ok := n.(*ast.FuncLit); ok {
			if exprReferencesVar(fnLit, name) {
				regions = append(regions, fnLit)
			}
			return false
		}
		if block, ok := n.(*ast.BlockStmt); ok {
			regions = append(regions, d.linearLockWindows(block.List, name)...)
		}
		return true
	})

	regions = append(regions, d.linearLockWindows(loopBody.List, name)...)
	return regions
}

// linearLockWindows collects the statements between a `name.Lock()` (or RLock)
// and the matching unlock in the same statement list. A deferred unlock extends
// the window to the end of the list.
func (d *loopMutexDetector) linearLockWindows(stmts []ast.Stmt, name string) []ast.Node {
	var regions []ast.Node
	open := false
	for _, stmt := range stmts {
		switch {
		case !open && statementIsMutexCall(stmt, name, "Lock", "RLock", "TryLock", "TryRLock"):
			open = true
		case open && statementIsMutexCall(stmt, name, "Unlock", "RUnlock"):
			open = false
		case open:
			regions = append(regions, stmt)
		}
	}
	return regions
}

func statementIsMutexCall(stmt ast.Stmt, name string, methods ...string) bool {
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
	return ok && ident.Name == name
}

// regionWritesOnlyIterationLocals reports whether every variable the region
// writes (assignment target, ++/--, or address-taken — a pointer escape is a
// potential write) is declared within the loop statement, along with how many
// variable references (reads included) the region contains. Struct fields
// inherit the locality of their base variable; non-variable objects (functions,
// types, constants, packages) are ignored.
func (d *loopMutexDetector) regionWritesOnlyIterationLocals(region ast.Node, name string, loop ast.Stmt) (bool, int) {
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
		if ident == nil || ident.Name == name {
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
			if node.Name == name {
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
func mutexProtectsJoinedWorkers(loopBody *ast.BlockStmt, varName string) bool {
	return blockContainsWaitCall(loopBody) && goroutineCapturesVar(loopBody, varName)
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

// goroutineCapturesVar reports whether block launches a goroutine — via `go` or
// X.Go(...) (WaitGroup.Go, errgroup) — whose call references varName.
func goroutineCapturesVar(block *ast.BlockStmt, varName string) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.GoStmt:
			if exprReferencesVar(node.Call, varName) {
				found = true
			}
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Go" {
				for _, arg := range node.Args {
					if exprReferencesVar(arg, varName) {
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

// exprReferencesVar reports whether node's subtree contains an identifier named
// varName.
func exprReferencesVar(node ast.Node, varName string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		if ident, ok := n.(*ast.Ident); ok && ident.Name == varName {
			found = true
			return false
		}
		return true
	})
	return found
}
