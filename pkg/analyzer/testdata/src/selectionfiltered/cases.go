// Package selectionfiltered is loaded with -checks "all,-GCL5*". The absent
// marker on Put is the assertion.
package selectionfiltered

import "sync"

func LockWithoutUnlock() {
	var mu sync.Mutex
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
}

func PutNonPointer() {
	var p sync.Pool
	buf := make([]byte, 8)
	p.Put(buf) // GCL5001 is disabled for this package: no diagnostic expected.
}
