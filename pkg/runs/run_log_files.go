package runs

import (
	"context"
	"sync"

	"github.com/chaitin/agent-compose/pkg/runlog"
)

// runLogFiles serializes opening and closing each path independently. The map
// lock protects only state: recovery, prompt writes and flushes run outside it.
type runLogFiles struct {
	mu   sync.Mutex
	open map[string]*runLogFile
}

type runLogFile struct {
	writer *runlog.Writer
	// busy is non-nil while an opener or closer owns this path. Waiters retry
	// after it closes, so a failed open can be retried and close cannot miss
	// an in-flight open or overlap a replacement writer.
	busy chan struct{}
}

func (f *runLogFiles) writer(ctx context.Context, path string, open func() (*runlog.Writer, error)) (*runlog.Writer, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f.mu.Lock()
		entry := f.open[path]
		if entry != nil && entry.busy != nil {
			busy := entry.busy
			f.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-busy:
				continue
			}
		}
		if entry != nil {
			w := entry.writer
			f.mu.Unlock()
			return w, nil
		}
		busy := make(chan struct{})
		entry = &runLogFile{busy: busy}
		if f.open == nil {
			f.open = make(map[string]*runLogFile)
		}
		f.open[path] = entry
		f.mu.Unlock()

		w, err := open()
		f.mu.Lock()
		if err != nil {
			delete(f.open, path)
		} else {
			entry.writer, entry.busy = w, nil
		}
		close(busy)
		f.mu.Unlock()
		return w, err
	}
}

func (f *runLogFiles) close(ctx context.Context, path string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		f.mu.Lock()
		entry := f.open[path]
		if entry == nil {
			f.mu.Unlock()
			return nil
		}
		if entry.busy != nil {
			busy := entry.busy
			f.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-busy:
				continue
			}
		}
		busy := make(chan struct{})
		entry.busy = busy
		f.mu.Unlock()

		err := entry.writer.Close()
		f.mu.Lock()
		delete(f.open, path)
		close(busy)
		f.mu.Unlock()
		return err
	}
}
