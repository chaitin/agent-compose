package runs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
)

const completionWorkerCount = 2

// completionMaxAttempts bounds how many times a run's sandbox cleanup is tried
// before the run is finalized anyway with the cleanup failure recorded on it.
// Without a bound, a permanent cleanup error (an unreachable runtime, a missing
// kubeconfig) leaves the run non-terminal and re-runs the same failing driver
// call every 30s for the rest of the daemon's lifetime, and no product path can
// clear it. The delays below total about two minutes before the bound applies.
const completionMaxAttempts = 8

// completionWaitTimeout bounds how long Complete waits for a staged completion
// to converge. It is generous relative to completionMaxAttempts retries; it
// exists so a storage failure inside the completion path surfaces as an error
// instead of hanging the caller forever.
const completionWaitTimeout = 5 * time.Minute

type CompletionStore interface {
	GetProjectRun(context.Context, string) (domain.ProjectRunRecord, error)
	StageProjectRunCompletion(context.Context, domain.ProjectRunCompletionRecord, []domain.ProjectRunEventRecord) (domain.ProjectRunCompletionRecord, bool, error)
	GetProjectRunCompletion(context.Context, string) (domain.ProjectRunCompletionRecord, error)
	ListDueProjectRunCompletions(context.Context, time.Time, int) ([]domain.ProjectRunCompletionRecord, error)
	RecordProjectRunCompletionFailure(context.Context, domain.ProjectRunCompletionFailure) error
	FinalizeProjectRunCompletion(context.Context, domain.ProjectRunRecord, domain.ProjectRunEventRecord) (domain.ProjectRunRecord, error)
	// AbandonProjectRunCompletion finalizes a run whose sandbox cleanup never
	// succeeded, recording cleanupError on the run instead of clearing it.
	AbandonProjectRunCompletion(context.Context, domain.ProjectRunRecord, domain.ProjectRunEventRecord, string) (domain.ProjectRunRecord, error)
	ProjectRunCompletionForSandbox(context.Context, string) (string, bool, error)
}

type CompletionSandboxStore interface {
	GetSandbox(context.Context, string) (*domain.Sandbox, error)
}

type CompletionSandboxLifecycle interface {
	Stop(context.Context, *domain.Sandbox) error
}

type CompletionManager struct {
	store     CompletionStore
	sandboxes CompletionSandboxStore
	lifecycle CompletionSandboxLifecycle
	removal   SandboxRemoval
	logger    *slog.Logger
	now       func() time.Time

	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	wake    chan struct{}
	started bool
}

// CompletionManagerDeps groups CompletionManager's constructor dependencies.
// Logger is optional; a nil value falls back to slog.Default().
type CompletionManagerDeps struct {
	Store     CompletionStore
	Sandboxes CompletionSandboxStore
	Lifecycle CompletionSandboxLifecycle
	Removal   SandboxRemoval
	Logger    *slog.Logger
}

func NewCompletionManager(deps CompletionManagerDeps) *CompletionManager {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &CompletionManager{
		store: deps.Store, sandboxes: deps.Sandboxes, lifecycle: deps.Lifecycle, removal: deps.Removal, logger: logger,
		now: func() time.Time { return time.Now().UTC() }, locks: map[string]*sync.Mutex{}, wake: make(chan struct{}, 1),
	}
}

func (m *CompletionManager) Complete(ctx context.Context, req TransitionRequest) (domain.ProjectRunRecord, error) {
	if m == nil || m.store == nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("project run completion store is required")
	}
	req.RunID = strings.TrimSpace(req.RunID)
	req.Status = NormalizeStatus(req.Status)
	if req.RunID == "" || !StatusIsTerminal(req.Status) {
		return domain.ProjectRunRecord{}, fmt.Errorf("terminal project run completion is required")
	}
	current, err := m.store.GetProjectRun(ctx, req.RunID)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if StatusIsTerminal(current.Status) {
		return current, nil
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("encode project run completion: %w", err)
	}
	action := CompletionCleanupAction(current.CleanupPolicy, strings.TrimSpace(current.SandboxID) != "", current.SandboxCreated)
	staged, _, err := m.store.StageProjectRunCompletion(ctx, domain.ProjectRunCompletionRecord{
		RunID: req.RunID, TargetStatus: req.Status, TransitionJSON: string(payload), CleanupAction: action,
	}, req.TerminalEvents)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	m.signal()
	deadline := m.nowUTC().Add(completionWaitTimeout)
	for {
		run, processErr := m.process(ctx, staged.RunID)
		if processErr == nil && StatusIsTerminal(run.Status) {
			return run, nil
		}
		if !m.nowUTC().Before(deadline) {
			return m.completionWaitTimeout(ctx, staged.RunID, processErr)
		}
		select {
		case <-ctx.Done():
			return m.store.GetProjectRun(context.WithoutCancel(ctx), staged.RunID)
		case <-time.After(100 * time.Millisecond):
		}
		staged, err = m.store.GetProjectRunCompletion(ctx, staged.RunID)
		if errors.Is(err, sql.ErrNoRows) {
			return m.store.GetProjectRun(ctx, req.RunID)
		}
		if err != nil {
			return domain.ProjectRunRecord{}, err
		}
		if staged.NextAttemptAt.After(m.nowUTC()) {
			wait := time.Until(staged.NextAttemptAt)
			if wait > 250*time.Millisecond {
				wait = 250 * time.Millisecond
			}
			select {
			case <-ctx.Done():
				return m.store.GetProjectRun(context.WithoutCancel(ctx), staged.RunID)
			case <-time.After(wait):
			}
		}
	}
}

// completionWaitTimeout reports why Complete stopped waiting, returning the
// latest run alongside the error so callers still see the state it is stuck in.
func (m *CompletionManager) completionWaitTimeout(ctx context.Context, runID string, lastErr error) (domain.ProjectRunRecord, error) {
	latest, err := m.store.GetProjectRun(context.WithoutCancel(ctx), runID)
	if err != nil {
		return domain.ProjectRunRecord{}, errors.Join(lastErr, err)
	}
	timeoutErr := fmt.Errorf("project run %s did not reach a terminal state within %s (last status %q)", runID, completionWaitTimeout, latest.Status)
	if lastErr != nil {
		return latest, errors.Join(timeoutErr, lastErr)
	}
	return latest, timeoutErr
}

func (m *CompletionManager) StageInterrupted(ctx context.Context, run domain.ProjectRunRecord, message string) error {
	req := TransitionRequest{RunID: run.RunID, Status: domain.ProjectRunStatusFailed, ExitCode: max(run.ExitCode, 1), Error: message}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, _, err = m.store.StageProjectRunCompletion(ctx, domain.ProjectRunCompletionRecord{
		RunID: run.RunID, TargetStatus: req.Status, TransitionJSON: string(payload),
		CleanupAction: CompletionCleanupAction(run.CleanupPolicy, strings.TrimSpace(run.SandboxID) != "", run.SandboxCreated),
	}, nil)
	m.signal()
	return err
}

// Pending reports whether runID has a staged completion still to finish: its
// result is already decided and the manager will finalize it.
func (m *CompletionManager) Pending(ctx context.Context, runID string) (bool, error) {
	if m == nil || m.store == nil {
		return false, nil
	}
	_, err := m.store.GetProjectRunCompletion(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (m *CompletionManager) PendingForSandbox(ctx context.Context, sandboxID string) (string, bool, error) {
	return m.store.ProjectRunCompletionForSandbox(ctx, sandboxID)
}

func (m *CompletionManager) Start(parent context.Context) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("project run completion manager is not configured")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	m.done = make(chan struct{})
	m.started = true
	go m.run(ctx, m.done)
	return nil
}

func (m *CompletionManager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	if cancel == nil || done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *CompletionManager) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	var workers sync.WaitGroup
	workers.Add(completionWorkerCount)
	for range completionWorkerCount {
		go func() {
			defer workers.Done()
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				if err := m.processDue(ctx); err != nil && ctx.Err() == nil {
					m.logger.Warn("failed to process project run completions", "error", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-m.wake:
				case <-ticker.C:
				}
			}
		}()
	}
	workers.Wait()
}

func (m *CompletionManager) processDue(ctx context.Context) error {
	items, err := m.store.ListDueProjectRunCompletions(ctx, m.nowUTC(), 100)
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err := m.process(ctx, item.RunID); err != nil && ctx.Err() == nil {
			continue
		}
	}
	return nil
}

func (m *CompletionManager) process(ctx context.Context, runID string) (domain.ProjectRunRecord, error) {
	lock := m.runLock(runID)
	lock.Lock()
	defer lock.Unlock()
	completion, err := m.store.GetProjectRunCompletion(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return m.store.GetProjectRun(ctx, runID)
	}
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if completion.NextAttemptAt.After(m.nowUTC()) {
		return m.store.GetProjectRun(ctx, runID)
	}
	run, err := m.store.GetProjectRun(ctx, runID)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if err := m.cleanup(ctx, run, completion.CleanupAction); err != nil {
		attempt := completion.Attempt + 1
		if attempt >= completionMaxAttempts {
			return m.abandonCleanup(ctx, run, completion, attempt, err)
		}
		next := m.nowUTC().Add(completionRetryDelay(attempt))
		if recordErr := m.store.RecordProjectRunCompletionFailure(context.WithoutCancel(ctx), domain.ProjectRunCompletionFailure{
			RunID: runID, Message: err.Error(), Attempt: attempt, NextAttemptAt: next,
		}); recordErr != nil {
			return run, errors.Join(err, recordErr)
		}
		m.logger.Warn("project run sandbox cleanup failed", "run_id", runID, "sandbox_id", run.SandboxID, "action", completion.CleanupAction, "attempt", attempt, "next_attempt_at", next, "error", err)
		return run, err
	}
	next, statusEvent, err := terminalRunUpdate(run, completion, m.nowUTC())
	if err != nil {
		return run, err
	}
	return m.store.FinalizeProjectRunCompletion(ctx, next, statusEvent)
}

// abandonCleanup finalizes a run whose sandbox cleanup kept failing. The run
// still reaches the terminal state its first completion writer selected, the
// cleanup failure is recorded on the run, and the completion row is deleted, so
// a permanent cleanup error converges instead of retrying forever.
func (m *CompletionManager) abandonCleanup(ctx context.Context, run domain.ProjectRunRecord, completion domain.ProjectRunCompletionRecord, attempt int, cleanupErr error) (domain.ProjectRunRecord, error) {
	next, statusEvent, err := terminalRunUpdate(run, completion, m.nowUTC())
	if err != nil {
		return run, err
	}
	// The terminal record must land even when the caller's context is already
	// gone: a cleanup failure is often what cancelled the request, and leaving
	// the run non-terminal here is the stuck state this bound exists to end.
	finalized, err := m.store.AbandonProjectRunCompletion(context.WithoutCancel(ctx), next, statusEvent, cleanupErr.Error())
	if err != nil {
		return run, errors.Join(cleanupErr, err)
	}
	m.logger.Warn("abandoning project run sandbox cleanup after repeated failures",
		"run_id", run.RunID, "sandbox_id", run.SandboxID, "action", completion.CleanupAction,
		"attempt", attempt, "cleanup_error", cleanupErr)
	return finalized, nil
}

// terminalRunUpdate builds the terminal run record and its status event from a
// staged completion, applying the transition selected by the first writer.
func terminalRunUpdate(run domain.ProjectRunRecord, completion domain.ProjectRunCompletionRecord, now time.Time) (domain.ProjectRunRecord, domain.ProjectRunEventRecord, error) {
	var req TransitionRequest
	if err := json.Unmarshal([]byte(completion.TransitionJSON), &req); err != nil {
		return run, domain.ProjectRunEventRecord{}, fmt.Errorf("decode project run completion %s: %w", run.RunID, err)
	}
	next := run
	next.Status = completion.TargetStatus
	applyProjectRunTransitionFields(&next, req)
	if next.StartedAt.IsZero() {
		next.StartedAt = now
	}
	next.CompletedAt = now
	next.DurationMs = max(0, next.CompletedAt.Sub(next.StartedAt).Milliseconds())
	statusEvent := domain.ProjectRunEventRecord{ID: terminalStatusEventID(run.RunID), RunID: run.RunID, Kind: domain.ProjectRunEventKindStatus, PayloadJSON: next.ResultJSON, Success: next.Status == domain.ProjectRunStatusSucceeded, ExitCode: next.ExitCode, StopReason: next.Error}
	return next, statusEvent, nil
}

func (m *CompletionManager) cleanup(ctx context.Context, run domain.ProjectRunRecord, action string) error {
	if action == domain.ProjectRunCompletionActionNone || strings.TrimSpace(run.SandboxID) == "" {
		return nil
	}
	if action == domain.ProjectRunCompletionActionRemove {
		if m.removal == nil {
			return fmt.Errorf("sandbox removal coordinator is required")
		}
		result, err := m.removal.Remove(ctx, run.SandboxID, true)
		if err == nil && result.Removed {
			return nil
		}
		if err == nil {
			return fmt.Errorf("sandbox removal did not complete")
		}
		if errors.Is(err, sandboxes.ErrOwnershipUnknown) && m.sandboxMissing(ctx, run.SandboxID) {
			return nil
		}
		return err
	}
	if m.sandboxes == nil || m.lifecycle == nil {
		return fmt.Errorf("sandbox stop lifecycle is required")
	}
	sandbox, err := m.sandboxes.GetSandbox(ctx, run.SandboxID)
	if err != nil {
		if completionSandboxMissing(err) {
			return nil
		}
		return err
	}
	return m.lifecycle.Stop(ctx, sandbox)
}

func (m *CompletionManager) sandboxMissing(ctx context.Context, sandboxID string) bool {
	if m.sandboxes == nil {
		return false
	}
	_, err := m.sandboxes.GetSandbox(ctx, sandboxID)
	return completionSandboxMissing(err)
}

func completionSandboxMissing(err error) bool {
	return err != nil && (errors.Is(err, sql.ErrNoRows) || errors.Is(err, os.ErrNotExist) || errors.Is(err, domain.ErrNotFound))
}

func completionRetryDelay(attempt int) time.Duration {
	delays := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	if attempt <= 0 {
		return 0
	}
	if attempt > len(delays) {
		return 30 * time.Second
	}
	return delays[attempt-1]
}

func (m *CompletionManager) runLock(runID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock := m.locks[runID]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[runID] = lock
	}
	return lock
}

func (m *CompletionManager) signal() {
	if m == nil {
		return
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *CompletionManager) nowUTC() time.Time {
	if m.now != nil {
		return m.now().UTC()
	}
	return time.Now().UTC()
}
