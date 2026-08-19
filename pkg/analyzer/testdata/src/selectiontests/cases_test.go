package selectiontests

import "sync"

// Suppressed by -tests=false: no diagnostic expected.
func lockWithoutUnlock() {
	var mu sync.Mutex
	mu.Lock()
}
