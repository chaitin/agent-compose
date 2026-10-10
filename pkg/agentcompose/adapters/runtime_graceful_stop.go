package adapters

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"

	"github.com/google/uuid"
)

const (
	guestRuntimeSignalInitialRetryDelay = 50 * time.Millisecond
	guestRuntimeSignalMaxRetryDelay     = 500 * time.Millisecond
)

var errGracefulStopTimedOut = errors.New("guest execution graceful stop timed out")

type guestRuntimeSignaler interface {
	SignalGuestRuntime(context.Context, *driver.Sandbox, driver.VMState, string, driver.RuntimeSignal) error
}

type activeExecution struct {
	id     string
	cancel context.CancelFunc
}

type sandboxExecutions struct {
	mu       sync.Mutex
	active   map[string]map[string]activeExecution
	stopping map[string]int
	changed  chan struct{}
}

func newSandboxExecutions() *sandboxExecutions {
	return &sandboxExecutions{active: map[string]map[string]activeExecution{}, stopping: map[string]int{}, changed: make(chan struct{})}
}

func (e *sandboxExecutions) begin(ctx context.Context, sandboxID string) (context.Context, string, func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopping[sandboxID] > 0 {
		return nil, "", nil, domain.ClassifyError(domain.ErrFailedPrecondition, "sandbox is stopping and cannot accept new work", nil)
	}
	id := uuid.NewString()
	execCtx, cancel := context.WithCancel(ctx)
	if e.active[sandboxID] == nil {
		e.active[sandboxID] = map[string]activeExecution{}
	}
	e.active[sandboxID][id] = activeExecution{id: id, cancel: cancel}
	e.broadcastLocked()
	return execCtx, id, func() { e.finish(sandboxID, id, cancel) }, nil
}

func (e *sandboxExecutions) finish(sandboxID, id string, cancel context.CancelFunc) {
	cancel()
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.active[sandboxID], id)
	if len(e.active[sandboxID]) == 0 {
		delete(e.active, sandboxID)
	}
	e.broadcastLocked()
}

func (e *sandboxExecutions) block(sandboxID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopping[sandboxID]++
	ids := e.activeIDsLocked(sandboxID)
	e.broadcastLocked()
	return ids
}

func (e *sandboxExecutions) ensureBlocked(sandboxID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopping[sandboxID] == 0 {
		e.stopping[sandboxID] = 1
	}
	ids := e.activeIDsLocked(sandboxID)
	e.broadcastLocked()
	return ids
}

func (e *sandboxExecutions) activeIDsLocked(sandboxID string) []string {
	ids := make([]string, 0, len(e.active[sandboxID]))
	for id := range e.active[sandboxID] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (e *sandboxExecutions) cancel(sandboxID string) {
	e.mu.Lock()
	items := make([]activeExecution, 0, len(e.active[sandboxID]))
	for _, item := range e.active[sandboxID] {
		items = append(items, item)
	}
	e.mu.Unlock()
	for _, item := range items {
		item.cancel()
	}
}

func (e *sandboxExecutions) wait(ctx context.Context, sandboxID string) error {
	for {
		e.mu.Lock()
		if len(e.active[sandboxID]) == 0 {
			e.mu.Unlock()
			return nil
		}
		changed := e.changed
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (e *sandboxExecutions) activeAmong(sandboxID string, ids []string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	active := e.active[sandboxID]
	remaining := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := active[id]; ok {
			remaining = append(remaining, id)
		}
	}
	return remaining
}

func (e *sandboxExecutions) waitForChange(ctx context.Context, retryAfter time.Duration) error {
	e.mu.Lock()
	changed := e.changed
	e.mu.Unlock()
	timer := time.NewTimer(retryAfter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-changed:
		return nil
	case <-timer.C:
		return nil
	}
}

func (e *sandboxExecutions) release(sandboxID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopping[sandboxID] <= 1 {
		delete(e.stopping, sandboxID)
	} else {
		e.stopping[sandboxID]--
	}
	e.broadcastLocked()
}

func (e *sandboxExecutions) broadcastLocked() {
	close(e.changed)
	e.changed = make(chan struct{})
}

func (r driverRuntimeAdapter) beginExecution(ctx context.Context, session *domain.Sandbox, spec domain.ExecSpec) (context.Context, domain.ExecSpec, func(), error) {
	if r.executions == nil || session == nil {
		return ctx, spec, func() {}, nil
	}
	execCtx, id, finish, err := r.executions.begin(ctx, session.Summary.ID)
	if err != nil {
		return nil, domain.ExecSpec{}, nil, err
	}
	marked := spec
	marked.Env = driver.GuestRuntimeControlEnv(spec.Env, id)
	return execCtx, marked, finish, nil
}

func (r driverRuntimeAdapter) beginInteraction(ctx context.Context, session *domain.Sandbox, spec driver.RuntimeStartSpec) (context.Context, driver.RuntimeStartSpec, func(), error) {
	if r.executions == nil || session == nil {
		return ctx, spec, func() {}, nil
	}
	execCtx, id, finish, err := r.executions.begin(ctx, session.Summary.ID)
	if err != nil {
		return nil, driver.RuntimeStartSpec{}, nil, err
	}
	marked := spec
	marked.Env = driver.GuestRuntimeControlEnv(spec.Env, id)
	if spec.Command != nil {
		command := *spec.Command
		command.Env = driver.GuestRuntimeControlEnv(spec.Command.Env, id)
		marked.Command = &command
	}
	return execCtx, marked, finish, nil
}

func (r driverRuntimeAdapter) PrepareSandboxStop(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, gracePeriod time.Duration) (sandboxes.StopPreparationResult, error) {
	if r.executions == nil || session == nil {
		return sandboxes.StopPreparationResult{Outcome: sandboxes.StopPreparationGraceful}, nil
	}
	ids := r.executions.block(session.Summary.ID)
	if len(ids) == 0 {
		return sandboxes.StopPreparationResult{Outcome: sandboxes.StopPreparationGraceful}, nil
	}
	graceCtx, cancel := context.WithTimeout(ctx, gracePeriod)
	defer cancel()
	if err := r.signalManagedExecutions(graceCtx, session, vmState, ids); err != nil {
		r.executions.cancel(session.Summary.ID)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return sandboxes.StopPreparationResult{Outcome: sandboxes.StopPreparationFailed, Error: ctxErr}, nil
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return sandboxes.StopPreparationResult{Outcome: sandboxes.StopPreparationTimeout, Error: errors.Join(errGracefulStopTimedOut, err)}, nil
		}
		return sandboxes.StopPreparationResult{Outcome: sandboxes.StopPreparationFailed, Error: err}, nil
	}
	return sandboxes.StopPreparationResult{Outcome: sandboxes.StopPreparationGraceful}, nil
}

func (r driverRuntimeAdapter) BeginSandboxForceStop(session *domain.Sandbox) {
	if r.executions == nil || session == nil {
		return
	}
	r.executions.block(session.Summary.ID)
	r.executions.cancel(session.Summary.ID)
}

func (r driverRuntimeAdapter) FinishSandboxStop(session *domain.Sandbox) {
	if r.executions != nil && session != nil {
		r.executions.release(session.Summary.ID)
	}
}

func (r driverRuntimeAdapter) signalManagedExecutions(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, ids []string) error {
	pending := append([]string(nil), ids...)
	retryAttempt := 0
	for len(pending) > 0 {
		pending = r.executions.activeAmong(session.Summary.ID, pending)
		if len(pending) == 0 {
			return r.executions.wait(ctx, session.Summary.ID)
		}
		signaled, err := r.signalReadyManagedExecutions(ctx, session, vmState, pending)
		if err != nil {
			return err
		}
		pending = unsignaledActiveExecutions(r.executions.activeAmong(session.Summary.ID, pending), signaled)
		if len(pending) == 0 {
			return r.executions.wait(ctx, session.Summary.ID)
		}
		if err := r.executions.waitForChange(ctx, guestRuntimeSignalRetryDelay(retryAttempt)); err != nil {
			return err
		}
		retryAttempt++
	}
	return nil
}

func guestRuntimeSignalRetryDelay(attempt int) time.Duration {
	delay := guestRuntimeSignalInitialRetryDelay
	for range attempt {
		if delay >= guestRuntimeSignalMaxRetryDelay/2 {
			return guestRuntimeSignalMaxRetryDelay
		}
		delay *= 2
	}
	return delay
}

func (r driverRuntimeAdapter) signalReadyManagedExecutions(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, ids []string) (map[string]struct{}, error) {
	signaler, ok := r.runtime.(guestRuntimeSignaler)
	if !ok {
		return nil, fmt.Errorf("runtime driver does not support guest runtime signals")
	}
	type signalResult struct {
		id  string
		err error
	}
	driverSandbox := execution.ToDriverSandbox(session)
	driverVMState := execution.ToDriverVMState(vmState)
	results := make(chan signalResult, len(ids))
	for _, id := range ids {
		go func() {
			results <- signalResult{
				id: id,
				err: signaler.SignalGuestRuntime(
					ctx,
					driverSandbox,
					driverVMState,
					id,
					driver.RuntimeSignalTerminate,
				),
			}
		}()
	}
	signaled := make(map[string]struct{}, len(ids))
	var signalErrors []error
	for range ids {
		result := <-results
		switch {
		case result.err == nil, errors.Is(result.err, driver.ErrGuestRuntimeGone):
			signaled[result.id] = struct{}{}
		case errors.Is(result.err, driver.ErrGuestRuntimeNotReady):
			// Readiness is published only after the runtime's TERM handler exists.
		default:
			signalErrors = append(signalErrors, fmt.Errorf("signal guest execution %s: %w", result.id, result.err))
		}
	}
	return signaled, errors.Join(signalErrors...)
}

func unsignaledActiveExecutions(active []string, signaled map[string]struct{}) []string {
	remaining := make([]string, 0, len(active))
	for _, id := range active {
		if _, ok := signaled[id]; !ok {
			remaining = append(remaining, id)
		}
	}
	return remaining
}

type trackedRuntimeInteraction struct {
	driver.RuntimeInteraction
	finishOnce sync.Once
	finish     func()
	done       chan struct{}
	// sandboxID and driverName identify the sandbox whose lower-layer isolation
	// facts are reported when Wait returns. reportOnce keeps a second Wait call
	// from logging the same facts twice.
	sandboxID  string
	driverName string
	reportOnce sync.Once
}

func (i *trackedRuntimeInteraction) Recv() (driver.RuntimeOutputFrame, error) {
	frame, err := i.RuntimeInteraction.Recv()
	if err != nil {
		i.complete()
	}
	return frame, err
}

func (i *trackedRuntimeInteraction) Wait() (driver.RuntimeResult, error) {
	result, err := i.RuntimeInteraction.Wait()
	i.reportLowerLayerIsolationFacts(result)
	i.complete()
	return result, err
}

// reportLowerLayerIsolationFacts logs the isolation failures the lower layer
// reported during this operation. The result frame carries the same facts to a
// caller that only reads frames, but Wait is the shared completion point of an
// interactive run, so it is where the engine consumes them.
func (i *trackedRuntimeInteraction) reportLowerLayerIsolationFacts(result driver.RuntimeResult) {
	if result.SecurityFacts == nil {
		return
	}
	i.reportOnce.Do(func() {
		reportMeasuredLowerLayerIsolationFacts(i.sandboxID, i.driverName, *result.SecurityFacts)
	})
}

func (i *trackedRuntimeInteraction) complete() {
	i.finishOnce.Do(func() {
		if i.done != nil {
			close(i.done)
		}
		i.finish()
	})
}

func (i *trackedRuntimeInteraction) finishWhenContextEnds(ctx context.Context) {
	select {
	case <-ctx.Done():
		i.complete()
	case <-i.done:
	}
}
