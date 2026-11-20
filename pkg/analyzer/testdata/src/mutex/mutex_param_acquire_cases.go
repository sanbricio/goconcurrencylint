package mutex

import (
	"context"
	"errors"
	"sync"
)

// A function can take a lock it is handed as bound methods, so one
// implementation serves any lock and either mode:
//
//	if err := acquireOrCancel(ctx, mu.TryRLock, mu.RLock, mu.RUnlock); err != nil {
//		return err
//	}
//	defer mu.RUnlock()

// acquireOrCancel takes the lock through tryLock or lock, and gives it back
// through unlock only when the context ended first.
func acquireOrCancel(ctx context.Context, tryLock func() bool, lock, unlock func()) error {
	if tryLock() {
		return nil
	}
	var (
		locked      = make(chan struct{})
		mu          sync.Mutex
		returned    bool
		maybeUnlock = func() {
			mu.Lock()
			defer mu.Unlock()
			if returned {
				unlock()
			}
			returned = true
		}
	)
	go func() {
		lock()
		close(locked)
		maybeUnlock()
	}()
	select {
	case <-locked:
		return nil
	case <-ctx.Done():
		maybeUnlock()
		return ctx.Err()
	}
}

// runLocked takes the lock and always gives it back before returning.
func runLocked(lock, unlock func(), work func() error) error {
	lock()
	defer unlock()
	return work()
}

type paramLocked struct {
	join   sync.RWMutex
	commit sync.Mutex
	n      int
}

// waitJoin must be CLEAN: it passes on the result of a call that takes the
// read lock on success.
func (p *paramLocked) waitJoin(ctx context.Context) error {
	mu := &p.join
	return acquireOrCancel(ctx, mu.TryRLock, mu.RLock, mu.RUnlock)
}

// GoodReleaseAfterHelper must be CLEAN.
func (p *paramLocked) GoodReleaseAfterHelper(ctx context.Context) {
	if err := p.waitJoin(ctx); err != nil {
		return
	}
	p.n++
	p.join.RUnlock()
}

// GoodReleaseAfterDirectCall must be CLEAN.
func (p *paramLocked) GoodReleaseAfterDirectCall(ctx context.Context) error {
	if err := acquireOrCancel(ctx, p.commit.TryLock, p.commit.Lock, p.commit.Unlock); err != nil {
		return err
	}
	defer p.commit.Unlock()
	p.n++
	return nil
}

// GoodReleaseOnFailureOfSecond must be CLEAN: the read lock the helper took is
// released on the failure arm of the next acquisition, and the success path
// hands both releases to a callback.
func (p *paramLocked) GoodReleaseOnFailureOfSecond(ctx context.Context, send func(func())) {
	if err := p.waitJoin(ctx); err != nil {
		return
	}
	if err := acquireOrCancel(ctx, p.commit.TryLock, p.commit.Lock, p.commit.Unlock); err != nil {
		p.join.RUnlock()
		return
	}
	done := func() {
		p.join.RUnlock()
		p.commit.Unlock()
	}
	send(done)
}

// BadReleaseAfterBalancedCall must be flagged: runLocked already gave the lock
// back.
func (p *paramLocked) BadReleaseAfterBalancedCall() {
	if err := runLocked(p.commit.Lock, p.commit.Unlock, func() error { return nil }); err != nil {
		return
	}
	p.commit.Unlock() // want "mutex 'p.commit' is unlocked but not locked"
}

// BadCallbackRunsOwnSection must be flagged: the callback takes the lock for
// itself, so it does not release the one the function holds.
func (p *paramLocked) BadCallbackRunsOwnSection(send func(func())) error {
	p.commit.Lock() // want "mutex 'p.commit' is locked but not unlocked"
	if p.n > 0 {
		return errors.New("busy")
	}
	send(func() {
		p.commit.Lock()
		p.n++
		p.commit.Unlock()
	})
	return nil
}

// GoodCallbackReleases must be CLEAN: the release travels with the callback.
func (p *paramLocked) GoodCallbackReleases(send func(func())) {
	p.commit.Lock()
	p.n++
	send(func() {
		p.n--
		p.commit.Unlock()
	})
}
