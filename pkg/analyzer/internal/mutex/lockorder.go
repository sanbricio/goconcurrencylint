package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"sort"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// lockOrderDetector reports inconsistent lock acquisition order ("lock order
// cycle"): two mutexes acquired as A-then-B on one path and B-then-A on
// another, the classic AB-BA deadlock shape.
//
// It was extracted from Checker because the detection is self-contained: each
// run builds its own edge/held bookkeeping from a function body and only reads
// package-wide configuration (the primitive name sets, the comment filter, type
// info and the reporting boundary). It holds no per-function mutable state, so a
// single instance can analyze any number of functions.
type lockOrderDetector struct {
	mutexNames    map[string]bool
	rwMutexNames  map[string]bool
	commentFilter *commentfilter.CommentFilter
	typesInfo     *types.Info
	reporter      report.Reporter
}

func newLockOrderDetector(mutexNames, rwMutexNames map[string]bool, cf *commentfilter.CommentFilter, typesInfo *types.Info, reporter report.Reporter) *lockOrderDetector {
	return &lockOrderDetector{
		mutexNames:    mutexNames,
		rwMutexNames:  rwMutexNames,
		commentFilter: cf,
		typesInfo:     typesInfo,
		reporter:      reporter,
	}
}

// lockOrderEdge is a directed "acquired from while holding to" relation between
// two named locks. Observing both an edge and its reverse is a cycle.
type lockOrderEdge struct {
	from string
	to   string
}

// waitGroupReleaseEvent records that a goroutine guarded by wgName releases the
// given locks before signalling Done, so a wg.Wait() following the `go` at
// goPos can be treated as releasing those locks in the waiter.
type waitGroupReleaseEvent struct {
	wgName    string
	goPos     token.Pos
	lockNames map[string]bool
}

// check scans block for lock-order cycles and reports any it finds.
func (d *lockOrderDetector) check(block *ast.BlockStmt) {
	if block == nil {
		return
	}

	edges := make(map[lockOrderEdge]token.Pos)
	reported := make(map[lockOrderEdge]bool)
	waitReleases := d.waitGroupReleasedLocks(block)
	d.scanStatements(block.List, make(map[string]bool), edges, reported, waitReleases)
}

func (d *lockOrderDetector) scanStatements(stmts []ast.Stmt, held map[string]bool, edges map[lockOrderEdge]token.Pos, reported map[lockOrderEdge]bool, waitReleases []waitGroupReleaseEvent) {
	for _, stmt := range stmts {
		if stmt == nil || d.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}

		switch s := stmt.(type) {
		case *ast.ExprStmt:
			expr := common.UnwrapParenExpr(s.X)
			if call, ok := expr.(*ast.CallExpr); ok {
				d.scanCall(call, held, edges, reported, waitReleases)
			}
		case *ast.DeferStmt:
			d.scanDefer(s, held, edges, reported, waitReleases)
		case *ast.GoStmt:
			if fnLit, ok := s.Call.Fun.(*ast.FuncLit); ok && fnLit.Body != nil {
				d.scanStatements(fnLit.Body.List, make(map[string]bool), edges, reported, waitReleases)
			}
		case *ast.IfStmt:
			thenHeld := maps.Clone(held)
			if varName, ok := d.tryLockCallVar(s.Cond); ok {
				d.recordHeldEdges(varName, s.Cond.Pos(), thenHeld, edges, reported)
				thenHeld[varName] = true
			}
			d.scanStatements(s.Body.List, thenHeld, edges, reported, waitReleases)
			if s.Else != nil {
				d.scanElse(s.Else, maps.Clone(held), edges, reported, waitReleases)
			}
		case *ast.ForStmt:
			if s.Body != nil {
				d.scanStatements(s.Body.List, maps.Clone(held), edges, reported, waitReleases)
			}
		case *ast.RangeStmt:
			if s.Body != nil {
				d.scanStatements(s.Body.List, maps.Clone(held), edges, reported, waitReleases)
			}
		case *ast.BlockStmt:
			d.scanStatements(s.List, held, edges, reported, waitReleases)
		case *ast.LabeledStmt:
			d.scanStatements([]ast.Stmt{s.Stmt}, held, edges, reported, waitReleases)
		case *ast.SwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					d.scanStatements(cc.Body, maps.Clone(held), edges, reported, waitReleases)
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					d.scanStatements(cc.Body, maps.Clone(held), edges, reported, waitReleases)
				}
			}
		case *ast.SelectStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CommClause); ok {
					d.scanStatements(cc.Body, maps.Clone(held), edges, reported, waitReleases)
				}
			}
		}
	}
}

func (d *lockOrderDetector) scanCall(call *ast.CallExpr, held map[string]bool, edges map[lockOrderEdge]token.Pos, reported map[lockOrderEdge]bool, waitReleases []waitGroupReleaseEvent) {
	if call == nil || d.commentFilter.ShouldSkipCall(call) {
		return
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	varName := common.GetVarName(sel.X)
	switch {
	case d.isAcquire(varName, sel.Sel.Name):
		d.recordHeldEdges(varName, call.Pos(), held, edges, reported)
		held[varName] = true
	case d.isRelease(varName, sel.Sel.Name):
		delete(held, varName)
	}

	if sel.Sel.Name == "Wait" && d.isWaitGroupReceiver(sel.X) {
		d.applyWaitGroupReleaseEvents(held, varName, call.Pos(), waitReleases)
	}
}

func (d *lockOrderDetector) scanDefer(stmt *ast.DeferStmt, held map[string]bool, edges map[lockOrderEdge]token.Pos, reported map[lockOrderEdge]bool, waitReleases []waitGroupReleaseEvent) {
	if stmt == nil || d.commentFilter.ShouldSkipCall(stmt.Call) {
		return
	}
	if call, ok := stmt.Call.Fun.(*ast.FuncLit); ok && call.Body != nil {
		d.scanStatements(call.Body.List, maps.Clone(held), edges, reported, waitReleases)
	}
}

func (d *lockOrderDetector) scanElse(stmt ast.Stmt, held map[string]bool, edges map[lockOrderEdge]token.Pos, reported map[lockOrderEdge]bool, waitReleases []waitGroupReleaseEvent) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		d.scanStatements(s.List, held, edges, reported, waitReleases)
	case *ast.IfStmt:
		d.scanStatements([]ast.Stmt{s}, held, edges, reported, waitReleases)
	}
}

func (d *lockOrderDetector) applyWaitGroupReleaseEvents(held map[string]bool, wgName string, waitPos token.Pos, waitReleases []waitGroupReleaseEvent) {
	for _, event := range waitReleases {
		if event.wgName != wgName || event.goPos >= waitPos {
			continue
		}
		for releasedName := range event.lockNames {
			delete(held, releasedName)
		}
	}
}

func (d *lockOrderDetector) waitGroupReleasedLocks(block *ast.BlockStmt) []waitGroupReleaseEvent {
	var releases []waitGroupReleaseEvent
	if block == nil {
		return releases
	}

	ast.Inspect(block, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		goStmt, ok := n.(*ast.GoStmt)
		if !ok || d.commentFilter.ShouldSkipStatement(goStmt) {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return false
		}
		for wgName, lockNames := range d.goroutineReleasedLocksBeforeDone(fnLit.Body) {
			releases = append(releases, waitGroupReleaseEvent{
				wgName:    wgName,
				goPos:     goStmt.Pos(),
				lockNames: lockNames,
			})
		}
		return false
	})
	return releases
}

func (d *lockOrderDetector) goroutineReleasedLocksBeforeDone(body *ast.BlockStmt) map[string]map[string]bool {
	releases := make(map[string]map[string]bool)
	if body == nil {
		return releases
	}

	deferredDone := make(map[string]bool)
	directDone := make(map[string][]token.Pos)
	unlocks := make(map[string][]token.Pos)

	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.DeferStmt:
			if wgName, ok := d.waitGroupDoneCallName(node.Call); ok {
				deferredDone[wgName] = true
			}
			return false
		case *ast.CallExpr:
			if d.commentFilter.ShouldSkipCall(node) {
				return true
			}
			if wgName, ok := d.waitGroupDoneCallName(node); ok {
				directDone[wgName] = append(directDone[wgName], node.Pos())
				return true
			}
			if lockName, ok := d.releaseCallName(node); ok {
				unlocks[lockName] = append(unlocks[lockName], node.Pos())
			}
		}
		return true
	})

	for wgName := range deferredDone {
		d.addReleasedLocks(releases, wgName, unlocks)
	}
	for wgName, donePositions := range directDone {
		firstDone := firstPos(donePositions)
		beforeDone := make(map[string][]token.Pos)
		for lockName, positions := range unlocks {
			for _, pos := range positions {
				if pos < firstDone {
					beforeDone[lockName] = append(beforeDone[lockName], pos)
				}
			}
		}
		d.addReleasedLocks(releases, wgName, beforeDone)
	}

	return releases
}

func (d *lockOrderDetector) addReleasedLocks(releases map[string]map[string]bool, wgName string, unlocks map[string][]token.Pos) {
	if len(unlocks) == 0 {
		return
	}
	if releases[wgName] == nil {
		releases[wgName] = make(map[string]bool)
	}
	for lockName := range unlocks {
		releases[wgName][lockName] = true
	}
}

func firstPos(positions []token.Pos) token.Pos {
	first := token.NoPos
	for _, pos := range positions {
		if first == token.NoPos || pos < first {
			first = pos
		}
	}
	return first
}

func (d *lockOrderDetector) waitGroupDoneCallName(call *ast.CallExpr) (string, bool) {
	if call == nil || d.commentFilter.ShouldSkipCall(call) {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Done" || !d.isWaitGroupReceiver(sel.X) {
		return "", false
	}
	return common.GetVarName(sel.X), true
}

func (d *lockOrderDetector) releaseCallName(call *ast.CallExpr) (string, bool) {
	if call == nil || d.commentFilter.ShouldSkipCall(call) {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	varName := common.GetVarName(sel.X)
	if !d.isRelease(varName, sel.Sel.Name) {
		return "", false
	}
	return varName, true
}

func (d *lockOrderDetector) isWaitGroupReceiver(expr ast.Expr) bool {
	if d.typesInfo == nil {
		return false
	}
	typ := d.typesInfo.TypeOf(expr)
	typ = common.DerefOnceAndUnalias(typ)
	return common.MatchesPkgAndName(typ, "sync", "WaitGroup")
}

func (d *lockOrderDetector) tryLockCallVar(expr ast.Expr) (string, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	varName := common.GetVarName(sel.X)
	switch sel.Sel.Name {
	case "TryLock":
		if d.mutexNames[varName] || d.rwMutexNames[varName] {
			return varName, true
		}
	case "TryRLock":
		if d.rwMutexNames[varName] {
			return varName, true
		}
	}
	return "", false
}

func (d *lockOrderDetector) recordHeldEdges(varName string, pos token.Pos, held map[string]bool, edges map[lockOrderEdge]token.Pos, reported map[lockOrderEdge]bool) {
	for heldName := range held {
		if heldName == varName {
			continue
		}

		edge := lockOrderEdge{from: heldName, to: varName}
		reverse := lockOrderEdge{from: varName, to: heldName}
		if _, exists := edges[edge]; !exists {
			edges[edge] = pos
		}
		if _, exists := edges[reverse]; !exists {
			continue
		}

		reportKey := normalizedEdge(heldName, varName)
		if reported[reportKey] {
			continue
		}
		reported[reportKey] = true
		first, second := orderedLockNames(heldName, varName)
		d.reporter.AddError(pos, category.LockOrderCycle, "mutex lock order cycle between '"+first+"' and '"+second+"'")
	}
}

func normalizedEdge(a, b string) lockOrderEdge {
	first, second := orderedLockNames(a, b)
	return lockOrderEdge{from: first, to: second}
}

func orderedLockNames(a, b string) (string, string) {
	names := []string{a, b}
	sort.Strings(names)
	return names[0], names[1]
}

func (d *lockOrderDetector) isAcquire(varName, methodName string) bool {
	switch methodName {
	case "Lock", "TryLock":
		return d.mutexNames[varName] || d.rwMutexNames[varName]
	case "RLock", "TryRLock":
		return d.rwMutexNames[varName]
	default:
		return false
	}
}

func (d *lockOrderDetector) isRelease(varName, methodName string) bool {
	switch methodName {
	case "Unlock":
		return d.mutexNames[varName] || d.rwMutexNames[varName]
	case "RUnlock":
		return d.rwMutexNames[varName]
	default:
		return false
	}
}
