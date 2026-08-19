// Package selectiontests is loaded with -tests=false: the violation below must
// still report, the identical one in cases_test.go must not. The marker below
// is the positive control — without it the test would pass even if the package
// were never analyzed.
package selectiontests

import "sync"

func lockWithoutUnlockInSource() {
	var mu sync.Mutex
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
}
