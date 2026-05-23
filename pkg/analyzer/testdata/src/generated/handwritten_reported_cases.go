package generated

import "sync"

// No generated header: misuses must still be reported (skip is per-file).

func BadHandwrittenLockWithoutUnlock() {
	var mu sync.Mutex
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
}

func BadHandwrittenAddWithoutDone() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
}
