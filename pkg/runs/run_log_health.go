package runs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

// Log health updates never replace an execution outcome. The narrow update is
// sticky so a concurrent status transition cannot clear an earlier log fault.
type runLogHealthStore interface {
	MarkProjectRunLogIncomplete(context.Context, string) error
	SaveProjectRunExecutionCheckpoint(context.Context, string, string) error
}

type runLogHealth struct {
	ctx         context.Context
	store       runLogHealthStore
	runID       string
	incomplete  bool
	marked      bool
	writeFailed bool
}

func runLogHealthFor(ctx context.Context, run domain.ProjectRunRecord, store any) runLogHealth {
	persistence, _ := store.(runLogHealthStore)
	return runLogHealth{ctx: ctx, store: persistence, runID: run.RunID, incomplete: run.LogIncomplete}
}

func (h *runLogHealth) record(err error) {
	if !h.incomplete {
		slog.Warn("run log incomplete", "run_id", h.runID, "error", err)
	}
	h.incomplete = true
	if h.store == nil || h.marked {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), 5*time.Second)
	defer cancel()
	if err := h.store.MarkProjectRunLogIncomplete(ctx, h.runID); err != nil {
		slog.Warn("persist run log health failed", "run_id", h.runID, "error", err)
		return
	}
	h.marked = true
}

func (p *agentRunProjector) bindStore(store any) {
	p.events, _ = store.(structuredEventStore)
	p.health = runLogHealthFor(p.eventCtx, p.run, store)
}

// Once a writer fails, freeze its prefix. Continuing to append after a partial
// write could make later records unreadable or assign misleading log sequences.
func (p *agentRunProjector) writeLog(write func(*runlog.Writer) error) {
	if p.logFailed {
		return
	}
	w, err := runLogWriter(p.eventCtx, p.run, p.runLogs)
	if err == nil {
		err = write(w)
	}
	if err != nil {
		p.logFailed = true
		p.health.record(err)
	}
}

// appendOutput isolates command log failures from output delivery. A fault
// freezes the log prefix, but later chunks still reach the caller.
func (h *runLogHealth) appendOutput(hub *RunLogHub, run domain.ProjectRunRecord, legacyPath string, chunk domain.ExecChunk) {
	var gap *domain.JournalGap
	if chunk.Journal != nil {
		gap = chunk.Journal.Gap
	}
	if gap != nil {
		h.record(fmt.Errorf("guest journal gap: %d..%d", gap.From, gap.To))
	}
	if h.writeFailed {
		return
	}
	var err error
	if gap != nil && usesRunLog(run) {
		var w *runlog.Writer
		w, err = runLogWriter(h.ctx, run, hub)
		if err == nil {
			err = w.AppendJournalGap(gap.From, gap.To)
		}
	} else {
		err = appendRunOutput(h.ctx, hub, run, legacyPath, chunk)
	}
	if err != nil {
		h.writeFailed = true
		h.record(err)
	}
}
