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
	c := &Checker{
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
	c.loopCarry = newLoopCarryAnalyzer(c.mutexNames, c.rwMutexNames, cf, errorCollector, term)
	return c
}

func (c *Checker) AnalyzeFunction(fn *ast.FuncDecl) {
	c.funcAnalysis = newFuncAnalysis(fn)
	c.tryLock = newTryLockTracker(c.mutexNames, c.rwMutexNames, c.commentFilter, c.errorCollector)
	c.wrapper = newWrapperResolver(c.receiverMethods, c.function, c.rawBodyEffects)
	c.lifecycle = newLifecycleResolver(c.receiverMethods, c.functions, c.typesInfo, c.explicitTransferCache, c.function)
	c.panicDetector = newLockedPanicDetector(c.mutexNames, c.rwMutexNames, c.typesInfo, c.errorCollector, c.rawBodyEffects)
	c.flagGuardedMutexes = c.detectFlagGuardedReleases(fn)
	c.stats = initialStats(c.mutexNames, c.rwMutexNames)
	lockOrder := newLockOrderDetector(c.mutexNames, c.rwMutexNames, c.commentFilter, c.typesInfo, c.errorCollector)
	lockOrder.check(fn.Body)
	finalStats := c.analyzeBlock(fn.Body, c.stats)
	c.tryLock.reportUnchecked()
	c.reportUnmatchedLocks(finalStats)
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
func (c *Checker) functionIsParameterUnlockHelper(varName string, acquireMethods []string) bool {
	if c.function == nil || c.function.Body == nil {
		return false
	}
	if !c.varRootIsFunctionParameter(varName) {
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
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
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
func (c *Checker) varRootIsFunctionParameter(varName string) bool {
	if c.function == nil || c.function.Type == nil || c.function.Type.Params == nil {
		return false
	}
	base := varName
	if before, _, ok := strings.Cut(varName, "."); ok {
		base = before
	}
	for _, field := range c.function.Type.Params.List {
		for _, name := range field.Names {
			if name.Name == base {
				return true
			}
		}
	}
	return false
}

func (c *Checker) simulateMethodEffect(fn *ast.FuncDecl, varName string, isRWMutex bool, initial *Stats) *Stats {
	if fn == nil || fn.Body == nil {
		return nil
	}

	stack := c.simulationStack
	if stack == nil {
		stack = make(map[methodSimulationKey]bool)
		c.simulationStack = stack
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

	sub := newSimulationFuncAnalysis(fn, stack, c.localFuncStack)
	simulated := c.forkForSimulation(sub, mutexNames, rwMutexNames)

	start := map[string]*Stats{varName: cloneStats(initial)}
	final := simulated.analyzeBlock(fn.Body, start)
	simulated.applyFunctionExitDefers(final, start)
	return cloneStats(final[varName])
}

func (c *Checker) applyLocalFunctionLiteralLifecycleEffects(call *ast.CallExpr, stats map[string]*Stats) bool {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}

	fnlit := c.localFunctionLiteralBefore(ident.Name, call.Pos())
	if fnlit == nil || fnlit.Body == nil {
		return false
	}

	stack := c.localFuncStack
	if stack == nil {
		stack = make(map[*ast.FuncLit]bool)
		c.localFuncStack = stack
	}
	if stack[fnlit] {
		return false
	}

	stack[fnlit] = true
	defer delete(stack, fnlit)

	sub := newSimulationFuncAnalysis(c.function, c.simulationStack, stack)
	simulated := c.forkForSimulation(sub, c.mutexNames, c.rwMutexNames)

	baseline := cloneStatsMap(stats)
	final := simulated.analyzeBlock(fnlit.Body, baseline)
	simulated.applyFunctionExitDefers(final, baseline)
	copyStatsMap(stats, final)
	return true
}

func (c *Checker) localFunctionLiteralBefore(name string, before token.Pos) *ast.FuncLit {
	if c.function == nil || c.function.Body == nil {
		return nil
	}

	var found *ast.FuncLit
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
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

func (c *Checker) applyFunctionExitDefers(stats, baseline map[string]*Stats) {
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

func (c *Checker) applyLocalMethodLifecycleEffects(call *ast.CallExpr, stats map[string]*Stats) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	baseVar := common.GetVarName(sel.X)
	if baseVar == "?" {
		return false
	}

	receiverType := baseTypeNameFromType(c.typesInfo.TypeOf(sel.X))
	if receiverType == "" {
		return false
	}

	callee := c.receiverMethods[receiverType][sel.Sel.Name]
	if callee == nil || callee.Body == nil || callee == c.function {
		return false
	}

	calleeReceiver := common.ReceiverName(callee)
	if calleeReceiver == "" {
		return false
	}

	changed := false

	for mutexName := range c.mutexNames {
		relativePath, ok := relativeMutexPath(mutexName, baseVar)
		if !ok {
			continue
		}

		simulated := c.simulateMethodEffect(callee, calleeReceiver+"."+relativePath, false, stats[mutexName])
		if simulated == nil {
			continue
		}

		stats[mutexName] = simulated
		changed = true
	}

	for rwMutexName := range c.rwMutexNames {
		relativePath, ok := relativeMutexPath(rwMutexName, baseVar)
		if !ok {
			continue
		}

		simulated := c.simulateMethodEffect(callee, calleeReceiver+"."+relativePath, true, stats[rwMutexName])
		if simulated == nil {
			continue
		}

		stats[rwMutexName] = simulated
		changed = true
	}

	return changed
}

// applyLocalFunctionCallLifecycleEffects models a call to a top-level (non-method)
// function that releases a lock held on one of its arguments, e.g.
// `releaseShardLock(shard)` whose body runs `shard.mu.Unlock()`. Without this the
// caller appears to leak the lock on the delegated path. It mirrors
// applyLocalMethodLifecycleEffects but maps the locked variable through the
// argument position to the callee's parameter name.
func (c *Checker) applyLocalFunctionCallLifecycleEffects(call *ast.CallExpr, stats map[string]*Stats) bool {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}

	callee := c.topLevelFunctionNamed(ident.Name)
	if callee == nil || callee.Body == nil || callee == c.function {
		return false
	}

	changed := false
	applyAll := func(names map[string]bool, isRWMutex bool) {
		for name := range names {
			if c.applyArgumentReleaseEffect(call, callee, name, isRWMutex, stats) {
				changed = true
			}
		}
	}

	applyAll(c.mutexNames, false)
	applyAll(c.rwMutexNames, true)
	return changed
}

// applyArgumentReleaseEffect simulates the callee's effect on a single mutex that
// the caller passes in as an argument, remapping the caller's variable to the
// matching parameter name inside the callee.
func (c *Checker) applyArgumentReleaseEffect(call *ast.CallExpr, callee *ast.FuncDecl, mutexName string, isRWMutex bool, stats map[string]*Stats) bool {
	base, suffix, ok := splitBaseAndSuffix(mutexName)
	if !ok {
		return false
	}

	paramName, ok := calleeParamNameForArg(call, callee, base)
	if !ok {
		return false
	}

	simulated := c.simulateMethodEffect(callee, paramName+"."+suffix, isRWMutex, stats[mutexName])
	if simulated == nil {
		return false
	}

	stats[mutexName] = simulated
	return true
}

// topLevelFunctionNamed returns the package-level (non-method) function with the
// given name, or nil when there is none.
func (c *Checker) topLevelFunctionNamed(name string) *ast.FuncDecl {
	for _, fn := range c.functions {
		if fn != nil && fn.Recv == nil && fn.Name != nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// calleeParamNameForArg returns the callee parameter name that receives the
// argument whose variable name equals base. It returns false when the argument
// is absent or the matching parameter is unnamed or blank.
func calleeParamNameForArg(call *ast.CallExpr, callee *ast.FuncDecl, base string) (string, bool) {
	if callee.Type == nil {
		return "", false
	}

	argIndex := -1
	for i, arg := range call.Args {
		if common.GetVarName(arg) == base {
			argIndex = i
			break
		}
	}
	if argIndex < 0 {
		return "", false
	}

	names := flattenParamNames(callee.Type.Params)
	if argIndex >= len(names) || names[argIndex] == "" {
		return "", false
	}
	return names[argIndex], true
}

// flattenParamNames returns one entry per positional parameter, using "" for
// unnamed or blank ("_") parameters so the slice index lines up with call args.
func flattenParamNames(params *ast.FieldList) []string {
	if params == nil {
		return nil
	}

	var names []string
	for _, field := range params.List {
		if len(field.Names) == 0 {
			names = append(names, "")
			continue
		}
		for _, name := range field.Names {
			if name == nil || name.Name == "_" {
				names = append(names, "")
				continue
			}
			names = append(names, name.Name)
		}
	}
	return names
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
