package runs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// AbandonCompletion finalizes a run whose staged sandbox cleanup an operator
// gives up on.
//
// The retry bound already ends a completion that keeps failing, so this exists
// for the operator who should not have to wait for it - a permanently
// unreachable runtime is a deployment fact, not something the loop will
// discover. The run still reaches the terminal state its first completion
// writer selected, and the record keeps the cleanup failure and says the sandbox
// runtime may still exist, because nothing here removes it.
//
// A run that is still executing has no staged completion and is refused: that
// run must be stopped, not abandoned, so its runtime is still cleaned up. The
// returned bool is false when there was nothing to abandon because the run was
// already terminal.
func (m *CompletionManager) AbandonCompletion(ctx context.Context, runID, reason string) (domain.ProjectRunRecord, bool, error) {
	if m == nil || m.store == nil {
		return domain.ProjectRunRecord{}, false, fmt.Errorf("project run completion store is required")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return domain.ProjectRunRecord{}, false, domain.ClassifyError(domain.ErrInvalidArgument, "run id is required", nil)
	}
	lock := m.runLock(runID)
	lock.Lock()
	defer lock.Unlock()
	run, err := m.store.GetProjectRun(ctx, runID)
	if err != nil {
		return domain.ProjectRunRecord{}, false, err
	}
	if StatusIsTerminal(run.Status) {
		return run, false, nil
	}
	completion, err := m.store.GetProjectRunCompletion(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return run, false, domain.ClassifyError(domain.ErrFailedPrecondition,
			fmt.Sprintf("run %s has no staged sandbox cleanup to abandon; it is still executing, so stop it instead", runID), nil)
	}
	if err != nil {
		return domain.ProjectRunRecord{}, false, err
	}
	next, statusEvent, err := terminalRunUpdate(run, completion, m.nowUTC())
	if err != nil {
		return run, false, err
	}
	// The terminal record must land even when the caller's request is already
	// gone: the operator asked for the run to end, and an expired context must
	// not leave it running for the same retry loop to keep failing.
	finalized, err := m.store.AbandonProjectRunCompletion(context.WithoutCancel(ctx), next, statusEvent,
		abandonedCleanupMessage(completion.LastError, reason))
	if err != nil {
		return run, false, err
	}
	m.logger.Warn("abandoning project run sandbox cleanup on operator request",
		"run_id", runID, "sandbox_id", run.SandboxID, "action", completion.CleanupAction,
		"attempt", completion.Attempt, "reason", strings.TrimSpace(reason))
	return finalized, true, nil
}

// abandonedCleanupMessage is what the run records about the cleanup an operator
// gave up on. It keeps the failure that stopped the retry loop, because that
// text is the only remaining diagnosis once the completion journal is released,
// and it names the consequence - the runtime may still exist - so a later
// reader does not mistake the abandoned run for a cleaned-up one.
func abandonedCleanupMessage(lastError, reason string) string {
	message := "sandbox cleanup abandoned by operator; the sandbox runtime may still exist"
	if trimmed := strings.TrimSpace(lastError); trimmed != "" {
		message += "; last cleanup error: " + trimmed
	}
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		message += "; operator note: " + trimmed
	}
	return message
}
