package waitgroup

// validateUsage performs validation checks on collected statistics
func (c *Checker) validateUsage(stats map[string]*Stats) {
	iteration := c.iteration
	if iteration == nil {
		iteration = newIterationEstimator(c.function, c.typesInfo, c.commentFilter)
	}

	balance := newBalanceValidator(balanceValidatorConfig{
		function:                     c.function,
		waitGroupNames:               c.waitGroupNames,
		localWaitGroupNames:          c.localWaitGroupNames,
		commentFilter:                c.commentFilter,
		reporter:                     c.errorCollector,
		typesInfo:                    c.typesInfo,
		escape:                       c.escape,
		isInGoroutine:                c.isInGoroutine,
		isNodeInGoroutine:            c.isNodeInGoroutine,
		callInvokesDone:              c.worker.callInvokesDone,
		goroutineDoneInfo:            c.goroutineDoneInfo,
		isSimpleDeferDone:            c.worker.isSimpleDeferDone,
		findRelatedAddCall:           c.worker.findRelatedAddCall,
		hasUnreachableDone:           c.worker.hasUnreachableDone,
		waitInEarlyExitBranch:        c.worker.waitInEarlyExitBranch,
		estimateForIterations:        iteration.estimateForIterations,
		estimateForIterationsKnown:   iteration.estimateForIterationsKnown,
		estimateRangeIterations:      iteration.estimateRangeIterations,
		estimateRangeIterationsKnown: iteration.estimateRangeIterationsKnown,
	})
	goroutines := newGoroutineInspector(
		c.waitGroupNames,
		c.commentFilter,
		c.errorCollector,
		c.worker.deferInvokesDone,
		c.worker.callInvokesDone,
		c.goroutineDoneInfo,
		balance.goroutineOnlyWaitsOnWaitGroup,
		c.analyzeDoneCallsWithVisited,
		c.isInGoroutine,
		c.typesInfo,
		balance.isInMainFunctionFlow,
		c.worker.isBuiltinPanic,
	)
	goroutines.checkAddInsideGoroutine(c.function)
	c.worker.checkDoneNotDeferredInWorker()
	balance.checkLiteralAddLoopGoroutineMismatch(stats)
	balance.checkWaitWithoutAdd(stats)
	goroutines.checkMultipleDoneSameWorkerBranch(c.function)
	goroutines.checkNestedWaitGroupDeadlock(c.function)
	balance.checkAddAfterWait(stats)
	balance.checkWaitBeforeDoneSameGoroutine(stats)
	goroutines.checkWaitAndDoneInSameGoroutine(c.function)
	goroutines.checkDoneOutsideWorkerGoroutine(c.function)
	goroutines.checkWaitGroupGoPanic(c.function)
	balance.checkLoopAddDoneBalance()
	balance.checkUnreachableDone()
	balance.checkWaitGroupBalance(stats)
}
