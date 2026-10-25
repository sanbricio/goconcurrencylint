// Package ctxsem stands in for golang.org/x/sync/semaphore: a counting
// semaphore that keeps a mutex of its own, unexported, to guard its counters.
package ctxsem

import "sync"

type Weighted struct {
	mu      sync.Mutex
	size    int64
	current int64
}

func NewWeighted(size int64) *Weighted {
	return &Weighted{size: size}
}

func (w *Weighted) Acquire(n int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.current += n
}

func (w *Weighted) Release(n int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.current -= n
}
