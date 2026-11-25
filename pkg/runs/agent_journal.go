package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

// agentJournalState is the lifecycle state paired with one durable log prefix.
// The merger recovers tool identities from that prefix; decoder state preserves
// frames split between journal records, even across intervening stderr records.
type agentJournalState struct {
	LogIncomplete    bool                           `json:"log_incomplete,omitempty"`
	ProcessExit      *domain.JournalExit            `json:"process_exit,omitempty"`
	Version          int                            `json:"v"`
	Decoder          execution.AgentDecoderState    `json:"decoder"`
	ItemTexts        map[string]string              `json:"item_texts,omitempty"`
	LoggedProse      string                         `json:"logged_prose,omitempty"`
	TurnText         string                         `json:"turn_text,omitempty"`
	HumanIndex       uint64                         `json:"human_index,omitempty"`
	TurnEnded        bool                           `json:"turn_ended,omitempty"`
	TurnNotified     bool                           `json:"turn_notified,omitempty"`
	TurnEndSeq       uint64                         `json:"turn_end_seq,omitempty"`
	TurnMessages     []domain.ProjectRunEventRecord `json:"turn_messages,omitempty"`
	PendingMessages  []domain.ProjectRunEventRecord `json:"pending_messages,omitempty"`
	MessageProjected bool                           `json:"message_projected,omitempty"`
	Terminal         *TransitionRequest             `json:"terminal,omitempty"`
	LastFrameSeq     uint64                         `json:"last_frame_seq,omitempty"`
	HasFrameSeq      bool                           `json:"has_frame_seq,omitempty"`
}

// RestoreJournal must run before opening the guest attach. A missing checkpoint
// identifies runs from older daemons, which keep their legacy recovery boundary.
func (p *agentRunProjector) RestoreJournal() (*execution.AgentJournalResume, error) {
	if !usesRunLog(p.run) {
		return nil, nil
	}
	var recovery *runlog.Recovery
	if p.run.ExecutionCheckpointJSON != "" {
		if err := json.Unmarshal([]byte(p.run.ExecutionCheckpointJSON), &recovery); err != nil {
			return nil, fmt.Errorf("decode independent execution checkpoint: %w", err)
		}
		p.logFailed = true
	} else {
		p.writeLog(func(w *runlog.Writer) error { recovery = w.Recovery(); return nil })
	}
	if recovery == nil {
		return nil, nil
	}
	var state agentJournalState
	if err := json.Unmarshal(recovery.State, &state); err != nil {
		return nil, fmt.Errorf("decode agent lifecycle checkpoint: %w", err)
	}
	if state.Version != 1 {
		return nil, fmt.Errorf("unsupported agent lifecycle checkpoint version %d", state.Version)
	}
	if err := p.decoder.Restore(state.Decoder); err != nil {
		return nil, err
	}
	p.health.incomplete = p.health.incomplete || state.LogIncomplete
	p.itemTexts = state.ItemTexts
	if p.itemTexts == nil {
		p.itemTexts = map[string]string{}
	}
	p.loggedProse, p.turnText = state.LoggedProse, state.TurnText
	p.humanIndex, p.turnEnded, p.turnNotified = state.HumanIndex, state.TurnEnded, state.TurnNotified
	p.turnEndSeq, p.turnMessages, p.pendingMessages = state.TurnEndSeq, state.TurnMessages, state.PendingMessages
	p.messageProjected, p.terminal = state.MessageProjected, state.Terminal
	p.lastFrameSeq, p.hasFrameSeq = state.LastFrameSeq, state.HasFrameSeq
	p.journal, p.sourceOffset, p.processExit = true, recovery.SourceOffset, state.ProcessExit
	resume := &execution.AgentJournalResume{Offset: recovery.SourceOffset, Decoder: state.Decoder, Exit: state.ProcessExit}
	if p.terminal != nil {
		resume.ResultJSON = p.terminal.ResultJSON
	}
	return resume, nil
}

func (p *agentRunProjector) checkpointJournal() error {
	if !p.journal {
		return nil
	}
	p.mu.Lock()
	state := agentJournalState{Version: 1, LogIncomplete: p.health.incomplete, ProcessExit: p.processExit, Decoder: p.decoder.Snapshot(), ItemTexts: p.itemTexts,
		LoggedProse: p.loggedProse, TurnText: p.turnText, HumanIndex: p.humanIndex,
		TurnEnded: p.turnEnded, TurnNotified: p.turnNotified, TurnEndSeq: p.turnEndSeq,
		TurnMessages: p.turnMessages, PendingMessages: p.pendingMessages,
		MessageProjected: p.messageProjected, Terminal: p.terminal,
		LastFrameSeq: p.lastFrameSeq, HasFrameSeq: p.hasFrameSeq}
	// Marshal while holding only the state lock; no I/O occurs here.
	data, err := json.Marshal(state)
	p.mu.Unlock()
	if err != nil {
		return fmt.Errorf("encode agent lifecycle checkpoint: %w", err)
	}
	p.writeLog(func(w *runlog.Writer) error { return w.Checkpoint(p.sourceOffset, data) })
	if !p.logFailed || p.health.store == nil {
		return nil
	}
	checkpoint, err := json.Marshal(runlog.Recovery{SourceOffset: p.sourceOffset, State: data})
	if err != nil {
		return fmt.Errorf("encode independent execution checkpoint: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(p.eventCtx), 5*time.Second)
	defer cancel()
	// This is execution state, not observability: failure must not acknowledge
	// the journal because the next daemon would otherwise lose lifecycle effects.
	return p.health.store.SaveProjectRunExecutionCheckpoint(ctx, p.run.RunID, string(checkpoint))
}

// ProjectRuntimeOutput is shared by exec and interactive attach. A journal
// record is acknowledged only after its log, decoder and lifecycle effects are
// committed together. serial orders whole operations, including human input;
// the state mutex is never held across I/O.
func (p *agentRunProjector) ProjectRuntimeOutput(chunk domain.ExecChunk) ([]agentProjection, error) {
	p.serial <- struct{}{}
	defer func() { <-p.serial }()
	if chunk.Journal != nil && usesRunLog(p.run) {
		if !p.journal {
			p.journal = true
			if err := p.checkpointJournal(); err != nil {
				return nil, err
			}
		}
		p.sourceOffset = chunk.Journal.End
		p.writeLog(func(w *runlog.Writer) error { w.SetSourceOffset(p.sourceOffset); return nil })
		if exit := chunk.Journal.Exit; exit != nil {
			if err := p.flushRuntimeOutput(); err != nil {
				return nil, err
			}
			p.processExit = exit
			return nil, p.checkpointJournal()
		}
		if gap := chunk.Journal.Gap; gap != nil {
			p.decoder.DiscardGap()
			p.health.record(fmt.Errorf("guest journal gap: %d..%d", gap.From, gap.To))
			p.writeLog(func(w *runlog.Writer) error { return w.AppendJournalGap(gap.From, gap.To) })
			return nil, p.checkpointJournal()
		}
	}
	var effects []agentProjection
	err := p.decoder.Push(chunk, func(output execution.AgentOutput) error {
		if output.Frame == nil {
			return p.appendOutput(output.Chunk)
		}
		effect, err := p.projectFrameOnce(*output.Frame)
		if err == nil && !effect.Ignored {
			effects = append(effects, effect)
		}
		return err
	})
	if err != nil {
		return effects, err
	}
	return effects, p.checkpointJournal()
}

// completeCheckpointedAgentRun handles a crash after process exit was committed
// but before the run's terminal database transaction. The guest journal may
// already have been cleaned up, so confirmed local completion precedes runtime
// health checks and never requires another attach.
func (c *Controller) completeCheckpointedAgentRun(ctx context.Context, runCtx context.Context, run domain.ProjectRunRecord, sandbox *domain.Sandbox) (domain.ProjectRunRecord, bool, error) {
	if !usesRunLog(run) || (run.DetachKind != domain.DetachKindAgent && run.DetachKind != domain.DetachKindInteractivePrompt) {
		return run, false, nil
	}
	p := newAgentRunProjector(ctx, run, sandbox, run.LogsPath, c.runLogs)
	p.bindStore(c.configDB)
	resume, err := p.RestoreJournal()
	if err != nil {
		return run, true, err
	}
	if resume == nil || resume.Exit == nil {
		return run, false, nil
	}
	c.deleteRetainedFacadeTokens(ctx, run)
	exit := resume.Exit
	var execErr error
	if exit.Error != "" {
		execErr = errors.New(exit.Error)
	}
	if exit.Cancelled {
		execErr = errors.Join(execErr, context.Canceled)
	}
	transition, execErr := p.CompleteExecution(exit.Result, execErr)
	if execErr != nil || transition.ExitCode != 0 {
		finished, err := c.completeProjectRunError(ctx, runCtx, transition, execErr)
		return finished, true, err
	}
	transition.Status = domain.ProjectRunStatusSucceeded
	finished, err := c.completeProjectRun(ctx, transition)
	return finished, true, err
}

func (p *agentRunProjector) FlushRuntimeOutput() error {
	p.serial <- struct{}{}
	defer func() { <-p.serial }()
	if err := p.flushRuntimeOutput(); err != nil {
		return err
	}
	return p.checkpointJournal()
}

func (p *agentRunProjector) flushRuntimeOutput() error {
	return p.decoder.Flush(func(output execution.AgentOutput) error {
		if output.Frame == nil {
			return p.appendOutput(output.Chunk)
		}
		_, err := p.projectFrameOnce(*output.Frame)
		return err
	})
}
