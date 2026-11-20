package mutex

import "sync"

// A method called with the read lock held may trade it for the write lock and
// take the read lock back on return with a deferred RLock: the caller still
// holds the read lock after the call.

type upgradingConfig struct {
	mu   sync.RWMutex
	keys []int
}

// ensureKeysRLocked must be CLEAN: called with mu read-locked, returns with it
// read-locked.
func (c *upgradingConfig) ensureKeysRLocked() {
	if len(c.keys) > 0 {
		return
	}
	c.mu.RUnlock()
	defer c.mu.RLock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.keys) == 0 {
		c.keys = []int{1}
	}
}

// GoodReadAfterUpgradeOnReceiver must be CLEAN.
func (c *upgradingConfig) GoodReadAfterUpgradeOnReceiver() int {
	c.mu.RLock()
	c.ensureKeysRLocked()
	n := len(c.keys)
	c.mu.RUnlock()
	return n
}

// GoodReadAfterUpgradeOnOther must be CLEAN: same call on another value.
func (c *upgradingConfig) GoodReadAfterUpgradeOnOther(other *upgradingConfig) int {
	other.mu.RLock()
	other.ensureKeysRLocked()
	if len(other.keys) == 0 {
		other.mu.RUnlock()
		return 0
	}
	n := len(other.keys)
	other.mu.RUnlock()
	return n
}

// dropReadLock releases the read lock and does not take it back.
func (c *upgradingConfig) dropReadLock() {
	c.keys = nil
	c.mu.RUnlock()
}

// BadReleaseAfterDroppingHelper must be flagged: the helper already gave the
// read lock back.
func (c *upgradingConfig) BadReleaseAfterDroppingHelper() {
	c.mu.RLock()
	c.dropReadLock()
	c.mu.RUnlock() // want "rwmutex 'c.mu' is runlocked but not rlocked"
}

// waitOutsideLocked is called with mu held: it drops it to wait and takes it
// back in a deferred closure.
func (c *upgradingConfig) waitOutsideLocked(wait func()) {
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.keys = nil
	}()
	wait()
}

// GoodReleaseAfterClosureRetake must be CLEAN.
func (c *upgradingConfig) GoodReleaseAfterClosureRetake(wait func()) {
	c.mu.Lock()
	c.waitOutsideLocked(wait)
	c.keys = append(c.keys, 1)
	c.mu.Unlock()
}
