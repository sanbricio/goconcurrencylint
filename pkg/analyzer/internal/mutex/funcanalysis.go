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
	wrapper                *wrapperResolver
	lifecycle              *lifecycleResolver
	panicDetector          *lockedPanicDetector
	terminatingTailDepth   int
	labelGotoSnapshots     map[string]map[string]*Stats
	simulationStack        map[methodSimulationKey]bool
	localFuncStack         map[*ast.FuncLit]bool
}

func newFuncAnalysis(fn *ast.FuncDecl) *funcAnalysis {
	// tryLock is wired separately (in AnalyzeFunction / forkForSimulation)
	// because the tracker needs the Checker's names and reporting boundary,
	// which newFuncAnalysis does not have.
	return &funcAnalysis{
		function:    fn,
		stats:       make(map[string]*Stats),
		deferErrors: newDeferErrorCollector(),
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
func (c *Checker) forkForSimulation(fa *funcAnalysis, mutexNames, rwMutexNames map[string]bool) *Checker {
	sim := &Checker{
		mutexNames:            mutexNames,
		rwMutexNames:          rwMutexNames,
		errorCollector:        &report.ErrorCollector{},
		commentFilter:         c.commentFilter,
		typesInfo:             c.typesInfo,
		receiverMethods:       c.receiverMethods,
		functions:             c.functions,
		termination:           c.termination,
		loopCarry:             c.loopCarry,
		explicitTransferCache: c.explicitTransferCache,
		funcAnalysis:          fa,
	}
	// Wire the per-function collaborators against the fork's own names and
	// (isolated) collector so simulation diagnostics never leak into the parent
	// run. fa.rawBodyEffects is true here, so the wrapper resolver stays inert.
	sim.tryLock = newTryLockTracker(sim.mutexNames, sim.rwMutexNames, sim.commentFilter, sim.errorCollector)
	sim.wrapper = newWrapperResolver(sim.receiverMethods, fa.function, fa.rawBodyEffects)
	sim.lifecycle = newLifecycleResolver(sim.receiverMethods, sim.functions, sim.typesInfo, sim.explicitTransferCache, fa.function)
	sim.panicDetector = newLockedPanicDetector(sim.mutexNames, sim.rwMutexNames, sim.typesInfo, sim.errorCollector, fa.rawBodyEffects)
	return sim
}
