package driver

import (
	"context"
	"sync"
)

// sandboxKeyedLocks serializes per-sandbox lifecycle operations (create and
// remove) keyed by sandbox ID. It is the shared mechanism the VM runtimes use to
// satisfy design §3.4's "no duplicate spawn after daemon restart" requirement:
// concurrent creates for one sandbox collapse into one, and a remove waits for
// an in-flight create of the same sandbox instead of interleaving with it.
type sandboxKeyedLocks struct {
	mu    sync.Mutex
	locks map[string]*sandboxKeyedLock
}

// sandboxKeyedLock is a one-slot semaphore: acquire holds a token when the lock
// is free, and taking the token acquires the lock. refs counts the goroutines
// that hold or are waiting for this entry so the entry can be dropped once the
// last one leaves.
type sandboxKeyedLock struct {
	acquire chan struct{}
	refs    int
}

// lock blocks until the per-key lock is held, ignoring cancellation. It is for
// the create path, whose context deliberately survives RPC cancellation.
func (l *sandboxKeyedLocks) lock(key string) func() {
	unlock, _ := l.lockContext(context.Background(), key)
	return unlock
}

// lockContext blocks until the per-key lock is held or ctx is done. On ctx
// cancellation it drops its waiting reference without acquiring and returns
// ctx.Err(), so a remove that is waiting behind a stuck create can still be
// cancelled by its caller.
func (l *sandboxKeyedLocks) lockContext(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*sandboxKeyedLock)
	}
	entry := l.locks[key]
	if entry == nil {
		entry = &sandboxKeyedLock{acquire: make(chan struct{}, 1)}
		entry.acquire <- struct{}{} // one free token
		l.locks[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	select {
	case <-entry.acquire:
	case <-ctx.Done():
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
		return nil, ctx.Err()
	}

	return func() {
		entry.acquire <- struct{}{} // release the token
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}, nil
}
