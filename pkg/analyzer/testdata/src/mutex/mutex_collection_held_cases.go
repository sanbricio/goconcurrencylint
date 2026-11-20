package mutex

import "sync"

// Locks held through a collection are paired by walking it, not by a name the
// function can follow: taken on an element that is then stored, or taken on
// every element of a collection that is handed back or walked again to release
// them.

type heldShard struct {
	mu    sync.RWMutex
	items map[int]*heldItem
}

type heldItem struct {
	mu sync.Mutex
	n  int
}

type heldStore struct {
	shards []*heldShard
}

// GoodLockStoredElementsReleaseByWalking must be CLEAN.
func (s *heldStore) GoodLockStoredElementsReleaseByWalking(id int) {
	var lockedShards []*heldShard
	var lockedItems []*heldItem
	unlockAll := func() {
		for _, it := range lockedItems {
			it.mu.Unlock()
		}
		for _, sh := range lockedShards {
			sh.mu.Unlock()
		}
	}
	defer unlockAll()
	for _, shard := range s.shards {
		shard.mu.Lock()
		if item, ok := shard.items[id]; ok {
			item.mu.Lock()
			lockedShards = append(lockedShards, shard)
			lockedItems = append(lockedItems, item)
		} else {
			shard.mu.Unlock()
		}
	}
}

// rlockEveryShard must be CLEAN: it hands back the collection whose elements
// it read-locked.
func (s *heldStore) rlockEveryShard() []*heldShard {
	for _, sh := range s.shards {
		sh.mu.RLock()
	}
	return s.shards
}

// GoodReleaseEveryElement must be CLEAN: the elements were locked by the helper.
func (s *heldStore) GoodReleaseEveryElement() {
	shards := s.rlockEveryShard()
	for _, sh := range shards {
		sh.mu.RUnlock()
	}
}

// GoodLockIntoMap must be CLEAN: each locked item is kept in the returned map.
func (s *heldStore) GoodLockIntoMap(ids []int) map[int]*heldItem {
	held := make(map[int]*heldItem)
	for _, id := range ids {
		item := s.shards[0].items[id]
		item.mu.Lock()
		held[id] = item
	}
	return held
}

// BadLockEveryElementNeverReleased must be flagged: the elements are neither
// kept anywhere nor released.
func (s *heldStore) BadLockEveryElementNeverReleased() {
	for _, shard := range s.shards {
		shard.mu.Lock() // want "rwmutex 'shard.mu' is locked but not unlocked"
		_ = shard.items
	}
}
