package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// The main analysis keeps one lock state per program point. Where two branches
// leave different states it can only report the difference, even when a later
// branch on the same decision balances it:
//
//	bucket, ok := buckets[id]
//	held := false
//	if !ok { mu.Lock(); held = true }
//	if bucket == nil {
//		if held { mu.Unlock() }
//		return
//	}
//	if !held { mu.Lock() }
//	mu.Unlock()
//
// Every path through that function is balanced, and a reader sees it by
// following `ok` and `held`. A refined run does the same: it re-analyzes the
// function knowing what local booleans and nil checks hold (facts.go), and
// where a decision still splits the lock state it analyzes the function once
// per outcome. The splits are on decisions taken once per call — a parameter
// or a local that is assigned once outside any loop, or an `if` that runs at
// most once — so together the runs cover every path.
//
// Refinement only ever takes diagnostics away. It runs for a function whose
// main analysis reported a lock-state problem, and that report is dropped only
// when every refined run is free of lock-state problems; otherwise the main
// analysis's diagnostics stand as they were.

// lockStateCategories are the diagnostics that follow from the lock state
// along a path, the ones a more precise path analysis can disprove.
var lockStateCategories = map[category.Category]bool{
	category.LockWithoutUnlock:      true,
	category.UnlockWithoutLock:      true,
	category.DeferUnlockWithoutLock: true,
	category.RWMutexAPIMismatch:     true,
	category.GoroutineLockDeadlock:  true,
	category.PanicBeforeUnlock:      true,
	category.DoubleLock:             true,
	category.RWMutexRecursiveLock:   true,
}

const (
	// maxRefinedRuns bounds the re-analyses of one function.
	maxRefinedRuns = 8
	// maxAssumptions bounds how many decisions one run fixes.
	maxAssumptions = 3
)

// refinement is what a refined run knows beyond the main analysis. Every
// simulation forked from the run shares it.
type refinement struct {
	root *ast.FuncDecl
	// vars fixes the value of invariant variables of root.
	vars map[types.Object]bool
	// ifs fixes the outcome of `if` statements of root that run at most once.
	ifs    map[*ast.IfStmt]bool
	record *refineRecord
	scopes map[*ast.FuncDecl]*varScope
}

// refineRecord collects what one run learns while it goes.
type refineRecord struct {
	// disagreements are the `if` statements of root whose branches left
	// different lock states the run could not reconcile.
	disagreements []*ast.IfStmt
	seen          map[*ast.IfStmt]bool
	// failedTest is set once the run follows the branch where a test reports
	// its own failure (see isTestFailureBlock).
	failedTest bool
	// calleeDeadlock is set when a simulated callee takes a lock the run
	// already holds. Simulations report nothing themselves, so without it a
	// run could look clean exactly because a callee re-locked what the caller
	// holds.
	calleeDeadlock bool
}

// simulationReporter receives the diagnostics of a simulation forked from a
// refined run: it keeps none, but notes a self-deadlock.
type simulationReporter struct {
	record *refineRecord
}

func (s *simulationReporter) AddError(_ token.Pos, cat category.Category, _ string) {
	if cat == category.DoubleLock || cat == category.RWMutexRecursiveLock {
		s.record.calleeDeadlock = true
	}
}

func (r *refinement) scopeOf(fn *ast.FuncDecl, info *types.Info) *varScope {
	if scope, ok := r.scopes[fn]; ok {
		return scope
	}
	scope := computeVarScope(fn, info)
	r.scopes[fn] = scope
	return scope
}

// recordDisagreement notes an `if` of the refined function whose branches the
// run could not reconcile: the decision a further split can fix.
func (c *Checker) recordDisagreement(stmt *ast.IfStmt) {
	if c.refine == nil || c.refine.record.seen[stmt] {
		return
	}
	body := c.refine.root.Body
	if stmt.Pos() < body.Pos() || stmt.End() > body.End() {
		return
	}
	c.refine.record.seen[stmt] = true
	c.refine.record.disagreements = append(c.refine.record.disagreements, stmt)
}

// noteBranchTaken marks the run as following a failed test when the branch it
// knows is taken is one where the test reports its failure.
func (c *Checker) noteBranchTaken(branch ast.Stmt) {
	if c.refine == nil || branch == nil || c.function != c.refine.root {
		return
	}
	if block, ok := branch.(*ast.BlockStmt); ok && c.isTestFailureBlock(block) {
		c.refine.record.failedTest = true
	}
}

// isTestFailureBlock reports whether block marks its test as failed: it calls
// Error, Errorf or Fail on a testing.TB-like value (one that also has FailNow).
// What a test does after it has already failed is not held to lock balance:
//
//	if !mu.TryLock() { t.Errorf("TryLock failed after release") }
//	mu.Unlock()
//
// unlocks a lock it may not hold only on the path where the test reported
// that very problem.
func (c *Checker) isTestFailureBlock(block *ast.BlockStmt) bool {
	for _, stmt := range block.List {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		switch sel.Sel.Name {
		case "Error", "Errorf", "Fail":
		default:
			continue
		}
		if c.typesInfo != nil && hasMethodNamed(c.typesInfo.TypeOf(sel.X), "FailNow") {
			return true
		}
	}
	return false
}

func hasMethodNamed(typ types.Type, name string) bool {
	if typ == nil {
		return false
	}
	obj, _, _ := types.LookupFieldOrMethod(typ, true, nil, name)
	_, isFunc := obj.(*types.Func)
	return isFunc
}

// bufferedReporter holds the diagnostics of one function until refinement has
// decided which of them stand.
type bufferedReporter struct {
	entries []report.ErrorReport
}

func (b *bufferedReporter) AddError(pos token.Pos, cat category.Category, message string) {
	b.entries = append(b.entries, report.ErrorReport{Pos: pos, Category: cat, Message: message})
}

func (b *bufferedReporter) hasAny(categories map[category.Category]bool) bool {
	return slices.ContainsFunc(b.entries, func(e report.ErrorReport) bool { return categories[e.Category] })
}

func (b *bufferedReporter) drop(categories map[category.Category]bool) {
	b.entries = slices.DeleteFunc(b.entries, func(e report.ErrorReport) bool { return categories[e.Category] })
}

func (b *bufferedReporter) flushTo(out report.Reporter) {
	for _, e := range b.entries {
		out.AddError(e.Pos, e.Category, e.Message)
	}
}

// assumptionSet is the set of decisions one refined run fixes.
type assumptionSet struct {
	vars map[types.Object]bool
	ifs  map[*ast.IfStmt]bool
}

func (a assumptionSet) size() int { return len(a.vars) + len(a.ifs) }

func (a assumptionSet) withVar(obj types.Object, value bool) assumptionSet {
	vars := make(map[types.Object]bool, len(a.vars)+1)
	maps.Copy(vars, a.vars)
	vars[obj] = value
	return assumptionSet{vars: vars, ifs: a.ifs}
}

func (a assumptionSet) withIf(stmt *ast.IfStmt, value bool) assumptionSet {
	ifs := make(map[*ast.IfStmt]bool, len(a.ifs)+1)
	maps.Copy(ifs, a.ifs)
	ifs[stmt] = value
	return assumptionSet{vars: a.vars, ifs: ifs}
}

// refinementClearsLockState reports whether every refined run of fn is free of
// lock-state diagnostics, splitting runs on the decisions they could not
// reconcile until each is clean or the budget runs out.
func (c *Checker) refinementClearsLockState(fn *ast.FuncDecl) bool {
	// A label lets control come back to code a path already left, which the
	// runs' notion of an ended path (see pathEnded) does not follow.
	if hasLabels(fn) {
		return false
	}
	work := []assumptionSet{{vars: map[types.Object]bool{}, ifs: map[*ast.IfStmt]bool{}}}
	for runs := 0; len(work) > 0; runs++ {
		if runs == maxRefinedRuns {
			return false
		}
		assumed := work[len(work)-1]
		work = work[:len(work)-1]

		reporter := &bufferedReporter{}
		refine := &refinement{
			root:   fn,
			vars:   assumed.vars,
			ifs:    assumed.ifs,
			record: &refineRecord{seen: map[*ast.IfStmt]bool{}},
			scopes: c.varScopes,
		}
		c.runFunction(fn, reporter, refine)
		if refine.record.failedTest || (!reporter.hasAny(lockStateCategories) && !refine.record.calleeDeadlock) {
			continue
		}
		if assumed.size() >= maxAssumptions {
			return false
		}

		obj, stmt, ok := c.nextAssumption(refine, assumed)
		switch {
		case !ok:
			return false
		case obj != nil:
			work = append(work, assumed.withVar(obj, true), assumed.withVar(obj, false))
		default:
			work = append(work, assumed.withIf(stmt, true), assumed.withIf(stmt, false))
		}
	}
	return true
}

// nextAssumption picks the decision to split a run on: the first unreconciled
// `if` whose condition reads an invariant variable not fixed yet, or else the
// first one that runs at most once.
func (c *Checker) nextAssumption(refine *refinement, assumed assumptionSet) (types.Object, *ast.IfStmt, bool) {
	disagreements := slices.Clone(refine.record.disagreements)
	slices.SortFunc(disagreements, func(a, b *ast.IfStmt) int { return int(a.Pos() - b.Pos()) })

	invariant := refine.scopeOf(refine.root, c.typesInfo).invariant
	for _, stmt := range disagreements {
		var found types.Object
		ast.Inspect(stmt.Cond, func(n ast.Node) bool {
			if found != nil {
				return false
			}
			if ident, ok := n.(*ast.Ident); ok {
				if obj := c.typesInfo.ObjectOf(ident); obj != nil && invariant[obj] {
					if _, fixed := assumed.vars[obj]; !fixed {
						found = obj
					}
				}
			}
			return true
		})
		if found != nil {
			return found, nil, true
		}
	}
	for _, stmt := range disagreements {
		if _, fixed := assumed.ifs[stmt]; !fixed && runsAtMostOnce(refine.root, stmt) {
			return nil, stmt, true
		}
	}
	return nil, nil, false
}

func hasLabels(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.LabeledStmt); ok {
			found = true
		}
		return !found
	})
	return found
}

// runsAtMostOnce reports whether stmt executes at most once per call of fn: it
// sits in no loop and no closure (fn has no labels, see hasLabels).
func runsAtMostOnce(fn *ast.FuncDecl, stmt ast.Stmt) bool {
	once := true
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if !once || n == nil {
			return false
		}
		switch n.(type) {
		case *ast.ForStmt, *ast.RangeStmt, *ast.FuncLit:
			if n.Pos() <= stmt.Pos() && stmt.End() <= n.End() {
				once = false
				return false
			}
		}
		return true
	})
	return once
}
