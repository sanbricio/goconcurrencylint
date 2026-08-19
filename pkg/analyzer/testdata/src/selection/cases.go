// Package selection is the baseline fixture: with no -checks flag, every check
// reports as usual. The selectionfiltered and selectiontests siblings hold the
// same violations under a flag.
package selection

import "sync"

// Mutex violation: GCL1001.
func LockWithoutUnlock() {
	var mu sync.Mutex
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
}

// Pool violation: GCL5001, the check most likely to be disabled in the wild.
func PutNonPointer() {
	var p sync.Pool
	buf := make([]byte, 8)
	p.Put(buf) // want "sync.Pool.Put stores non-pointer value"
}
