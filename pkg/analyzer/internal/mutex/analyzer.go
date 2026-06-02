package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/primitives"
)

// Checker handles the analysis of mutex and rwmutex usage.
//
// Fields fall into two groups: package-wide configuration set once in
// NewChecker and never mutated, and per-function state in the embedded
// *funcAnalysis. AnalyzeFunction replaces the embed for each function so the
// per-call reset is a single assignment instead of clearing fields by hand.
type Checker struct {
	mutexNames      map[string]bool
	rwMutexNames    map[string]bool
	errorCollector  report.Reporter
	commentFilter   *commentfilter.CommentFilter
	typesInfo       *types.Info
	receiverMethods map[string]map[string]*ast.FuncDecl
	functions       []*ast.FuncDecl
	termination     *terminationAnalyzer
	loopCarry       *loopCarryAnalyzer

	// explicitTransferCache is keyed by *ast.BlockStmt so it remains correct
	// across functions; the cached map is shared by reference, callers must
	// treat it as read-only.
	explicitTransferCache map[*ast.BlockStmt]map[token.Pos]struct{}

	*funcAnalysis
}

// goroutineLockConflict records a goroutine that was launched while the parent
// held a mutex that the goroutine also tries to acquire.
type goroutineLockConflict struct {
	varName        string
	pos            token.Pos
	isRWMutex      bool
	parentReadLock bool
	requestMethod  string
}

type methodSimulationKey struct {
	fn        *ast.FuncDecl
	varName   string
	isRWMutex bool
}

// Stats tracks the state of a mutex within a block
type Stats struct {
	lock, rlock                 int
	borrowedLock, borrowedRLock int
	deferUnlock, deferRUnlock   int
	lockPos, rlockPos           []token.Pos
	borrowedUnlockPos           []token.Pos
	borrowedRUnlockPos          []token.Pos
}

// deferErrorCollector tracks defer-related errors to avoid duplicate reporting
type deferErrorCollector struct {
	badDeferUnlock  map[string]bool
	badDeferRUnlock map[string]bool
}

// NewChecker creates a new mutex checker. fr supplies the mutex and
// rwmutex names visible inside the function being analyzed.
func NewChecker(fr *primitives.FunctionResult, errorCollector report.Reporter, cf *commentfilter.CommentFilter, typesInfo *types.Info, files []*ast.File) *Checker {
	term := newTerminationAnalyzer(typesInfo)
	ma := &Checker{
		mutexNames:            fr.Mutexes,
		rwMutexNames:          fr.RWMutexes,
		errorCollector:        errorCollector,
		commentFilter:         cf,
		typesInfo:             typesInfo,
		receiverMethods:       buildReceiverMethodMap(files),
		functions:             collectFunctionDecls(files),
		termination:           term,
		explicitTransferCache: make(map[*ast.BlockStmt]map[token.Pos]struct{}),
	}
	ma.loopCarry = newLoopCarryAnalyzer(ma.mutexNames, ma.rwMutexNames, cf, errorCollector, term)
	return ma
}

func (ma *Checker) AnalyzeFunction(fn *ast.FuncDecl) {
	ma.funcAnalysis = newFuncAnalysis(fn)
	ma.tryLock = newTryLockTracker(ma.mutexNames, ma.rwMutexNames, ma.commentFilter, ma.errorCollector)
	ma.wrapper = newWrapperResolver(ma.receiverMethods, ma.function, ma.rawBodyEffects)
	ma.lifecycle = newLifecycleResolver(ma.receiverMethods, ma.functions, ma.typesInfo, ma.explicitTransferCache, ma.function)
	ma.panicDetector = newLockedPanicDetector(ma.mutexNames, ma.rwMutexNames, ma.typesInfo, ma.errorCollector, ma.rawBodyEffects)
	ma.stats = initialStats(ma.mutexNames, ma.rwMutexNames)
	lockOrder := newLockOrderDetector(ma.mutexNames, ma.rwMutexNames, ma.commentFilter, ma.typesInfo, ma.errorCollector)
	lockOrder.check(fn.Body)
	finalStats := ma.analyzeBlock(fn.Body, ma.stats)
	ma.tryLock.reportUnchecked()
	ma.reportUnmatchedLocks(finalStats)
}

func relativeMutexPath(varName, prefix string) (string, bool) {
	relative, ok := strings.CutPrefix(varName, prefix+".")
	if !ok || relative == "" {
		return "", false
	}
	return relative, true
}

func splitBaseAndSuffix(varName string) (string, string, bool) {
	parts := strings.Split(varName, ".")
	if len(parts) < 2 {
		return "", "", false
	}
	return parts[0], strings.Join(parts[1:], "."), true
}

// functionIsParameterUnlockHelper reports helpers that only release a mutex
// parameter.
func (ma *Checker) functionIsParameterUnlockHelper(varName string, acquireMethods []string) bool {
	if ma.function == nil || ma.function.Body == nil {
		return false
	}
	if !ma.varRootIsFunctionParameter(varName) {
		return false
	}

	releaseSet := make(map[string]struct{})
	for _, acquire := range acquireMethods {
		if release := matchingUnlockMethod(acquire); release != "" {
			releaseSet[release] = struct{}{}
		}
	}
	if len(releaseSet) == 0 {
		return false
	}

	acquireSet := make(map[string]struct{}, len(acquireMethods))
	for _, acquire := range acquireMethods {
		acquireSet[acquire] = struct{}{}
	}

	sawRelease := false
	sawAcquire := false
	ast.Inspect(ma.function.Body, func(n ast.Node) bool {
		if sawAcquire {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if common.GetVarName(sel.X) != varName {
			return true
		}
		if _, ok := acquireSet[sel.Sel.Name]; ok {
			sawAcquire = true
			return false
		}
		if _, ok := releaseSet[sel.Sel.Name]; ok {
			sawRelease = true
		}
		return true
	})
	return sawRelease && !sawAcquire
}

// varRootIsFunctionParameter reports whether `varName` starts at a parameter.
func (ma *Checker) varRootIsFunctionParameter(varName string) bool {
	if ma.function == nil || ma.function.Type == nil || ma.function.Type.Params == nil {
		return false
	}
	base := varName
	if before, _, ok := strings.Cut(varName, "."); ok {
		base = before
	}
	for _, field := range ma.function.Type.Params.List {
		for _, name := range field.Names {
			if name.Name == base {
				return true
			}
		}
	}
	return false
}

func (ma *Checker) simulateMethodEffect(fn *ast.FuncDecl, varName string, isRWMutex bool, initial *Stats) *Stats {
	if fn == nil || fn.Body == nil {
		return nil
	}

	stack := ma.simulationStack
	if stack == nil {
		stack = make(map[methodSimulationKey]bool)
		ma.simulationStack = stack
	}

	key := methodSimulationKey{fn: fn, varName: varName, isRWMutex: isRWMutex}
	if stack[key] {
		return cloneStats(initial)
	}

	stack[key] = true
	defer delete(stack, key)

	mutexNames := map[string]bool{}
	rwMutexNames := map[string]bool{}
	if isRWMutex {
		rwMutexNames[varName] = true
	} else {
		mutexNames[varName] = true
	}

	sub := newSimulationFuncAnalysis(fn, stack, ma.localFuncStack)
	simulated := ma.forkForSimulation(sub, mutexNames, rwMutexNames)

	start := map[string]*Stats{varName: cloneStats(initial)}
	final := simulated.analyzeBlock(fn.Body, start)
	simulated.applyFunctionExitDefers(final, start)
	return cloneStats(final[varName])
}

func (ma *Checker) applyLocalFunctionLiteralLifecycleEffects(call *ast.CallExpr, stats map[string]*Stats) bool {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}

	fnlit := ma.localFunctionLiteralBefore(ident.Name, call.Pos())
	if fnlit == nil || fnlit.Body == nil {
		return false
	}

	stack := ma.localFuncStack
	if stack == nil {
		stack = make(map[*ast.FuncLit]bool)
		ma.localFuncStack = stack
	}
	if stack[fnlit] {
		return false
	}

	stack[fnlit] = true
	defer delete(stack, fnlit)

	sub := newSimulationFuncAnalysis(ma.function, ma.simulationStack, stack)
	simulated := ma.forkForSimulation(sub, ma.mutexNames, ma.rwMutexNames)

	baseline := cloneStatsMap(stats)
	final := simulated.analyzeBlock(fnlit.Body, baseline)
	simulated.applyFunctionExitDefers(final, baseline)
	copyStatsMap(stats, final)
	return true
}

func (ma *Checker) localFunctionLiteralBefore(name string, before token.Pos) *ast.FuncLit {
	if ma.function == nil || ma.function.Body == nil {
		return nil
	}

	var found *ast.FuncLit
	ast.Inspect(ma.function.Body, func(n ast.Node) bool {
		if n == nil || n.Pos() >= before {
			return false
		}

		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name != name || i >= len(node.Rhs) {
					continue
				}
				if fnlit, ok := node.Rhs[i].(*ast.FuncLit); ok {
					found = fnlit
				}
			}
		case *ast.ValueSpec:
			for i, ident := range node.Names {
				if ident.Name != name || i >= len(node.Values) {
					continue
				}
				if fnlit, ok := node.Values[i].(*ast.FuncLit); ok {
					found = fnlit
				}
			}
		}

		return true
	})

	return found
}

func (ma *Checker) applyFunctionExitDefers(stats, baseline map[string]*Stats) {
	for name, st := range stats {
		baselineDeferUnlocks := 0
		baselineDeferRUnlocks := 0
		if baselineStats := baseline[name]; baselineStats != nil {
			baselineDeferUnlocks = baselineStats.deferUnlock
			baselineDeferRUnlocks = baselineStats.deferRUnlock
		}

		for st.deferUnlock > baselineDeferUnlocks && st.lock > 0 {
			st.deferUnlock--
			st.lock--
			st.removeFirstLockPos()
		}
		st.deferUnlock = baselineDeferUnlocks
		for st.deferRUnlock > baselineDeferRUnlocks && st.rlock > 0 {
			st.deferRUnlock--
			st.rlock--
			st.removeFirstRLockPos()
		}
		st.deferRUnlock = baselineDeferRUnlocks
	}
}

func (ma *Checker) applyLocalMethodLifecycleEffects(call *ast.CallExpr, stats map[string]*Stats) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	baseVar := common.GetVarName(sel.X)
	if baseVar == "?" {
		return false
	}

	receiverType := baseTypeNameFromType(ma.typesInfo.TypeOf(sel.X))
	if receiverType == "" {
		return false
	}

	callee := ma.receiverMethods[receiverType][sel.Sel.Name]
	if callee == nil || callee.Body == nil || callee == ma.function {
		return false
	}

	calleeReceiver := common.ReceiverName(callee)
	if calleeReceiver == "" {
		return false
	}

	changed := false

	for mutexName := range ma.mutexNames {
		relativePath, ok := relativeMutexPath(mutexName, baseVar)
		if !ok {
			continue
		}

		simulated := ma.simulateMethodEffect(callee, calleeReceiver+"."+relativePath, false, stats[mutexName])
		if simulated == nil {
			continue
		}

		stats[mutexName] = simulated
		changed = true
	}

	for rwMutexName := range ma.rwMutexNames {
		relativePath, ok := relativeMutexPath(rwMutexName, baseVar)
		if !ok {
			continue
		}

		simulated := ma.simulateMethodEffect(callee, calleeReceiver+"."+relativePath, true, stats[rwMutexName])
		if simulated == nil {
			continue
		}

		stats[rwMutexName] = simulated
		changed = true
	}

	return changed
}

// removeFirstLockPos removes the first lock position from the list
func (s *Stats) removeFirstLockPos() {
	if len(s.lockPos) > 0 {
		s.lockPos = s.lockPos[1:]
	}
}

// removeFirstRLockPos removes the first rlock position from the list
func (s *Stats) removeFirstRLockPos() {
	if len(s.rlockPos) > 0 {
		s.rlockPos = s.rlockPos[1:]
	}
}

func (s *Stats) removeFirstBorrowedUnlockPos() {
	if len(s.borrowedUnlockPos) > 0 {
		s.borrowedUnlockPos = s.borrowedUnlockPos[1:]
	}
}

func (s *Stats) removeFirstBorrowedRUnlockPos() {
	if len(s.borrowedRUnlockPos) > 0 {
		s.borrowedRUnlockPos = s.borrowedRUnlockPos[1:]
	}
}
