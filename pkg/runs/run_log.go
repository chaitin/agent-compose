package runs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

// openRunLogs keeps one writer per run log while its run executes, so every
// path appending to a run (output, agent events, human messages, turn ends)
// shares one sequence. Writers are closed when the run becomes terminal.
var openRunLogs = &runLogFiles{open: map[string]*runlog.Writer{}}

type runLogFiles struct {
	mu   sync.Mutex
	open map[string]*runlog.Writer
}

// initialPromptLogSeq is the run log entry of a run's initial prompt: the
// writer records it first, so the conversation message created with the run
// can point at it before the log exists.
const initialPromptLogSeq = 1

// usesRunLog reports whether the run records its history in a run log rather
// than a legacy text transcript.
func usesRunLog(run domain.ProjectRunRecord) bool {
	return run.LogFormat == runlog.FormatJSONL && strings.TrimSpace(run.LogsPath) != ""
}

// runLogWriter returns the run's open writer, opening it on first use. The
// first time a log is opened empty, the run's prompt becomes entry 1.
func runLogWriter(run domain.ProjectRunRecord, hub *RunLogHub) (*runlog.Writer, error) {
	path := strings.TrimSpace(run.LogsPath)
	openRunLogs.mu.Lock()
	defer openRunLogs.mu.Unlock()
	if w := openRunLogs.open[path]; w != nil {
		return w, nil
	}
	runID := run.RunID
	w, err := runlog.Open(path, runlog.Options{OnAppend: func(entry runlog.Entry, rendered string) {
		if hub == nil {
			return
		}
		_ = hub.Publish(RunLogEvent{
			RunID:     runID,
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
			_ = w.Close()
			return nil, err
		}
	}
	openRunLogs.open[path] = w
	return w, nil
}

// closeRunLog flushes and closes the run's writer if it is open.
func closeRunLog(path string) error {
	path = strings.TrimSpace(path)
	openRunLogs.mu.Lock()
	w := openRunLogs.open[path]
	delete(openRunLogs.open, path)
	openRunLogs.mu.Unlock()
	if w == nil {
		return nil
	}
	return w.Close()
}

// appendRunOutput records process output for a run: as an output entry in
// its run log, or appended to its legacy text transcript.
func appendRunOutput(hub *RunLogHub, run domain.ProjectRunRecord, legacyPath string, chunk domain.ExecChunk) error {
	if chunk.Text == "" {
		return nil
	}
	if !usesRunLog(run) {
		offset, err := appendProjectRunLogChunk(legacyPath, chunk)
		if err != nil {
			return err
		}
		publishRunLogChunk(hub, run.RunID, chunk, offset)
		return nil
	}
	w, err := runLogWriter(run, hub)
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

// endRunLogTurn closes the run's current turn and points the turn's agent
// message, if one is among events, at the turn end entry.
func endRunLogTurn(hub *RunLogHub, run domain.ProjectRunRecord, stopReason, finalText string, events []domain.ProjectRunEventRecord) error {
	if !usesRunLog(run) {
		return nil
	}
	w, err := runLogWriter(run, hub)
	if err != nil {
		return err
	}
	entry, err := w.EndTurn(stopReason, finalText)
	if err != nil {
		return err
	}
	for i := range events {
		if events[i].Kind == domain.ProjectRunEventKindAgentMessage && events[i].LogSeq == 0 {
			events[i].LogSeq = entry.Seq
		}
	}
	return nil
}

// finalizeRunLog closes a terminal run's writer and folds its log into the
// run's summary. A missing log leaves the summary empty: the run failed
// before it produced anything.
func finalizeRunLog(run *domain.ProjectRunRecord) error {
	if run == nil || !usesRunLog(*run) {
		return nil
	}
	closeErr := closeRunLog(run.LogsPath)
	entries, err := runlog.ReadEntries(run.LogsPath, 0)
	if errors.Is(err, os.ErrNotExist) {
		return closeErr
	}
	if err != nil {
		return errors.Join(closeErr, err)
	}
	encoded, err := json.Marshal(runlog.Summarize(entries))
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
