package waitgroup

import "sync"

// Recursive helpers reached from a goroutine that uses a package-level
// WaitGroup. The package-level fallback in the interprocedural Done walk asks
// "could this callee manage recursionWG?" for every call, so a callee that
// calls itself (directly or through another function) used to recurse until the
// runtime killed the whole process with "fatal error: stack overflow". These
// cases only have to finish; the diagnostics they expect are the ordinary ones.

var recursionWG sync.WaitGroup

func recurseForever() { recurseForever() }

func walkTree(depth int) {
	if depth == 0 {
		return
	}
	walkTree(depth - 1)
	walkTree(depth - 1)
}

func pingHelper(n int) {
	if n > 0 {
		pongHelper(n - 1)
	}
}

func pongHelper(n int) {
	if n > 0 {
		pingHelper(n - 1)
	}
}

// pingDone and pongDone recurse into each other and only pingDone releases the
// WaitGroup, so Done is reachable from either one through the cycle.
func pingDone(n int) {
	if n > 0 {
		pongDone(n - 1)
		return
	}
	recursionWG.Done()
}

func pongDone(n int) {
	pingDone(n)
}

func GoodDeferredDoneAroundSelfRecursiveHelper() {
	recursionWG.Add(1)
	go func() {
		defer recursionWG.Done()
		recurseForever()
	}()
	recursionWG.Wait()
}

func GoodDeferredDoneAroundBranchingRecursion() {
	recursionWG.Add(1)
	go func() {
		defer recursionWG.Done()
		walkTree(3)
	}()
	recursionWG.Wait()
}

func GoodDeferredDoneAroundMutualRecursion() {
	recursionWG.Add(1)
	go func() {
		defer recursionWG.Done()
		pingHelper(4)
	}()
	recursionWG.Wait()
}

func GoodDoneReachedThroughMutualRecursion() {
	recursionWG.Add(1)
	go pongDone(2)
	recursionWG.Wait()
}

func GoodDoneReachedThroughMutualRecursionInClosure() {
	recursionWG.Add(1)
	go func() {
		pingDone(2)
	}()
	recursionWG.Wait()
}
