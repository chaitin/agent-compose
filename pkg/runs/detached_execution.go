package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// ErrRunDisconnected reports that an attach was dropped because the daemon
// shut down while the detached task keeps running. The run must be left in its
// running state for a later reconciliation to re-attach, not marked terminal.
var ErrRunDisconnected = errors.New("run disconnected: task still running in guest")

// detachStopTimeout bounds how long the daemon waits for the `stop` request to
// reach the supervisor when terminating a detached task, and separately how
// long the marker-based fallback termination may take. A var so tests can
// shorten it.
var detachStopTimeout = 30 * time.Second

// detachCleanupTimeout bounds the best-effort removal of a finished
// execution's guest state.
var detachCleanupTimeout = 10 * time.Second

// DetachedTask describes a guest task run behind the detach supervisor: a
// project command or agent run, fresh or re-attached after a daemon restart.
type DetachedTask struct {
	Config      *appconfig.Config
	Runtime     Runtime
	Sandbox     *domain.Sandbox
	VMState     domain.VMState
	ExecutionID string
	// TaskArgs is the guest CLI subcommand plus flags (e.g. ["exec",
	// "--request-file", ...]); the supervisor re-enters the CLI with them.
	TaskArgs []string
	Env      map[string]string
	// TaskTimeout is the remaining run timeout handed to the supervisor so it
	// can stop the task on its own while no daemon is attached; 0 disables it.
	TaskTimeout time.Duration
	// Cursor is the journal offset to resume from (0 for a fresh run). Output
	// before it was already delivered and is not forwarded again.
	Cursor uint64
	// ReplayAll attaches from the start of the journal instead of Cursor, so
	// the result is rebuilt from the whole output while records before Cursor
	// are still not forwarded. A re-attached agent run needs it: its result
	// payload may be before the cursor when the daemon stopped between the
	// payload and the exit record.
	ReplayAll bool
	// Writer forwards task output chunks to the run log; may be nil.
	Writer func(domain.ExecChunk)
	// PersistCursor records the highest consumed journal offset so a restart
	// resumes from there without duplicating output.
	PersistCursor func(uint64) error
}

// detachedExecutionResult is the reconstructed outcome of a detached run.
type detachedExecutionResult struct {
	Result domain.ExecResult
	// Abnormal reports that the attach found the supervisor gone without an
	// exit record, so the task's process tree may still be running unmanaged.
	Abnormal   bool
	StopReason string
	Err        error
}

// executeDetachedCommand runs a guest task behind the detach supervisor and
// follows its journal until the task ends. ctx is the run context: cancelled
// only by StopActiveRun or a timeout. On ctx cancellation it sends a stop
// request to the supervisor and waits for the exit record; on daemon shutdown
// (the signal attached with WithDaemonShutdown) it disconnects the follower
// and returns ErrRunDisconnected without terminating the task.
func executeDetachedCommand(ctx context.Context, opts DetachedTask) detachedExecutionResult {
	if result := startDetachedCommand(ctx, opts); result.Err != nil {
		return result
	}
	return followDetachedCommand(ctx, opts)
}

// startDetachedCommand launches the supervisor in the guest and waits for its
// ready record (the start exec blocks until the task is detached).
func startDetachedCommand(ctx context.Context, opts DetachedTask) detachedExecutionResult {
	startSpec := execution.BuildDetachStartSpec(opts.Config, opts.ExecutionID, execution.DetachStartOptions{TaskTimeout: opts.TaskTimeout}, opts.TaskArgs)
	startSpec.Env = opts.Env
	if result, err := opts.Runtime.ExecStream(ctx, opts.Sandbox, opts.VMState, startSpec, nil); err != nil || result.ExitCode != 0 {
		if detachIsShutdown(ctx) {
			// The daemon shut down before the task detached. Whether or not the
			// supervisor was spawned, the run must be left running for the next
			// reconciliation rather than recorded as a terminal failure.
			return detachedExecutionResult{Err: ErrRunDisconnected}
		}
		// The start exec failed (non-zero exit or transport error) after it may
		// already have spawned the detached supervisor: the guest spawns the
		// supervisor before waiting for ready, so a ready timeout or a crash
		// after spawn leaves a tree the daemon is about to stop tracking. End it
		// by marker so it cannot linger as an orphan. The shutdown branch above
		// deliberately skips this so the task survives for re-attach.
		startErr := detachControlExecErr(result, err, "start detached execution")
		terminateCtx, cancelTerminate := context.WithTimeout(context.WithoutCancel(ctx), detachStopTimeout)
		terminateErr := terminateDetachedProcessTree(terminateCtx, opts)
		cancelTerminate()
		if terminateErr != nil {
			startErr = errors.Join(startErr, terminateErr)
		}
		if ctx.Err() != nil {
			startErr = errors.Join(ctx.Err(), startErr)
		}
		return detachedExecutionResult{Err: startErr}
	}
	return detachedExecutionResult{}
}

// followDetachedCommand attaches to a detached task and follows its journal to
// completion. It assumes the task is already running (startDetachedCommand, or
// an earlier daemon that is now gone). Once the run is over for the daemon it
// removes the execution's guest state; after a disconnect it keeps it so the
// next daemon can resume.
func followDetachedCommand(ctx context.Context, opts DetachedTask) detachedExecutionResult {
	result := followDetachedJournal(ctx, opts)
	if !errors.Is(result.Err, ErrRunDisconnected) {
		cleanupDetachedExecution(ctx, opts)
	}
	return result
}

// cleanupDetachedExecution best-effort removes the execution's journal and
// state records from the guest. The guest refuses while the task still runs
// (e.g. a stop that never landed), and any failure is left to the guest's
// retention-based pruning.
func cleanupDetachedExecution(ctx context.Context, opts DetachedTask) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), detachCleanupTimeout)
	defer cancel()
	spec := execution.BuildDetachCleanupSpec(opts.Config, opts.ExecutionID)
	spec.Env = opts.Env
	_, _ = opts.Runtime.ExecStream(cleanupCtx, opts.Sandbox, opts.VMState, spec, nil)
}

func followDetachedJournal(ctx context.Context, opts DetachedTask) detachedExecutionResult {
	follower := newDetachFollower(opts.Writer, opts.PersistCursor)
	follower.forwardFrom = opts.Cursor
	attachFrom := opts.Cursor
	if opts.ReplayAll {
		attachFrom = 0
	}
	attachCtx, cancelAttach := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancelAttach(nil)
	attachStop := detachAttachShutdownWatcher(attachCtx, cancelAttach, ctx)

	attachSpec := execution.BuildDetachAttachSpec(opts.Config, opts.ExecutionID, attachFrom)
	attachSpec.Env = opts.Env

	outcome := make(chan detachedExecutionResult, 1)
	go func() {
		defer attachStop()
		_, err := opts.Runtime.ExecStream(attachCtx, opts.Sandbox, opts.VMState, attachSpec, follower.consume)
		outcome <- follower.result(err, attachCtx)
	}()

	select {
	case <-ctx.Done():
		if detachIsShutdown(ctx) {
			// Daemon shutdown: the detached task keeps running. Disconnect the
			// follower and surface ErrRunDisconnected instead of terminating it.
			cancelAttach(ErrRunDisconnected)
			return <-outcome
		}
		reason := detachStopReason(ctx)
		stopCtx, cancelStop := context.WithTimeout(context.WithoutCancel(ctx), detachStopTimeout)
		defer cancelStop()
		stopSpec := execution.BuildDetachStopSpec(opts.Config, opts.ExecutionID, reason)
		stopSpec.Env = opts.Env
		stopResult, stopErr := opts.Runtime.ExecStream(stopCtx, opts.Sandbox, opts.VMState, stopSpec, nil)
		if stopErr != nil || stopResult.ExitCode != 0 {
			// The stop request did not reach the supervisor: a transport error,
			// or the guest CLI exited non-zero (its writeStop failed). Terminate
			// the task's process tree directly by marker so it cannot outlive
			// the run, then drop the follower with the combined failure.
			stopErr = detachControlExecErr(stopResult, stopErr, "detached execution stop request")
			// The stop request may have failed because stopCtx timed out, so the
			// fallback gets its own budget instead of an already-expired context.
			terminateCtx, cancelTerminate := context.WithTimeout(context.WithoutCancel(ctx), detachStopTimeout)
			terminateErr := terminateDetachedProcessTree(terminateCtx, opts)
			cancelTerminate()
			if terminateErr != nil {
				stopErr = errors.Join(stopErr, terminateErr)
			}
			cancelAttach(stopErr)
			result := <-outcome
			result.StopReason = reason
			return result
		}
		// The stop request was delivered. The supervisor stops the task and the
		// attach delivers its exit record. Bound the wait so a stuck supervisor
		// (SIGSTOP, D state, an unresponsive waitForClose) cannot hold the run
		// finalize open forever.
		stopWatchCtx, cancelWatch := context.WithTimeout(context.WithoutCancel(ctx), detachStopTimeout)
		defer cancelWatch()
		select {
		case result := <-outcome:
			result.StopReason = reason
			return terminateAbnormalDetachedTask(ctx, opts, result)
		case <-stopWatchCtx.Done():
			terminateCtx, cancelTerminate := context.WithTimeout(context.WithoutCancel(ctx), detachStopTimeout)
			terminateErr := terminateDetachedProcessTree(terminateCtx, opts)
			cancelTerminate()
			timeoutErr := fmt.Errorf("detached execution did not stop within %s", detachStopTimeout)
			if terminateErr != nil {
				timeoutErr = errors.Join(timeoutErr, terminateErr)
			}
			cancelAttach(timeoutErr)
			result := <-outcome
			result.StopReason = reason
			return result
		}
	case result := <-outcome:
		return terminateAbnormalDetachedTask(ctx, opts, result)
	}
}

// terminateAbnormalDetachedTask ends the task's process tree by marker when the
// attach reported that the supervisor died without an exit record. The
// supervisor then cannot forward a stop request, so this is the only way to
// reach an orphaned task. It runs before the caller cleans the execution state
// up and gets its own budget: the run context may already be cancelled.
func terminateAbnormalDetachedTask(ctx context.Context, opts DetachedTask, result detachedExecutionResult) detachedExecutionResult {
	if !result.Abnormal {
		return result
	}
	terminateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), detachStopTimeout)
	defer cancel()
	if err := terminateDetachedProcessTree(terminateCtx, opts); err != nil {
		result.Err = errors.Join(result.Err, fmt.Errorf("terminate orphaned detached task: %w", err))
	}
	return result
}

// detachAttachShutdownWatcher cancels attachCtx when the daemon shutdown signal
// fires, and returns a stop function that ends the watcher goroutine.
func detachAttachShutdownWatcher(attachCtx context.Context, cancel context.CancelCauseFunc, runCtx context.Context) func() {
	shutdown, ok := DaemonShutdown(runCtx)
	if !ok {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-shutdown:
			cancel(ErrRunDisconnected)
		case <-attachCtx.Done():
		}
	}()
	return func() {
		cancel(nil)
		<-done
	}
}

// detachStopReason maps a cancelled run context to the stop reason to record.
func detachStopReason(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return execution.DetachStopReasonTimeout
	}
	return execution.DetachStopReasonUser
}

// detachControlExecErr reports whether a detach control-plane exec (start or
// stop) succeeded. The guest CLI expresses failure by exiting non-zero, while
// every driver returns a non-zero exit code with a nil error, so the exit code
// must be checked explicitly. The guest's own stderr carries the reason.
func detachControlExecErr(result domain.ExecResult, err error, op string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if result.ExitCode == 0 {
		return nil
	}
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Output)
	}
	if detail != "" {
		return fmt.Errorf("%s: guest exited %d: %s", op, result.ExitCode, detail)
	}
	return fmt.Errorf("%s: guest exited %d", op, result.ExitCode)
}

// detachIsShutdown reports whether ctx's cancellation is the daemon shutdown
// rather than a user stop or timeout. The fresh-run path cancels the exec
// context with ErrRunDisconnected when the shutdown signal fires; the re-attach
// path keeps the run context alive and carries the signal as a value, so both
// are checked.
func detachIsShutdown(ctx context.Context) bool {
	if cause := context.Cause(ctx); errors.Is(cause, ErrRunDisconnected) {
		return true
	}
	shutdown, ok := DaemonShutdown(ctx)
	if !ok {
		return false
	}
	select {
	case <-shutdown:
		return true
	default:
		return false
	}
}

// processTreeTerminator is the optional Runtime capability to terminate a
// detached task's whole guest process tree by marker once its supervisor can no
// longer be reached. driverRuntimeAdapter forwards it to drivers that support
// ProcessTreeTerminator; fakes and unsupported drivers simply do not satisfy it.
type processTreeTerminator interface {
	TerminateProcessTree(context.Context, *domain.Sandbox, domain.VMState, string) error
}

// terminateDetachedProcessTree best-effort terminates the task's guest process
// tree by execution marker. It is a no-op when the runtime does not support
// marker-based termination.
func terminateDetachedProcessTree(ctx context.Context, opts DetachedTask) error {
	terminator, ok := opts.Runtime.(processTreeTerminator)
	if !ok {
		return nil
	}
	return terminator.TerminateProcessTree(ctx, opts.Sandbox, opts.VMState, opts.ExecutionID)
}

// executeDetachedProjectRunCommand runs a project-run command task behind the
// detach supervisor and returns its reconstructed result. It records the
// execution id and detach decision on the run before starting, and persists
// the journal cursor as it follows the task so a restart resumes cleanly.
func (c *Controller) executeDetachedProjectRunCommand(ctx context.Context, input detachedRunCommand) (domain.ExecResult, error) {
	run := input.Run
	executionID := newDetachExecutionID(run)
	deadline, _ := ctx.Deadline()
	if err := c.persistDetachedRunFields(ctx, run, executionID, domain.DetachKindCommand, deadline); err != nil {
		return domain.ExecResult{}, fmt.Errorf("persist detached run fields: %w", err)
	}
	artifactsDir := projectRunCommandArtifactsDir(run, input.Sandbox)
	taskArgs := execution.RuntimeCommandCLIArgs(c.config, input.GuestRequestPath, c.config.GuestHomePath)
	env := execution.BuildSandboxExecEnv(ctx, c.config, input.Sandbox, c.config.GuestHomePath)
	env["AGENT_COMPOSE_RUN_ID"] = run.RunID
	env["AGENT_COMPOSE_PROJECT_ID"] = run.ProjectID

	result := executeDetachedCommand(ctx, DetachedTask{
		Config:      c.config,
		Runtime:     input.Runtime,
		Sandbox:     input.Sandbox,
		VMState:     input.VMState,
		ExecutionID: executionID,
		TaskArgs:    taskArgs,
		Env:         env,
		TaskTimeout: remainingTimeout(ctx),
		// A fresh start always begins at the journal start with a fresh
		// execution id; only re-attach resumes from a persisted cursor.
		Cursor: 0,
		Writer: input.Writer,
		PersistCursor: func(offset uint64) error {
			return writeDetachCursor(artifactsDir, offset)
		},
	})
	return result.Result, result.Err
}

// detachedRunCommand bundles the inputs for a detached project-run command so
// executeDetachedProjectRunCommand stays below the argument limit.
type detachedRunCommand struct {
	Runtime          Runtime
	Sandbox          *domain.Sandbox
	VMState          domain.VMState
	Run              domain.ProjectRunRecord
	GuestRequestPath string
	Writer           func(domain.ExecChunk)
}

// detachFollower parses the NDJSON records emitted by `attach` and reconstructs
// the task result while forwarding output chunks to the run log.
type detachFollower struct {
	writer  func(domain.ExecChunk)
	persist func(uint64) error
	sendErr error

	pending strings.Builder
	// queued holds the output parsed from the current chunk until its cursor
	// is persisted.
	queued []domain.ExecChunk
	exit   *execution.DetachAttachRecord
	// attachStderr keeps the head of the attach process's own stderr (Node
	// warnings, a crash trace). It is not journal data, so it is never parsed;
	// it only explains an attach that ended without an exit record.
	attachStderr strings.Builder

	stdout strings.Builder
	stderr strings.Builder
	output strings.Builder
	cursor uint64
	// forwardFrom is the offset below which output was already delivered by
	// an earlier daemon: it still counts towards the result but is not
	// forwarded or re-persisted.
	forwardFrom uint64
}

func newDetachFollower(writer func(domain.ExecChunk), persist func(uint64) error) *detachFollower {
	return &detachFollower{writer: writer, persist: persist}
}

// detachAttachStderrLimit bounds how much of the attach process's stderr is
// kept for error messages.
const detachAttachStderrLimit = 4096

func (f *detachFollower) consume(chunk domain.ExecChunk) {
	if chunk.Stream == domain.StdioStderr {
		if room := detachAttachStderrLimit - f.attachStderr.Len(); room > 0 {
			f.attachStderr.WriteString(truncateUTF8(chunk.Text, room))
		}
		return
	}
	if f.sendErr != nil || f.exit != nil {
		return
	}
	before := f.cursor
	f.pending.WriteString(chunk.Text)
	for {
		line, rest, ok := strings.Cut(f.pending.String(), "\n")
		if !ok {
			break
		}
		f.pending.Reset()
		f.pending.WriteString(rest)
		f.handleLine(line)
	}
	// Persist once per chunk rather than per record: attach batches every
	// record it read in one poll, so this is one cursor write per poll instead
	// of one per output line. As before, the cursor is written before the
	// output is forwarded, so a hard kill in between can drop this chunk from
	// the run log but never duplicates it on resume.
	if f.cursor != before {
		f.persistCursor()
	}
	queued := f.queued
	f.queued = nil
	for _, out := range queued {
		f.forward(out)
	}
}

func (f *detachFollower) handleLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	record, err := execution.ParseDetachAttachLine(line)
	if err != nil {
		f.sendErr = err
		return
	}
	switch {
	case record.IsExit():
		f.exit = &record
	case record.IsOut():
		f.appendOut(record)
	case record.Type == "gap":
		if record.To <= f.forwardFrom {
			return
		}
		if record.To > f.cursor {
			f.cursor = record.To
		}
		f.queued = append(f.queued, domain.ExecChunk{Stream: domain.StdioStderr, Text: fmt.Sprintf("\n[detach journal gap: %d..%d]\n", record.From, record.To)})
	}
}

func (f *detachFollower) appendOut(record execution.DetachAttachRecord) {
	stream := domain.StdioStream(record.Stream)
	if stream != domain.StdioStdout && stream != domain.StdioStderr {
		stream = domain.StdioStdout
	}
	if stream == domain.StdioStdout {
		f.stdout.WriteString(record.Text)
	} else {
		f.stderr.WriteString(record.Text)
	}
	f.output.WriteString(record.Text)
	end := record.Offset + uint64(len(record.Text))
	if end <= f.forwardFrom {
		return
	}
	text := record.Text
	if record.Offset < f.forwardFrom {
		text = text[f.forwardFrom-record.Offset:]
	}
	if end > f.cursor {
		f.cursor = end
	}
	f.queued = append(f.queued, domain.ExecChunk{Stream: stream, Text: text})
}

func (f *detachFollower) persistCursor() {
	if f.persist == nil || f.sendErr != nil {
		return
	}
	if err := f.persist(f.cursor); err != nil {
		f.sendErr = err
	}
}

func (f *detachFollower) forward(chunk domain.ExecChunk) {
	if f.writer != nil {
		f.writer(chunk)
	}
}

func (f *detachFollower) result(execErr error, attachCtx context.Context) detachedExecutionResult {
	if f.sendErr != nil {
		execErr = errors.Join(execErr, f.sendErr)
	}
	if f.exit == nil && execErr != nil {
		if detail := strings.TrimSpace(f.attachStderr.String()); detail != "" {
			execErr = fmt.Errorf("%w (attach stderr: %s)", execErr, detail)
		}
	}
	result := domain.ExecResult{
		Stdout: f.stdout.String(),
		Stderr: f.stderr.String(),
		Output: f.output.String(),
	}
	if f.exit == nil {
		result.ExitCode = -1
		if execErr != nil {
			cause := context.Cause(attachCtx)
			if errors.Is(cause, ErrRunDisconnected) {
				return detachedExecutionResult{Result: result, Err: ErrRunDisconnected}
			}
			// Surface a specific cancellation cause (e.g. a failed stop request)
			// rather than masking it behind the attach exec's context.Canceled.
			// Only the bare context errors are skipped: a stop request that
			// timed out wraps context.DeadlineExceeded but is still specific.
			if cause != nil && !isBareContextError(cause) {
				return detachedExecutionResult{Result: result, Err: cause}
			}
			return detachedExecutionResult{Result: result, Err: execErr}
		}
		return detachedExecutionResult{Result: result, Err: errors.New("detached execution ended without an exit record")}
	}
	exit := execution.DetachExitResultFor(*f.exit)
	mapped, exitErr := execution.ExecResultFromDetachExit(exit)
	mapped.Stdout = result.Stdout
	mapped.Stderr = result.Stderr
	mapped.Output = result.Output
	if exitErr == nil {
		mapped.Success = mapped.ExitCode == 0
	}
	stopReason := ""
	if exit.Reason == execution.DetachExitStopped {
		stopReason = exit.StopReason
	}
	return detachedExecutionResult{
		Result:     mapped,
		Abnormal:   exit.Reason == execution.DetachExitAbnormal,
		StopReason: stopReason,
		Err:        exitErr,
	}
}

// remainingTimeout returns the time left before ctx's deadline, or 0 when ctx
// has none.
func remainingTimeout(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	if remaining := time.Until(deadline); remaining > 0 {
		return remaining
	}
	return time.Millisecond
}

// ExecuteDetachedTask starts task behind the guest supervisor and follows it
// to completion. On daemon shutdown it returns ErrRunDisconnected and leaves
// the task running; the caller must then leave the run running too.
func ExecuteDetachedTask(ctx context.Context, task DetachedTask) (domain.ExecResult, error) {
	result := executeDetachedCommand(ctx, task)
	return result.Result, result.Err
}

// FollowDetachedTask re-attaches to a task started by an earlier daemon.
func FollowDetachedTask(ctx context.Context, task DetachedTask) (domain.ExecResult, error) {
	result := followDetachedCommand(ctx, task)
	return result.Result, result.Err
}

// RemainingTimeout returns the time left before ctx's deadline, or 0 when ctx
// has none. Callers hand it to the supervisor as DetachedTask.TaskTimeout.
func RemainingTimeout(ctx context.Context) time.Duration {
	return remainingTimeout(ctx)
}

// truncateUTF8 returns at most limit bytes of s without splitting a rune.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

// isBareContextError reports whether err is context.Canceled or
// context.DeadlineExceeded itself, not an error that merely wraps one.
func isBareContextError(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded //nolint:errorlint // identity is the point: wrapped causes carry detail.
}
