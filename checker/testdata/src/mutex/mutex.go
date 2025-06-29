package mutex

import "sync"

// Incorrect: Lock without Unlock
func badMutex1() {
	var mu sync.Mutex
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
}

// Correct: Lock followed by Unlock
func goodMutex1() {
	var mu sync.Mutex
	mu.Lock()
	mu.Unlock()
}

// Correct: Lock/Unlock inside goroutine
func goodMutex2() {
	var mu sync.Mutex
	go func() {
		mu.Lock()
		defer mu.Unlock()
	}()
}

// Incorrect: Lock in one branch, no Unlock in another
func badMutex2() {
	var mu sync.Mutex
	cond := true
	if cond {
		mu.Lock() // want "mutex 'mu' is locked but not unlocked"
	}
	// Unlock might be skipped
}

// Correct: Lock/Unlock in both branches
func goodMutex3() {
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

// Incorrect: Multiple Locks, fewer Unlocks
func badMutex3() {
	var mu sync.Mutex
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
	mu.Unlock()
}

// Correct: Multiple Locks and Unlocks
func goodMutex4() {
	var mu sync.Mutex
	mu.Lock()
	mu.Unlock()
	mu.Lock()
	mu.Unlock()
}

// Incorrect: Lock in goroutine, Unlock never called due to deadlock
func badMutexWeird1() {
	var mu sync.Mutex
	ch := make(chan struct{})
	go func() {
		mu.Lock() // want "mutex 'mu' is locked but not unlocked"
		<-ch      // goroutine queda bloqueada, nunca hace Unlock
	}()
	// ch nunca recibe, Unlock nunca se llama
}

// Incorrect: Lock/Unlock en defer, pero panic antes del Lock
func badMutexWeird2() {
	var mu sync.Mutex
	defer mu.Unlock() // want "mutex 'mu' is locked but not unlocked"
	panic("fail before lock")
	mu.Lock()
}

// Correct: Lock/Unlock con recover en defer
func goodMutexRecover() {
	var mu sync.Mutex
	mu.Lock()
	defer func() {
		if r := recover(); r != nil {
			mu.Unlock()
		}
	}()
	panic("fail")
}
