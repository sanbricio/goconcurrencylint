package mutex

import (
	"sync"
	"time"
)

// Embedded methods provide the matching lock operation.
type embeddedRWHolder struct {
	sync.RWMutex
}

func (e *embeddedRWHolder) UnlockIgnoreTime() {
	e.RWMutex.Unlock()
}

type embeddedMutexHolder struct {
	sync.Mutex
}

func (e *embeddedMutexHolder) UnlockIgnoreTime() {
	e.Mutex.Unlock()
}

// The promoted Unlock method provides the matching release.
type embeddedTimedMutex struct {
	sync.Mutex
	acquireDuration time.Duration
}

func (e *embeddedTimedMutex) Lock() {
	start := time.Now()
	e.Mutex.Lock()
	e.acquireDuration += time.Since(start)
}

// A non-wrapper method must still be reported.
type embeddedRefresher struct {
	sync.Mutex
	count int
}

func (e *embeddedRefresher) Refresh() {
	e.Mutex.Lock() // want "mutex 'e.Mutex' is locked but not unlocked"
	e.count++
}

// Named fields do not promote matching methods.
type embeddedNamedFieldHolder struct {
	mu sync.Mutex
}

func (e *embeddedNamedFieldHolder) Unlock() {
	e.mu.Unlock() // want "mutex 'e.mu' is unlocked but not locked"
}
