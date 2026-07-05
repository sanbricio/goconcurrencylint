package synccopy

import "sync"

// ========== sync-primitive-copy for sync.Cond / sync.Pool / sync.Map (GCL9001) ==========
//
// These primitives contain a noCopy / internal state that must not be duplicated
// after first use. go vet's copylocks flags the same copies, so this is a
// zero-false-positive extension of the copy check to the rest of the sync
// package. A *pointer* to any of them may be shared and copied freely; only a
// by-value copy is a bug.

// --- sync.Cond ---

func takesCondByValue(c sync.Cond) {} // want "cond 'c' is copied by value"

func BadCopyCond() {
	var c sync.Cond
	cp := c // want "cond 'c' is copied by value"
	_ = cp
}

// Good: a *sync.Cond pointer is passed around, never the value.
func GoodCondPointer() {
	var mu sync.Mutex
	c := sync.NewCond(&mu)
	usesCondPointer(c)
}

func usesCondPointer(c *sync.Cond) { _ = c }

// --- sync.Pool ---

func takesPoolByValue(p sync.Pool) {} // want "pool 'p' is copied by value"

func BadCopyPool() {
	var p sync.Pool
	cp := p // want "pool 'p' is copied by value"
	_ = cp
}

// Good: the pool is used in place; method calls do not copy it.
func GoodPoolInPlace() {
	var p sync.Pool
	v := 1
	p.Put(&v) // pointer-shaped: no GCL5001
	_ = p.Get()
}

// --- sync.Map ---

func takesMapByValue(m sync.Map) {} // want "sync.Map 'm' is copied by value"

func BadCopyMap() {
	var m sync.Map
	cp := m // want "sync.Map 'm' is copied by value"
	_ = cp
}

// Good: the map is used in place.
func GoodMapInPlace() {
	var m sync.Map
	m.Store("k", 1)
	_, _ = m.Load("k")
}

// --- a struct that holds one of them by value ---

type resources struct {
	pool sync.Pool
}

func takesResourcesByValue(r resources) {} // want "struct 'r' containing pool is copied by value"

// Good: hold and pass the struct by pointer.
func takesResourcesByPointer(r *resources) { _ = r }
