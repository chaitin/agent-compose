package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

// openRunLogs keeps one writer per run log while its run executes, so every
// path appending to a run (output, agent events, human messages, turn ends)
// shares one sequence. Writers are closed when the run becomes terminal.
var openRunLogs = &runLogFiles{}

// initialPromptLogSeq is the run log entry of a run's initial prompt: the
// writer records it first, so the conversation message created with the run
// can point at it before the log exists.
const initialPromptLogSeq = 1

// usesRunLog reports whether the run records its history in a run log rather
// than a legacy text transcript.
func usesRunLog(run domain.ProjectRunRecord) bool {
	return run.LogFormat == runlog.FormatJSONL && strings.TrimSpace(run.LogsPath) != ""
}

// runLogMaxBytes caps a project run's log; tests lower it.
var runLogMaxBytes = runlog.DefaultRunLogMaxBytes

// runLogWriter returns the run's open writer, opening it on first use. The
// first time a log is opened empty, the run's prompt becomes entry 1.
func runLogWriter(ctx context.Context, run domain.ProjectRunRecord, hub *RunLogHub) (*runlog.Writer, error) {
	path := strings.TrimSpace(run.LogsPath)
	return openRunLogs.writer(ctx, path, func() (*runlog.Writer, error) {
		return openRunLog(run, hub)
	})
}

func openRunLog(run domain.ProjectRunRecord, hub *RunLogHub) (*runlog.Writer, error) {
	path := strings.TrimSpace(run.LogsPath)
	w, err := runlog.Open(path, runlog.Options{MaxBytes: runLogMaxBytes, OnAppend: func(entry runlog.Entry, rendered string) {
		if hub == nil {
			return
		}
		_ = hub.Publish(RunLogEvent{
			RunID:     run.RunID,
			ProjectID: run.ProjectID,
			SandboxID: run.SandboxID,
			Stream:    entry.Stream,
			Data:      rendered,
			Offset:    entry.TextOffset + uint64(len(rendered)),
			CreatedAt: entry.Time,
			Seq:       entry.Seq,
		})
	}})
	if err != nil {
		return nil, err
	}
	if w.Seq() == 0 && strings.TrimSpace(run.Prompt) != "" {
		if _, err := w.AppendInitialPrompt(run.Prompt); err != nil {
			return nil, errors.Join(err, w.Close())
		}
	}
	return w, nil
}

// closeRunLog flushes and closes the run's writer if it is open.
func closeRunLog(ctx context.Context, path string) error {
	return openRunLogs.close(ctx, strings.TrimSpace(path))
}

// appendRunOutput records process output for a run: as an output entry in
// its run log, or appended to its legacy text transcript.
func appendRunOutput(ctx context.Context, hub *RunLogHub, run domain.ProjectRunRecord, legacyPath string, chunk domain.ExecChunk) error {
	if chunk.Text == "" {
		return nil
	}
	if !usesRunLog(run) {
		offset, err := appendProjectRunLogChunk(legacyPath, chunk)
		if err != nil {
			return err
		}
		publishRunLogChunk(hub, run, chunk, offset)
		return nil
	}
	w, err := runLogWriter(ctx, run, hub)
	if err != nil {
		return err
	}
	return w.AppendOutput(runLogStream(chunk.Stream), chunk.Text)
}

func runLogStream(stream domain.StdioStream) string {
	if stream == "tty" {
		// A terminal merges both streams into one byte stream.
		return "tty"
	}
	if domain.NormalizeStdioStream(stream) == domain.StdioStderr {
		return "stderr"
	}
	return "stdout"
}

// finalizeRunLog closes a terminal run's writer and folds its log into the
// run's summary. A missing log leaves the summary empty: the run failed
// before it produced anything.
func finalizeRunLog(ctx context.Context, run *domain.ProjectRunRecord) error {
	if run == nil || !usesRunLog(*run) {
		return nil
	}
	// Terminal cleanup must still flush the writer after its execution request
	// is canceled. Ordinary callers can cancel waits in runLogWriter/closeRunLog.
	closeErr := closeRunLog(context.WithoutCancel(ctx), run.LogsPath)
	summary, err := runlog.SummarizeLog(run.LogsPath)
	if errors.Is(err, os.ErrNotExist) {
		return closeErr
	}
	if err != nil {
		return errors.Join(closeErr, err)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return errors.Join(closeErr, fmt.Errorf("encode run summary: %w", err))
	}
	run.SummaryJSON = string(encoded)
	return closeErr
}

// runLogRoot is where new runs keep their run logs: the daemon data root.
func (c *Controller) runLogRoot() string {
	if c == nil || c.config == nil {
		return ""
	}
	return strings.TrimSpace(c.config.DataRoot)
}
