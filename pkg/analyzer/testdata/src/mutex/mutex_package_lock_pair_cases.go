package mutex

import "sync"

// A package can split one of its global locks across two plain functions that
// callers use as a pair, the way a type does with two methods:
//
//	func acquireTable() { table.mu.Lock(); ... }
//	func releaseTable() { table.mu.Unlock() }

var pairedTable struct {
	mu sync.Mutex
	m  map[string]int
}

// acquirePairedTable must be CLEAN: it does its own work after taking the lock,
// and its callers release with releasePairedTable.
func acquirePairedTable() {
	pairedTable.mu.Lock()
	if pairedTable.m == nil {
		pairedTable.m = map[string]int{}
	}
}

// releasePairedTable must be CLEAN.
func releasePairedTable() {
	pairedTable.mu.Unlock()
}

// GoodPairedTableDefer must be CLEAN.
func GoodPairedTableDefer(key string) int {
	acquirePairedTable()
	defer releasePairedTable()
	return pairedTable.m[key]
}

// GoodPairedTableExplicit must be CLEAN.
func GoodPairedTableExplicit(key string, v int) {
	acquirePairedTable()
	pairedTable.m[key] = v
	releasePairedTable()
}

var pairedGate sync.RWMutex

// takePairedGate and givePairedGate must be CLEAN: nothing but the lock call,
// used together by GoodPairedGate.
func takePairedGate() {
	pairedGate.Lock()
}

func givePairedGate() {
	pairedGate.Unlock()
}

// GoodPairedGate must be CLEAN.
func GoodPairedGate(work func() error) error {
	takePairedGate()
	if err := work(); err != nil {
		givePairedGate()
		return err
	}
	givePairedGate()
	return nil
}

var unpairedCounter struct {
	mu sync.Mutex
	n  int
}

// takeUnpairedCounter must be flagged: it does its own work, and its only
// caller never calls the release half.
func takeUnpairedCounter() {
	unpairedCounter.mu.Lock() // want "mutex 'unpairedCounter.mu' is locked but not unlocked"
	unpairedCounter.n++
}

func dropUnpairedCounter() {
	unpairedCounter.mu.Unlock()
}

// BadUnpairedCounterCaller only takes the lock.
func BadUnpairedCounterCaller() int {
	takeUnpairedCounter()
	return unpairedCounter.n
}

var releasedFirst sync.Mutex

// lockReleasedFirst must be flagged: its only caller runs the release half
// before it, never after.
func lockReleasedFirst() {
	releasedFirst.Lock() // want "mutex 'releasedFirst' is locked but not unlocked"
}

// unlockReleasedFirst must be flagged: no caller takes the lock before it.
func unlockReleasedFirst() {
	releasedFirst.Unlock() // want "mutex 'releasedFirst' is unlocked but not locked"
}

// BadReleasedFirstCaller uses the halves in the wrong order.
func BadReleasedFirstCaller() {
	unlockReleasedFirst()
	lockReleasedFirst()
}

var conditionalTable sync.Mutex

// lockConditionalTable must be flagged: the lock is taken on one path only, so
// it is not one half of a pair.
func lockConditionalTable(ok bool) {
	if ok {
		conditionalTable.Lock() // want "mutex 'conditionalTable' is locked but not unlocked"
	}
}

func unlockConditionalTable() {
	conditionalTable.Unlock() // want "mutex 'conditionalTable' is unlocked but not locked"
}

// BadConditionalTableCaller pairs them anyway.
func BadConditionalTableCaller(ok bool) {
	lockConditionalTable(ok)
	unlockConditionalTable()
}
