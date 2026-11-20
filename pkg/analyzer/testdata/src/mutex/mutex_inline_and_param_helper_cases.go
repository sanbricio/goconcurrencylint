package mutex

import (
	"errors"
	"sync"
)

type acquireLitIndex struct {
	drop sync.RWMutex
	ok   bool
}

type acquireLitDB struct {
	mu      sync.RWMutex
	indices map[string]*acquireLitIndex
}

// GoodInlineLiteralAcquireOrFail must be CLEAN: the literal holds the index
// lock exactly when it reports true.
func (db *acquireLitDB) GoodInlineLiteralAcquireOrFail(name string) error {
	var index *acquireLitIndex
	if ok := func() bool {
		db.mu.RLock()
		defer db.mu.RUnlock()
		index = db.indices[name]
		if index == nil || !index.ok {
			return false
		}
		index.drop.RLock()
		return true
	}(); !ok {
		return errors.New("not found")
	}
	defer index.drop.RUnlock()
	return nil
}

// BadInlineLiteralKeepsOnFailure must be flagged: the literal also returns
// false holding the lock.
func (db *acquireLitDB) BadInlineLiteralKeepsOnFailure(name string) error {
	var index *acquireLitIndex
	if ok := func() bool {
		index = db.indices[name]
		index.drop.RLock()
		return index.ok
	}(); !ok {
		return errors.New("not ready")
	}
	defer index.drop.RUnlock() // want "rwmutex 'index.drop' has defer runlock but no corresponding rlock"
	return nil
}

type paramBucket struct {
	flush sync.RWMutex
	name  string
}

type paramStore struct {
	mu      sync.Mutex
	buckets map[string]*paramBucket
}

// freezeBucket must be CLEAN: it locks the bucket it is handed and returns
// holding it on success only; every caller releases it.
func (s *paramStore) freezeBucket(name string, b *paramBucket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buckets[name] != b {
		return errors.New("not registered")
	}
	b.flush.Lock()
	s.buckets[name] = nil
	return nil
}

// GoodReleaseFrozenBucket must be CLEAN.
func (s *paramStore) GoodReleaseFrozenBucket(name string, b *paramBucket) error {
	if err := s.freezeBucket(name, b); err != nil {
		return err
	}
	defer b.flush.Unlock()
	b.name = name
	return nil
}

// lockBucketForgotten must be flagged: its only caller never releases.
func (s *paramStore) lockBucketForgotten(b *paramBucket) error {
	if b == nil {
		return errors.New("nil")
	}
	b.flush.Lock() // want "rwmutex 'b.flush' is locked but not unlocked"
	return nil
}

// BadNeverReleasesBucket uses the helper above.
func (s *paramStore) BadNeverReleasesBucket(b *paramBucket) {
	if err := s.lockBucketForgotten(b); err != nil {
		return
	}
	b.name = "x"
}
