package runs

import (
	"context"
	"errors"
	"fmt"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"strings"
)

// detachFollower parses the NDJSON records emitted by `attach` and reconstructs
// the task result while forwarding output chunks to the run log.
type detachFollower struct {
	journal bool
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
		if f.sendErr != nil || f.exit != nil {
			break
		}
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
		// An abnormal record means the supervisor disappeared; the task tree
		// may still need termination. Do not let a restart bypass that cleanup.
		if f.journal && record.Reason != string(execution.DetachExitAbnormal) {
			result, err := execution.ExecResultFromDetachExit(execution.DetachExitResultFor(record))
			exit := &domain.JournalExit{Result: result, Cancelled: errors.Is(err, context.Canceled)}
			if err != nil {
				exit.Error = err.Error()
			}
			f.queued = append(f.queued, domain.ExecChunk{Journal: &domain.JournalPosition{End: f.cursor, Exit: exit}})
		}
	case record.IsOut():
		f.appendOut(record)
	case record.Type == "gap":
		if record.To <= f.forwardFrom {
			return
		}
		if record.To > f.cursor {
			f.cursor = record.To
		}
		f.queued = append(f.queued, domain.ExecChunk{Stream: domain.StdioStderr, Text: fmt.Sprintf("\n[detach journal gap: %d..%d]\n", record.From, record.To), Journal: &domain.JournalPosition{End: record.To, Gap: &domain.JournalGap{From: record.From, To: record.To}}})
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
	f.queued = append(f.queued, domain.ExecChunk{Stream: stream, Text: text, Journal: &domain.JournalPosition{End: end}})
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
	if !f.journal && (chunk.Journal == nil || chunk.Journal.Gap == nil) {
		chunk.Journal = nil
	}
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
		Err:        errors.Join(exitErr, execErr),
	}
}
