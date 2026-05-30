package mutex

import (
	"go/ast"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// funcAnalysis groups the per-function mutable state used by Checker.
type funcAnalysis struct {
	function               *ast.FuncDecl
	stats                  map[string]*Stats
	deferErrors            *deferErrorCollector
	rawBodyEffects         bool
	goroutineLockConflicts []goroutineLockConflict
	tryLock                *tryLockTracker
	collectionLengths      map[string]int
	terminatingTailDepth   int
	labelGotoSnapshots     map[string]map[string]*Stats
	simulationStack        map[methodSimulationKey]bool
	localFuncStack         map[*ast.FuncLit]bool

	// callerManagedCache memoizes functionIsCallerManagedReleaseFor results
	// for the current function. It belongs here (not on Checker) because the
	// underlying computation reads ma.function; sharing the cache across
	// functions would surface stale results for unrelated callers.
	callerManagedCache map[callerManagedKey]bool
}

func newFuncAnalysis(fn *ast.FuncDecl) *funcAnalysis {
	// tryLock is wired separately (in AnalyzeFunction / forkForSimulation)
	// because the tracker needs the Checker's names and reporting boundary,
	// which newFuncAnalysis does not have.
	return &funcAnalysis{
		function:           fn,
		stats:              make(map[string]*Stats),
		deferErrors:        newDeferErrorCollector(),
		collectionLengths:  make(map[string]int),
		callerManagedCache: make(map[callerManagedKey]bool),
	}
}

func newDeferErrorCollector() *deferErrorCollector {
	return &deferErrorCollector{
		badDeferUnlock:  make(map[string]bool),
		badDeferRUnlock: make(map[string]bool),
	}
}

// newSimulationFuncAnalysis builds a funcAnalysis configured for a simulated
// run: rawBodyEffects is set so reporting paths short-circuit, and the
// recursion stacks are inherited from the caller so cycle detection survives
// across the fork.
func newSimulationFuncAnalysis(fn *ast.FuncDecl, simStack map[methodSimulationKey]bool, localStack map[*ast.FuncLit]bool) *funcAnalysis {
	fa := newFuncAnalysis(fn)
	fa.rawBodyEffects = true
	fa.simulationStack = simStack
	fa.localFuncStack = localStack
	return fa
}

// forkForSimulation builds a sibling Checker that shares the package-wide
// configuration with the receiver but uses the supplied per-function state
// and primitive name maps. The fork gets its own ErrorCollector so
// simulation diagnostics do not leak into the parent run.
func (ma *Checker) forkForSimulation(fa *funcAnalysis, mutexNames, rwMutexNames map[string]bool) *Checker {
	sim := &Checker{
		mutexNames:            mutexNames,
		rwMutexNames:          rwMutexNames,
		errorCollector:        &report.ErrorCollector{},
		commentFilter:         ma.commentFilter,
		typesInfo:             ma.typesInfo,
		receiverMethods:       ma.receiverMethods,
		functions:             ma.functions,
		explicitTransferCache: ma.explicitTransferCache,
		funcAnalysis:          fa,
	}
	// Wire the tracker against the fork's own names and (isolated) collector so
	// simulation diagnostics never leak into the parent run.
	sim.tryLock = newTryLockTracker(sim.mutexNames, sim.rwMutexNames, sim.commentFilter, sim.errorCollector)
	return sim
}
