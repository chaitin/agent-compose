package driver

import "sync"

// sandboxKeyedLocks serializes per-sandbox lifecycle operations (create and
// remove) keyed by sandbox ID. It is the shared mechanism the VM runtimes use to
// satisfy design §3.4's "no duplicate spawn after daemon restart" requirement:
// concurrent creates for one sandbox collapse into one, and a remove waits for
// an in-flight create of the same sandbox instead of interleaving with it.
type sandboxKeyedLocks struct {
	mu    sync.Mutex
	locks map[string]*sandboxKeyedLock
}

type sandboxKeyedLock struct {
	mu   sync.Mutex
	refs int
}

func (l *sandboxKeyedLocks) lock(key string) func() {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*sandboxKeyedLock)
	}
	entry := l.locks[key]
	if entry == nil {
		entry = &sandboxKeyedLock{}
		l.locks[key] = entry
	}
	entry.refs++
	l.mu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}
