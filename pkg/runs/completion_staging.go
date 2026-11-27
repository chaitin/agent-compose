package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// unstagedCompletion bridges execution and durable completion ownership. It is
// daemon-local: a crash before staging still needs the existing execution
// recovery path. The immutable record and events are owned by the manager;
// retry state and sandboxID are protected by its mutex.
type unstagedCompletion struct {
	record        domain.ProjectRunCompletionRecord
	events        []domain.ProjectRunEventRecord
	sandboxID     string
	attempt       int
	nextAttemptAt time.Time
	lastError     error
	inFlight      chan struct{}
}

func (m *CompletionManager) retainCompletion(req TransitionRequest) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode project run completion: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.unstaged[req.RunID]; exists {
		// A later stop or recovery request must not replace the first result.
		return nil
	}
	m.unstaged[req.RunID] = &unstagedCompletion{
		record: domain.ProjectRunCompletionRecord{
			RunID: req.RunID, TargetStatus: req.Status, TransitionJSON: string(payload),
		},
		events: slices.Clone(req.TerminalEvents), sandboxID: strings.TrimSpace(req.SandboxID),
	}
	return nil
}

func (m *CompletionManager) stageRetainedCompletion(ctx context.Context, runID string) error {
	for {
		m.mu.Lock()
		item := m.unstaged[runID]
		if item == nil {
			m.mu.Unlock()
			return nil
		}
		if done := item.inFlight; done != nil {
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-done:
				continue
			}
		}
		if item.nextAttemptAt.After(m.nowUTC()) {
			err := item.lastError
			m.mu.Unlock()
			return err
		}
		item.inFlight = make(chan struct{})
		m.mu.Unlock()

		// Reserve the attempt under the mutex, but perform all I/O outside it.
		err := m.persistRetainedCompletion(ctx, item)
		m.finishStagingAttempt(runID, item, err)
		return err
	}
}

func (m *CompletionManager) persistRetainedCompletion(ctx context.Context, item *unstagedCompletion) error {
	run, err := m.store.GetProjectRun(ctx, item.record.RunID)
	if err != nil {
		return fmt.Errorf("load project run for completion staging: %w", err)
	}
	if StatusIsTerminal(run.Status) {
		return nil
	}
	m.mu.Lock()
	item.sandboxID = strings.TrimSpace(run.SandboxID)
	m.mu.Unlock()
	record := item.record
	record.CleanupAction = CompletionCleanupAction(run.CleanupPolicy, run.SandboxID != "", run.SandboxCreated)
	if _, _, err := m.store.StageProjectRunCompletion(ctx, record, item.events); err != nil {
		return fmt.Errorf("stage project run completion: %w", err)
	}
	return nil
}

func (m *CompletionManager) finishStagingAttempt(runID string, item *unstagedCompletion, err error) {
	m.mu.Lock()
	retained := m.unstaged[runID] == item
	if err == nil && retained {
		delete(m.unstaged, runID)
	} else if err != nil && retained {
		item.attempt++
		item.lastError = err
		item.nextAttemptAt = m.nowUTC().Add(completionRetryDelay(item.attempt))
	}
	attempt, next := item.attempt, item.nextAttemptAt
	close(item.inFlight)
	item.inFlight = nil
	m.mu.Unlock()
	if err != nil && retained {
		m.logger.Warn("project run completion staging failed; result retained for retry",
			"run_id", runID, "phase", "staging", "attempt", attempt, "next_attempt_at", next, "error", err)
	}
	m.signal()
}

func (m *CompletionManager) forgetRetainedCompletion(runID string) {
	m.mu.Lock()
	delete(m.unstaged, runID)
	m.mu.Unlock()
}

func (m *CompletionManager) stageDueCompletions(ctx context.Context) {
	m.mu.Lock()
	var due []string
	for runID, item := range m.unstaged {
		if item.inFlight == nil && !item.nextAttemptAt.After(m.nowUTC()) {
			due = append(due, runID)
		}
	}
	m.mu.Unlock()
	for _, runID := range due {
		if ctx.Err() != nil {
			return
		}
		// finishStagingAttempt records the error and retains the result. A
		// failure of one run must not prevent attempts for the remaining runs.
		if err := m.stageRetainedCompletion(ctx, runID); err != nil {
			continue
		}
	}
}

func (m *CompletionManager) unstagedShutdownError() error {
	type remainingResult struct {
		runID   string
		attempt int
		err     error
	}
	m.mu.Lock()
	remaining := make([]remainingResult, 0, len(m.unstaged))
	for runID, item := range m.unstaged {
		remaining = append(remaining, remainingResult{runID, item.attempt, item.lastError})
	}
	m.mu.Unlock()
	if len(remaining) == 0 {
		return nil
	}
	for _, item := range remaining {
		m.logger.Error("daemon stopped before project run completion was persisted",
			"run_id", item.runID, "attempt", item.attempt, "error", item.err)
	}
	return fmt.Errorf("%d project run completion results have not been confirmed persisted at shutdown", len(remaining))
}
