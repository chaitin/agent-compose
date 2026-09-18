package sandboxstore

import (
	"context"
	"sync"
	"time"
)

// sandboxIndexRepairs owns failed write-through IDs and the worker that retries
// them. Its mutex protects bookkeeping only; filesystem and database work run
// outside it. Per-ID gates order projection writes without blocking other IDs.
type sandboxIndexRepairs struct {
	mu       sync.Mutex
	pending  map[string]uint64
	sequence uint64
	gates    map[string]*sandboxIndexGate
	wake     chan struct{}
	cancel   context.CancelFunc
	done     chan struct{}
}

type sandboxIndexGate struct {
	token chan struct{}
	users int
}

func (s *Store) startIndexRepairs() {
	ctx, cancel := context.WithCancel(context.Background())
	r := &sandboxIndexRepairs{
		pending: make(map[string]uint64), gates: make(map[string]*sandboxIndexGate),
		wake: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{}),
	}
	s.indexRepairs = r
	go func() {
		defer close(r.done)
		s.runIndexRepairs(ctx)
	}()
}

func (r *sandboxIndexRepairs) stop() {
	r.cancel()
	<-r.done
}

func (r *sandboxIndexRepairs) mark(id string) {
	r.mu.Lock()
	r.sequence++
	r.pending[id] = r.sequence
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *sandboxIndexRepairs) revision(id string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending[id]
}

func (r *sandboxIndexRepairs) clear(id string, revision uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A newer failed update may arrive while a repair reads older metadata.
	// It must remain queued even when that repair succeeds.
	if r.pending[id] == revision {
		delete(r.pending, id)
	}
}

func (r *sandboxIndexRepairs) ids() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.pending))
	for id := range r.pending {
		ids = append(ids, id)
	}
	return ids
}

func (r *sandboxIndexRepairs) acquire(ctx context.Context, id string) (func(), error) {
	r.mu.Lock()
	gate := r.gates[id]
	if gate == nil {
		gate = &sandboxIndexGate{token: make(chan struct{}, 1)}
		r.gates[id] = gate
	}
	gate.users++
	r.mu.Unlock()
	releaseReference := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		gate.users--
		if gate.users == 0 {
			delete(r.gates, id)
		}
	}
	select {
	case gate.token <- struct{}{}:
		return func() { <-gate.token; releaseReference() }, nil
	case <-ctx.Done():
		releaseReference()
		return nil, ctx.Err()
	}
}

func (s *Store) runIndexRepairs(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.indexRepairs.wake:
		}
		delay := time.Second
		for ids := s.indexRepairs.ids(); len(ids) > 0; ids = s.indexRepairs.ids() {
			for _, id := range ids {
				if ctx.Err() != nil {
					return
				}
				if s.indexRepairs.revision(id) == 0 {
					continue
				}
				attemptCtx, cancel := context.WithTimeout(ctx, sandboxCacheWriteTimeout)
				// syncSandboxIndex logs stage timings and pool statistics on failure.
				err := s.syncSandboxIndex(attemptCtx, id, sandboxIndexRefresh)
				cancel()
				if err != nil && ctx.Err() != nil {
					return
				}
			}
			if !waitSandboxIndexRetry(ctx, delay) {
				return
			}
			delay = min(delay*2, 30*time.Second)
		}
	}
}

func waitSandboxIndexRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
