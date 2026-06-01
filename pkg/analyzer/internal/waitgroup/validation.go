package waitgroup

// validateUsage performs validation checks on collected statistics
func (wga *Checker) validateUsage(stats map[string]*Stats) {
	iteration := wga.iteration
	if iteration == nil {
		iteration = newIterationEstimator(wga.function, wga.typesInfo, wga.commentFilter)
	}

	balance := newBalanceValidator(balanceValidatorConfig{
		function:                     wga.function,
		waitGroupNames:               wga.waitGroupNames,
		localWaitGroupNames:          wga.localWaitGroupNames,
		commentFilter:                wga.commentFilter,
		reporter:                     wga.errorCollector,
		typesInfo:                    wga.typesInfo,
		escape:                       wga.escape,
		isInGoroutine:                wga.isInGoroutine,
		isNodeInGoroutine:            wga.isNodeInGoroutine,
		callInvokesDone:              wga.callInvokesDone,
		goroutineDoneInfo:            wga.goroutineDoneInfo,
		isSimpleDeferDone:            wga.isSimpleDeferDone,
		findRelatedAddCall:           wga.findRelatedAddCall,
		hasUnreachableDone:           wga.hasUnreachableDone,
		waitInEarlyExitBranch:        wga.waitInEarlyExitBranch,
		estimateForIterations:        iteration.estimateForIterations,
		estimateForIterationsKnown:   iteration.estimateForIterationsKnown,
		estimateRangeIterations:      iteration.estimateRangeIterations,
		estimateRangeIterationsKnown: iteration.estimateRangeIterationsKnown,
	})
	goroutines := newGoroutineInspector(
		wga.waitGroupNames,
		wga.commentFilter,
		wga.errorCollector,
		wga.deferInvokesDone,
		wga.callInvokesDone,
		wga.goroutineDoneInfo,
		balance.goroutineOnlyWaitsOnWaitGroup,
		wga.analyzeDoneCallsWithVisited,
		wga.isInGoroutine,
		wga.typesInfo,
		balance.isInMainFunctionFlow,
		wga.isBuiltinPanic,
	)
	goroutines.checkAddInsideGoroutine(wga.function)
	wga.checkDoneNotDeferredInWorker()
	balance.checkLiteralAddLoopGoroutineMismatch(stats)
	balance.checkWaitWithoutAdd(stats)
	goroutines.checkMultipleDoneSameWorkerBranch(wga.function)
	goroutines.checkNestedWaitGroupDeadlock(wga.function)
	balance.checkAddAfterWait(stats)
	balance.checkWaitBeforeDoneSameGoroutine(stats)
	goroutines.checkWaitAndDoneInSameGoroutine(wga.function)
	goroutines.checkDoneOutsideWorkerGoroutine(wga.function)
	goroutines.checkWaitGroupGoPanic(wga.function)
	balance.checkLoopAddDoneBalance()
	balance.checkUnreachableDone()
	balance.checkWaitGroupBalance(stats)
}
