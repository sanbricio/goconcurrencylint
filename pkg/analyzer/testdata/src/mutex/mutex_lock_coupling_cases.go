package mutex

import "sync"

// A loop that walks a linked structure hand over hand locks the next node
// while it still holds the current one, then moves its cursor onto it: one
// spelling names a different lock on every pass.

type coupledNode struct {
	mu     sync.Mutex
	next   *coupledNode
	parent *coupledNode
	val    int
}

// GoodWalkHandOverHand must be CLEAN: y.mu is released as the lock taken as
// n.mu one pass earlier.
func GoodWalkHandOverHand(head *coupledNode) int {
	head.mu.Lock()
	y := head
	sum := 0
	for y.next != nil {
		n := y.next
		n.mu.Lock()
		sum += n.val
		y.mu.Unlock()
		y = n
	}
	y.mu.Unlock()
	return sum
}

// GoodClimbHandOverHand must be CLEAN: the parent is locked before the child
// is released and the cursor climbs onto it.
func GoodClimbHandOverHand(i *coupledNode) {
	i.mu.Lock()
	for i.parent != nil && i.val == 0 {
		parent := i.parent
		parent.mu.Lock()
		parent.val--
		i.mu.Unlock()
		i = parent
	}
	i.mu.Unlock()
}

// BadTraversalForgetsUnlock must be flagged: each node is released before the
// cursor moves, so the names stay stable and the early return leaks.
func BadTraversalForgetsUnlock(n *coupledNode) int {
	sum := 0
	for n != nil {
		n.mu.Lock() // want "mutex 'n.mu' is locked but not unlocked"
		if n.val < 0 {
			return sum
		}
		sum += n.val
		n.mu.Unlock()
		n = n.next
	}
	return sum
}

type flagFirstGuard struct {
	mu sync.Mutex
	n  int
}

// GoodFlagSetBeforeLock must be CLEAN: the flag is set right before the lock,
// which records the acquisition just as well as right after.
func (g *flagFirstGuard) GoodFlagSetBeforeLock(steps []int) {
	held := false
	defer func() {
		if held {
			g.mu.Unlock()
		}
	}()
	for _, s := range steps {
		if s > 0 && !held {
			held = true
			g.mu.Lock()
		}
		g.n += s
	}
}

// BadFlagSetAwayFromLock must be flagged: work runs between the flag and the
// lock, so the flag does not record the acquisition.
func (g *flagFirstGuard) BadFlagSetAwayFromLock(v int) {
	held := false
	defer func() { // want "mutex 'g.mu' has defer unlock but no corresponding lock"
		if held {
			g.mu.Unlock()
		}
	}()
	if v > 0 {
		held = true
		g.n = v
		g.mu.Lock() // want "mutex 'g.mu' is locked but not unlocked in if"
	}
}
