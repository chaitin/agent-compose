//go:build linux && cgo && microsandboxcgo

package driver

import "sync"

type microsandboxKeyedLocks struct {
	mu    sync.Mutex
	locks map[string]*microsandboxKeyedLock
}

type microsandboxKeyedLock struct {
	mu   sync.Mutex
	refs int
}

func (l *microsandboxKeyedLocks) lock(key string) func() {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*microsandboxKeyedLock)
	}
	entry := l.locks[key]
	if entry == nil {
		entry = &microsandboxKeyedLock{}
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
