package adapters

import (
	"fmt"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

func (r *agentCellRun) writeChunk(chunk domain.ExecChunk) {
	if r.stream.OnRuntimeOutput != nil {
		if err := r.stream.OnRuntimeOutput(chunk); err != nil {
			r.setStreamErr(err)
			return
		}
	}
	if chunk.Journal != nil && chunk.Journal.Gap != nil {
		r.stdout.DiscardGap()
		return
	}
	if err := r.stdout.Push(chunk, r.writeAgentOutput); err != nil {
		r.setStreamErr(err)
	}
}

// flushChunks emits a held partial stdout line and the transcript text
// still held for coalescing once the exec has ended.
func (r *agentCellRun) flushChunks() {
	if r.stream.OnRuntimeEnd != nil {
		if err := r.stream.OnRuntimeEnd(); err != nil {
			r.setStreamErr(err)
		}
	}
	if err := r.stdout.Flush(r.writeAgentOutput); err != nil {
		r.setStreamErr(err)
	}
	if r.transcript != nil {
		r.writeTranscript(r.transcript.FlushText())
	}
}

// writeAgentEvent rebuilds the text transcript the runtime no longer mirrors
// to stderr; writeAgentOutput forwards the original frame separately. The cell
// output, and the run output derived from it, read as they did before.
func (r *agentCellRun) writeAgentEvent(event []byte) {
	if r.transcript == nil {
		r.transcript = runlog.NewMerger(0)
	}
	// The text compatibility view is best effort. Even if it cannot render
	// an event, writeAgentOutput forwards it to the structured consumer.
	if pending, err := r.transcript.Event(event); err == nil {
		r.writeTranscript(pending)
	}
}

// writeAgentOutput sees the same envelopes for native and legacy guests.
// Cell lifecycle still follows the exec result, so a turn-completed/result
// pair must not finish it twice or expose protocol JSON as process output.
func (r *agentCellRun) writeAgentOutput(output execution.AgentOutput) error {
	if output.Frame == nil {
		r.writeVisibleChunk(output.Chunk)
		return r.deferredStreamErr()
	}
	frame := output.Frame
	if frame.Type == "agent_event" {
		r.writeAgentEvent(frame.Event)
	}
	r.cellMu.Lock()
	cellID := r.cell.ID
	r.cellMu.Unlock()
	if r.stream.OnRuntimeOutput != nil {
		// Lifecycle effects were projected from the original runtime chunk.
	} else if r.stream.OnFrame != nil {
		if err := r.stream.OnFrame(cellID, *frame); err != nil {
			return err
		}
	} else if frame.Type == "agent_event" && r.stream.OnAgentEvent != nil {
		if err := r.stream.OnAgentEvent(cellID, frame.Event); err != nil {
			return err
		}
	}
	if frame.Type == "error" {
		return fmt.Errorf("agent runtime %s: %s", frame.Code, frame.Message)
	}
	return r.deferredStreamErr()
}

func (r *agentCellRun) writeTranscript(pending []runlog.Pending) {
	for _, p := range pending {
		if text := runlog.Render(p.Entry); text != "" {
			r.writeVisibleChunk(domain.ExecChunk{Text: text, Stream: domain.StdioStderr, Transcript: true})
		}
	}
}

func (r *agentCellRun) writeVisibleChunk(chunk domain.ExecChunk) {
	if chunk.Text == "" {
		return
	}
	// The decoder already classified these bytes using complete stdout lines.
	// Reclassifying a fragment could hide a marker quoted inside ordinary text.
	isStderr := domain.NormalizeStdioStream(chunk.Stream) == domain.StdioStderr
	r.cellMu.Lock()
	r.streamed.WriteChunk(chunk)
	if isStderr {
		r.cell.Stderr += chunk.Text
	} else {
		r.cell.Stdout += chunk.Text
	}
	r.cell.Output += chunk.Text
	snapshot := r.cell
	r.cellMu.Unlock()
	if r.history == execution.AgentHistoryNotebook {
		if err := r.e.store.AddCell(r.ctx, r.session, snapshot); err != nil {
			r.setStreamErr(err)
			return
		}
		if r.e.streams != nil {
			r.e.streams.PublishCellOutput(r.session.Summary.ID, snapshot.ID, chunk.Text, chunk.Stream)
		}
	}
	if r.stream.OnChunk != nil {
		if err := r.stream.OnChunk(snapshot.ID, chunk); err != nil {
			r.setStreamErr(err)
		}
	}
}
