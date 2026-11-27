package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

// A detached agent run's projector consumes the guest journal and commits its
// run log after the records it processed (runlog.Writer.Commit). After a
// daemon restart the log is rolled back to its last commit, the projector is
// rebuilt from the committed entries (rebuildFromLog) and the journal is read
// again from the commit's offset. Only two facts cannot be rebuilt from
// entries, the process exit and the terminal outcome, and they are kept as
// records.

// agentCommitState is what the projector keeps on a commit: how the decoder
// continues at the commit's journal offset.
type agentCommitState struct {
	Decoder execution.AgentDecoderState `json:"decoder"`
}

// agentTurnControl is what a turn_end entry keeps for the projector: the
// conversation messages the turn produced, and whether they were persisted
// with the turn, as a completed interactive turn's are, rather than with the
// run's terminal transition.
type agentTurnControl struct {
	Messages  []domain.ProjectRunEventRecord `json:"messages,omitempty"`
	Persisted bool                           `json:"persisted,omitempty"`
}

const (
	agentExitRecord     = "exit"
	agentTerminalRecord = "terminal"
)

// humanInputQuiescenceTimeout bounds how long human input waits for the
// agent's output to reach a line boundary before it is refused.
var humanInputQuiescenceTimeout = 30 * time.Second

// RestoreJournal must run before opening the guest attach. A run whose log has
// no commit (one from an older daemon, or one that stopped before its first
// journal record) keeps the legacy recovery boundary.
func (p *agentRunProjector) RestoreJournal() (*execution.AgentJournalResume, error) {
	if !usesRunLog(p.run) {
		return nil, nil
	}
	var resume *runlog.Resume
	p.writeLog(func(w *runlog.Writer) error { resume = w.Resume(); return nil })
	if resume == nil {
		return nil, nil
	}
	var state agentCommitState
	if len(resume.State) > 0 {
		if err := json.Unmarshal(resume.State, &state); err != nil {
			return nil, fmt.Errorf("decode agent run log commit: %w", err)
		}
	}
	if err := p.rebuildFromLog(); err != nil {
		return nil, err
	}
	if raw := resume.Records[agentExitRecord]; raw != nil {
		var exit domain.JournalExit
		if err := json.Unmarshal(raw, &exit); err != nil {
			return nil, fmt.Errorf("decode agent process exit record: %w", err)
		}
		p.processExit = &exit
	}
	if raw := resume.Records[agentTerminalRecord]; raw != nil {
		var terminal TransitionRequest
		if err := json.Unmarshal(raw, &terminal); err != nil {
			return nil, fmt.Errorf("decode agent terminal record: %w", err)
		}
		p.terminal = &terminal
	}
	p.decoder.Restore(state.Decoder)
	p.journal, p.sourceOffset = true, resume.Offset
	out := &execution.AgentJournalResume{Offset: resume.Offset, Decoder: state.Decoder, Exit: p.processExit}
	if p.terminal != nil {
		out.ResultJSON = p.terminal.ResultJSON
	}
	return out, nil
}

// rebuildFromLog derives the projector's turn state from the committed log,
// the same way the live projector built it while appending those entries.
func (p *agentRunProjector) rebuildFromLog() error {
	var (
		humanIndex        uint64
		turnEnded         bool
		turnEndSeq        uint64
		prose, transcript strings.Builder
		turnMessages      []domain.ProjectRunEventRecord
		pending           []domain.ProjectRunEventRecord
		decodeErr         error
	)
	err := runlog.ScanEntries(strings.TrimSpace(p.run.LogsPath), func(entry runlog.Entry) bool {
		switch entry.Type {
		case runlog.EntryUser:
			if entry.Initial {
				return true
			}
			humanIndex++
			if turnEnded {
				prose.Reset()
				transcript.Reset()
			}
			turnEnded, turnEndSeq, turnMessages = false, 0, nil
		case runlog.EntryTurnEnd:
			turnEnded, turnEndSeq, turnMessages = true, entry.Seq, nil
			if len(entry.Control) == 0 {
				return true
			}
			var control agentTurnControl
			if err := json.Unmarshal(entry.Control, &control); err != nil {
				decodeErr = fmt.Errorf("decode agent turn end %d: %w", entry.Seq, err)
				return false
			}
			for _, message := range control.Messages {
				message.LogSeq = entry.Seq
				turnMessages = append(turnMessages, message)
				if !control.Persisted {
					pending = append(pending, message)
				}
			}
		case runlog.EntryText:
			prose.WriteString(entry.Text)
			transcript.WriteString(entry.Text)
		default:
			transcript.WriteString(runlog.Render(entry))
		}
		return true
	})
	if err == nil {
		err = decodeErr
	}
	if err != nil {
		return fmt.Errorf("rebuild agent run from its log: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.humanIndex, p.turnEnded, p.turnEndSeq = humanIndex, turnEnded, turnEndSeq
	// A committed turn end follows its messages' persistence, and a duplicate
	// completion of a closed turn is ignored as before the restart.
	p.turnNotified, p.messageProjected = turnEnded, turnEnded
	p.turnMessages, p.pendingMessages = turnMessages, pending
	p.loggedProse, p.turnText = prose.String(), transcript.String()
	p.itemTexts = map[string]string{}
	return nil
}

// commitJournal makes the effects of the journal records processed so far
// durable where a restart can rebuild the projector from the log: the decoder
// holds no protocol line waiting for its newline. Otherwise the next record
// commits, and a restart replays from the previous commit.
func (p *agentRunProjector) commitJournal() error {
	_, err := p.commitJournalNow()
	return err
}

func (p *agentRunProjector) commitJournalNow() (bool, error) {
	if !p.journal || p.logFailed || !p.decoder.Quiescent() {
		return false, nil
	}
	state, err := json.Marshal(agentCommitState{Decoder: p.decoder.Snapshot()})
	if err != nil {
		return false, fmt.Errorf("encode agent run log commit: %w", err)
	}
	committed := false
	p.writeLog(func(w *runlog.Writer) error {
		var err error
		committed, err = w.Commit(p.sourceOffset, state)
		return err
	})
	return committed, nil
}

// appendRecord keeps a fact a restart cannot rebuild from entries. Only a
// journaled run is ever rebuilt.
func (p *agentRunProjector) appendRecord(name string, value any) error {
	if !p.journal || !usesRunLog(p.run) {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode agent %s record: %w", name, err)
	}
	p.writeLog(func(w *runlog.Writer) error { return w.AppendRecord(name, data) })
	return nil
}

// signalProgress wakes human input waiting for the decoder to reach a line
// boundary.
func (p *agentRunProjector) signalProgress() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.progress != nil {
		close(p.progress)
	}
	p.progress = make(chan struct{})
}

func (p *agentRunProjector) progressSignal() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.progress == nil {
		p.progress = make(chan struct{})
	}
	return p.progress
}

// ProjectRuntimeOutput is shared by exec and interactive attach. A journal
// record is acknowledged only after its log and lifecycle effects are
// committed together. serial orders whole operations, including human input;
// the state mutex is never held across I/O.
func (p *agentRunProjector) ProjectRuntimeOutput(chunk domain.ExecChunk) ([]agentProjection, error) {
	p.serial <- struct{}{}
	defer func() { <-p.serial }()
	defer p.signalProgress()
	if chunk.Journal != nil && usesRunLog(p.run) {
		if !p.journal {
			p.journal = true
			// The first commit precedes anything a journal record produces,
			// so the prompt and whatever came before stay committed.
			if err := p.commitJournal(); err != nil {
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
			if err := p.appendRecord(agentExitRecord, exit); err != nil {
				return nil, err
			}
			// Nothing follows the exit: what the merger still holds came from
			// the records before it, so the exit commits with all of it.
			p.writeLog(func(w *runlog.Writer) error { return w.Settle() })
			return nil, p.commitJournal()
		}
		if gap := chunk.Journal.Gap; gap != nil {
			p.decoder.DiscardGap()
			p.health.record(fmt.Errorf("guest journal gap: %d..%d", gap.From, gap.To))
			p.writeLog(func(w *runlog.Writer) error { return w.AppendJournalGap(gap.From, gap.To) })
			return nil, p.commitJournal()
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
	return effects, p.commitJournal()
}

// completeCommittedAgentRun handles a crash after process exit was committed
// but before the run's terminal database transaction. The guest journal may
// already have been cleaned up, so confirmed local completion precedes runtime
// health checks and never requires another attach.
func (c *Controller) completeCommittedAgentRun(ctx context.Context, runCtx context.Context, run domain.ProjectRunRecord, sandbox *domain.Sandbox) (domain.ProjectRunRecord, bool, error) {
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
	defer p.signalProgress()
	if err := p.flushRuntimeOutput(); err != nil {
		return err
	}
	return p.commitJournal()
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
