package runs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

func TestIntegrationRunLogFilesOpenDoesNotBlockOtherPaths(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	files := &runLogFiles{}
	root := t.TempDir()
	cachedPath := filepath.Join(root, "cached", runlog.FileName)
	cached, err := files.writer(ctx, cachedPath, func() (*runlog.Writer, error) { return runlog.Open(cachedPath, runlog.Options{}) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := files.close(context.Background(), cachedPath); err != nil {
			t.Error(err)
		}
	}()

	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	slowPath := filepath.Join(root, "slow", runlog.FileName)
	result := make(chan error, 1)
	go func() {
		_, err := files.writer(ctx, slowPath, func() (*runlog.Writer, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return runlog.Open(slowPath, runlog.Options{})
		})
		result <- err
	}()
	defer func() {
		unblock()
		if err := <-result; err != nil {
			t.Error(err)
		}
		if err := files.close(context.Background(), slowPath); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	for _, path := range []string{cachedPath, filepath.Join(root, "new", runlog.FileName)} {
		w, err := files.writer(ctx, path, func() (*runlog.Writer, error) { return runlog.Open(path, runlog.Options{}) })
		if err != nil {
			t.Fatal(err)
		}
		if path == cachedPath && w != cached {
			t.Fatal("cached writer was replaced")
		}
		if err := w.AppendOutput("stdout", "other run keeps progressing\n"); err != nil {
			t.Fatal(err)
		}
		if err := files.close(ctx, path); err != nil {
			t.Fatal(err)
		}
		entries, err := runlog.ReadEntries(path, 0)
		if err != nil || len(entries) != 1 || entries[0].Data != "other run keeps progressing\n" {
			t.Fatalf("independent path entries=%+v err=%v", entries, err)
		}
	}
}

func TestIntegrationRunLogFilesConcurrentOpenRecordsOnePrompt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	files := &runLogFiles{}
	run := domain.ProjectRunRecord{RunID: "one", Prompt: "initial", LogsPath: filepath.Join(t.TempDir(), runlog.FileName)}
	defer func() {
		if err := files.close(context.Background(), run.LogsPath); err != nil {
			t.Error(err)
		}
	}()
	var calls atomic.Int32
	start := make(chan struct{})
	const count = 32
	results := make(chan error, count)
	for i := range count {
		go func() {
			<-start
			w, err := files.writer(ctx, run.LogsPath, func() (*runlog.Writer, error) {
				calls.Add(1)
				return openRunLog(run, nil)
			})
			if err == nil {
				err = w.AppendOutput("stdout", fmt.Sprintf("chunk-%d\n", i))
			}
			results <- err
		}()
	}
	close(start)
	for range count {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if err := files.close(ctx, run.LogsPath); err != nil {
		t.Fatal(err)
	}
	entries, err := runlog.ReadEntries(run.LogsPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(entries) != count+1 {
		t.Fatalf("opens=%d entries=%d", calls.Load(), len(entries))
	}
	outputs := make(map[string]bool)
	for i, entry := range entries {
		if entry.Seq != uint64(i+1) {
			t.Fatalf("sequence at %d: %+v", i, entry)
		}
		if i == 0 {
			if entry.Type != runlog.EntryUser || entry.Text != "initial" || !entry.Initial {
				t.Fatalf("initial prompt: %+v", entry)
			}
		} else {
			outputs[entry.Data] = true
		}
	}
	if len(outputs) != count {
		t.Fatalf("unique outputs=%d, want %d", len(outputs), count)
	}
}

// observedWaitContext lets a test wait until an operation reaches a cancellable
// wait, without relying on sleeps or the goroutine scheduler.
type observedWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestIntegrationRunLogFilesCloseWaitsForOpenAndCanBeCanceled(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	files := &runLogFiles{}
	path := filepath.Join(t.TempDir(), runlog.FileName)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	opened := make(chan *runlog.Writer, 1)
	openErr := make(chan error, 1)
	go func() {
		w, err := files.writer(ctx, path, func() (*runlog.Writer, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return runlog.Open(path, runlog.Options{})
		})
		opened <- w
		openErr <- err
	}()
	defer func() {
		unblock()
		if err := <-openErr; err != nil {
			t.Error(err)
		}
		if err := files.close(context.Background(), path); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	// Both a second opener and a closer must be able to abandon their wait.
	for _, closeWriter := range []bool{false, true} {
		waitCtx, stop := context.WithCancel(ctx)
		observed := &observedWaitContext{Context: waitCtx, waiting: make(chan struct{})}
		result := make(chan error, 1)
		go func() {
			if closeWriter {
				result <- files.close(observed, path)
				return
			}
			_, err := files.writer(observed, path, func() (*runlog.Writer, error) { return nil, errors.New("duplicate open") })
			result <- err
		}()
		select {
		case <-observed.waiting:
		case <-ctx.Done():
			stop()
			t.Fatal(ctx.Err())
		}
		stop()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("wait error=%v", err)
		}
	}
	waitCtx := &observedWaitContext{Context: ctx, waiting: make(chan struct{})}
	closed := make(chan error, 1)
	go func() { closed <- files.close(waitCtx, path) }()
	select {
	case <-waitCtx.waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	unblock()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	w := <-opened
	if err := w.AppendOutput("stdout", "must be closed"); err == nil {
		t.Fatal("close missed the in-flight writer")
	}
	replacement, err := files.writer(ctx, path, func() (*runlog.Writer, error) { return runlog.Open(path, runlog.Options{}) })
	if err != nil || replacement == w {
		t.Fatalf("replacement=%p old=%p err=%v", replacement, w, err)
	}
}

func TestIntegrationRunLogFilesOpenFailureCanBeRetried(t *testing.T) {
	files := &runLogFiles{}
	path := filepath.Join(t.TempDir(), runlog.FileName)
	failure := errors.New("open failed")
	if _, err := files.writer(t.Context(), path, func() (*runlog.Writer, error) { return nil, failure }); !errors.Is(err, failure) {
		t.Fatalf("error=%v", err)
	}
	w, err := files.writer(t.Context(), path, func() (*runlog.Writer, error) { return runlog.Open(path, runlog.Options{}) })
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AppendOutput("stdout", "retry succeeded"); err != nil {
		t.Fatal(err)
	}
	if err := files.close(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	entries, err := runlog.ReadEntries(path, 0)
	if err != nil || len(entries) != 1 || entries[0].Data != "retry succeeded" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}

func TestIntegrationRunLogFilesReopenWaitsForCloseFlush(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	files := &runLogFiles{}
	root := t.TempDir()
	path := filepath.Join(root, "closing", runlog.FileName)
	flushing, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	w, err := files.writer(ctx, path, func() (*runlog.Writer, error) {
		return runlog.Open(path, runlog.Options{OnAppend: func(runlog.Entry, string) {
			close(flushing)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"buffered prose"}`)); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- files.close(ctx, path) }()
	defer func() {
		unblock()
		if err := <-closed; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-flushing:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	waitCtx := &observedWaitContext{Context: ctx, waiting: make(chan struct{})}
	reopened := make(chan *runlog.Writer, 1)
	reopenErr := make(chan error, 1)
	go func() {
		next, err := files.writer(waitCtx, path, func() (*runlog.Writer, error) { return runlog.Open(path, runlog.Options{}) })
		reopened <- next
		reopenErr <- err
	}()
	defer func() {
		unblock()
		if err := <-reopenErr; err != nil {
			t.Error(err)
		}
		if err := files.close(context.Background(), path); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-waitCtx.waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	otherPath := filepath.Join(root, "other", runlog.FileName)
	other, err := files.writer(ctx, otherPath, func() (*runlog.Writer, error) { return runlog.Open(otherPath, runlog.Options{}) })
	if err != nil {
		t.Fatal(err)
	}
	if err := other.AppendOutput("stdout", "independent"); err != nil {
		t.Fatal(err)
	}
	if err := files.close(ctx, otherPath); err != nil {
		t.Fatal(err)
	}
	unblock()
	next := <-reopened
	if err := next.AppendOutput("stdout", "after flush"); err != nil {
		t.Fatal(err)
	}
	if err := files.close(ctx, path); err != nil {
		t.Fatal(err)
	}
	entries, err := runlog.ReadEntries(path, 0)
	if err != nil || len(entries) != 2 || entries[0].Text != "buffered prose" || entries[1].Data != "after flush" || entries[1].Seq != 2 {
		t.Fatalf("reopened log entries=%+v err=%v", entries, err)
	}
}

func TestIntegrationFinalizeRunLogFlushesAfterRequestCancellation(t *testing.T) {
	run := domain.ProjectRunRecord{RunID: "canceled", LogsPath: filepath.Join(t.TempDir(), runlog.FileName), LogFormat: runlog.FormatJSONL}
	t.Cleanup(func() {
		if err := closeRunLog(context.Background(), run.LogsPath); err != nil {
			t.Error(err)
		}
	})
	w, err := runLogWriter(t.Context(), run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"kept after cancel"}`)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := finalizeRunLog(ctx, &run); err != nil {
		t.Fatal(err)
	}
	if run.SummaryJSON == "" {
		t.Fatal("terminal summary was not recorded")
	}
	if err := w.AppendOutput("stdout", "must be closed"); err == nil {
		t.Fatal("canceled request left the terminal writer open")
	}
	entries, err := runlog.ReadEntries(run.LogsPath, 0)
	if err != nil || len(entries) != 1 || entries[0].Text != "kept after cancel" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}
