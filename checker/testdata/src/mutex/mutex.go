package mutex

import "sync"

// ========== POSITIVE CASES (Correct usage) ==========

// Level 1: Basic correct usage
func basicLockUnlock() {
	var mu sync.Mutex
	mu.Lock()
	mu.Unlock()
}

func basicDeferUnlock() {
	var mu sync.Mutex
	mu.Lock()
	defer mu.Unlock()
}

// Level 2: Multiple operations
func multipleLockUnlock() {
	var mu sync.Mutex
	mu.Lock()
	mu.Unlock()
	mu.Lock()
	mu.Unlock()
}

// Level 3: Conditional branches
func conditionalLockBothBranches() {
	var mu sync.Mutex
	cond := true
	if cond {
		mu.Lock()
		defer mu.Unlock()
	} else {
		mu.Lock()
		defer mu.Unlock()
	}
}

// Level 4: Goroutines
func goroutineLockUnlock() {
	var mu sync.Mutex
	go func() {
		mu.Lock()
		defer mu.Unlock()
	}()
}

// Level 5: Error handling with recover
func recoverWithUnlock() {
	var mu sync.Mutex
	mu.Lock()
	defer func() {
		if r := recover(); r != nil {
			mu.Unlock()
		}
	}()
	panic("fail")
}

// ========== NEGATIVE CASES (Incorrect usage) ==========

// Level 1: Basic errors
func simpleLockWithoutUnlock() {
	var mu sync.Mutex
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
}

func simpleUnlockWithoutLock() {
	var mu sync.Mutex
	mu.Unlock() // want "mutex 'mu' is unlocked but not locked"
}

func unlockWithoutPriorLock() {
	var mu sync.Mutex
	// Some other operations...
	mu.Unlock() // want "mutex 'mu' is unlocked but not locked"
}

// Level 2: Imbalanced operations
func imbalancedLockUnlock() {
	var mu sync.Mutex
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
	mu.Lock()
	mu.Unlock()
}

// Level 3: Conditional branch errors
func conditionalLockMissingUnlock() {
	var mu sync.Mutex
	cond := true
	if cond {
		mu.Lock() // want "mutex 'mu' is locked but not unlocked"
	}
	// Unlock might be skipped
}

func conditionalLockMissingLock() {
	var mu sync.Mutex
	cond := true
	if cond {
		mu.Unlock() // want "mutex 'mu' is locked but not unlocked"
	}
	// Unlock might be skipped
}

func conditionalOneBranchMissingUnlock() {
	var mu sync.Mutex
	cond := true
	if cond {
		mu.Lock() // want "mutex 'mu' is locked but not unlocked"
	} else {
		mu.Lock()
		defer mu.Unlock()
	}
}

// Level 4: Complex control flow errors
func deferUnlockWithoutLock() {
	var mu sync.Mutex
	defer mu.Unlock() // want "mutex 'mu' has defer unlock but no corresponding lock"
	panic("fail before lock")
	mu.Lock()
}

// Level 5: Goroutine-related errors
func goroutineDeadlock() {
	var mu sync.Mutex
	ch := make(chan struct{})
	go func() {
		mu.Lock() // want "mutex 'mu' is locked but not unlocked"
		<-ch      // This will block forever, so unlock never happens
	}()
}
