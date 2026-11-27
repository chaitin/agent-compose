package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

// agentRunProjector owns the lifecycle of agent turns independently of exec or
// interactive transport. A completed turn has one log boundary and one message;
// result closes the execution, not a second copy of its last turn.
// Frames have one ordered producer. Human input may arrive from the input pump;
// serial orders whole operations, and mu protects state only, never I/O.
type agentRunProjector struct {
	run       domain.ProjectRunRecord
	sandbox   *domain.Sandbox
	logsPath  string
	runLogs   *RunLogHub
	eventCtx  context.Context
	events    structuredEventStore
	health    runLogHealth
	logFailed bool

	serial             chan struct{}
	decoder            execution.AgentOutputDecoder
	journal            bool
	sourceOffset       uint64
	processExit        *domain.JournalExit
	mu                 sync.Mutex
	itemTexts          map[string]string
	loggedProse        string
	turnText           string
	hasLoggedText      bool
	logEndsWithNewline bool
	humanIndex         uint64
	turnEnded          bool
	turnNotified       bool
	turnEndSeq         uint64
	turnMessages       []domain.ProjectRunEventRecord
	pendingMessages    []domain.ProjectRunEventRecord
	messageProjected   bool
	terminal           *TransitionRequest
	lastFrameSeq       uint64
	hasFrameSeq        bool
	// progress is closed after each journal record, for human input waiting
	// for the decoder to reach a line boundary; see AppendHumanMessageFrame.
	progress chan struct{}
	// heldText flushes prose the merger holds once the agent pauses
	// mid-paragraph; see scheduleHeldTextFlush. finished stops it.
	heldText *time.Timer
	finished bool
}

// agentProjection contains use-case effects, leaving transport response mapping
// to the attach adapter. One-shot execution only needs Terminal.
type agentProjection struct {
	FrameType     string
	Code          string
	Name          string
	Text          string
	Payload       []byte
	TurnCompleted bool
	Terminal      *TransitionRequest
	Ignored       bool
}

func newAgentRunProjector(ctx context.Context, run domain.ProjectRunRecord, sandbox *domain.Sandbox, logsPath string, hub *RunLogHub) *agentRunProjector {
	return &agentRunProjector{eventCtx: ctx, run: run, sandbox: sandbox, logsPath: logsPath, runLogs: hub,
		itemTexts: map[string]string{}, serial: make(chan struct{}, 1), health: runLogHealth{ctx: ctx, runID: run.RunID, incomplete: run.LogIncomplete}}
}

func (p *agentRunProjector) ProjectFrame(frame execution.AgentFrame) (agentProjection, error) {
	p.serial <- struct{}{}
	defer func() { <-p.serial }()
	effect, err := p.projectFrameOnce(frame)
	if err != nil {
		return effect, err
	}
	return effect, p.commitJournal()
}

func (p *agentRunProjector) projectFrameOnce(frame execution.AgentFrame) (agentProjection, error) {
	p.mu.Lock()
	sequenced := frame.Version != nil || frame.Seq != 0
	ignored := p.terminal != nil || (sequenced && p.hasFrameSeq && frame.Seq <= p.lastFrameSeq)
	p.mu.Unlock()
	if ignored {
		return agentProjection{Ignored: true}, nil
	}
	if len(frame.Raw) == 0 {
		encoded, err := json.Marshal(frame)
		if err != nil {
			return agentProjection{}, fmt.Errorf("encode agent frame: %w", err)
		}
		frame.Raw = encoded
	}
	projection, err := p.projectFrame(frame)
	if err != nil {
		return projection, err
	}
	p.mu.Lock()
	if sequenced {
		p.lastFrameSeq, p.hasFrameSeq = frame.Seq, true
	}
	var terminal *TransitionRequest
	if projection.Terminal != nil {
		copied := *projection.Terminal
		copied.TerminalEvents = append([]domain.ProjectRunEventRecord(nil), copied.TerminalEvents...)
		p.terminal, terminal = &copied, &copied
	}
	p.mu.Unlock()
	if terminal != nil {
		// The terminal frame is behind the commit a restart resumes from, so
		// its outcome is kept rather than replayed.
		if err := p.appendRecord(agentTerminalRecord, terminal); err != nil {
			return projection, err
		}
	}
	return projection, nil
}

func (p *agentRunProjector) projectFrame(frame execution.AgentFrame) (agentProjection, error) {
	out := agentProjection{Name: frame.Type, FrameType: frame.Type, Code: frame.Code, Payload: frame.Raw}
	switch frame.Type {
	case "agent_event":
		p.mu.Lock()
		closed := p.turnEnded
		name, text := p.agentEventText(frame.Event)
		p.mu.Unlock()
		if closed {
			return agentProjection{Ignored: true}, nil
		}
		if err := p.appendAgentEvent(frame.Event, text, name == "text_delta"); err != nil {
			return out, err
		}
		out.Name, out.Text, out.Payload = firstNonEmpty(name, "agent_event"), text, frame.Event
	case "agent_turn_completed", "result":
		p.mu.Lock()
		duplicateTurn := p.turnNotified && frame.Type == "agent_turn_completed"
		p.mu.Unlock()
		if duplicateTurn {
			return agentProjection{Ignored: true}, nil
		}
		if err := p.closeTurn(frame); err != nil {
			return out, err
		}
		if frame.Type == "agent_turn_completed" {
			if err := p.persistTurnMessage(); err != nil {
				return out, err
			}
			p.mu.Lock()
			p.turnNotified = true
			p.mu.Unlock()
			out.TurnCompleted = true
			return out, nil
		}
		transition := transitionFromAgentFrame(p.run, p.sandbox, agentFrameOutcome{
			LogsPath: p.logsPath, Payload: frame.Raw, FinalText: frame.FinalText, StopReason: frame.StopReason,
		})
		transition.TerminalEvents = p.pendingTurnMessages()
		out.Terminal = &transition
	case "error":
		message := firstNonEmpty(frame.Message, "runtime stream error")
		event, err := json.Marshal(map[string]string{"kind": "error", "message": message, "code": frame.Code})
		if err != nil {
			return out, fmt.Errorf("encode agent runtime error: %w", err)
		}
		if err := p.appendAgentEvent(event, "", false); err != nil {
			return out, err
		}
		if _, err := p.endTurn(firstNonEmpty(frame.StopReason, "error"), "", "", nil); err != nil {
			return out, err
		}
		transition := transitionFromAgentFrame(p.run, p.sandbox, agentFrameOutcome{
			LogsPath: p.logsPath, Payload: frame.Raw, StopReason: frame.StopReason, Message: message,
		})
		// Completed turns remain valid even if a later turn fails. An unfinished
		// turn's diagnostics never become an assistant-authored message.
		transition.TerminalEvents = p.pendingTurnMessages()
		out.Text, out.Terminal = message, &transition
	}
	return out, nil
}

func (p *agentRunProjector) closeTurn(frame execution.AgentFrame) error {
	p.mu.Lock()
	if !p.messageProjected {
		turn := agentTurnProjection{Transcript: firstNonEmpty(frame.Transcript, p.turnText), FinalText: frame.FinalText,
			FinalTextSource: frame.FinalTextSource, Provider: frame.Provider, StopReason: frame.StopReason}
		if !strings.EqualFold(strings.TrimSpace(frame.StopReason), "cancelled") {
			if frame.Type == "agent_turn_completed" {
				p.turnMessages = attachedAgentTurnEvents(p.run, frame.Seq, frame.Raw, turn)
			} else {
				p.turnMessages = terminalPromptTurnEvents(p.run, turn)
			}
		}
		p.pendingMessages = append(p.pendingMessages, p.turnMessages...)
		p.messageProjected = true
	}
	// A completed interactive turn persists its messages before the record
	// that closed it commits; a run's last turn persists them with the run.
	control, err := json.Marshal(agentTurnControl{Messages: p.turnMessages, Persisted: frame.Type == "agent_turn_completed"})
	p.mu.Unlock()
	if err != nil {
		return fmt.Errorf("encode agent turn end: %w", err)
	}
	seq, err := p.endTurn(frame.StopReason, frame.FinalText, frame.FinalTextSource, control)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.turnMessages {
		p.turnMessages[i].LogSeq = seq
		for j := range p.pendingMessages {
			if p.pendingMessages[j].ID == p.turnMessages[i].ID {
				p.pendingMessages[j].LogSeq = seq
			}
		}
	}
	return nil
}

func (p *agentRunProjector) endTurn(stopReason, finalText string, source domain.AgentFinalTextSource, control json.RawMessage) (uint64, error) {
	p.mu.Lock()
	closed, seq := p.turnEnded, p.turnEndSeq
	p.mu.Unlock()
	if closed {
		return seq, nil
	}
	if usesRunLog(p.run) {
		p.writeLog(func(w *runlog.Writer) error {
			// The runtime may deliver the answer only on the terminal frame.
			// Record the part the turn never streamed as prose, so the text
			// view has it; a streamed answer leaves nothing to append. A
			// transcript fallback is the transcript itself, already in the log.
			tail := ""
			if finalText != "" && source != domain.AgentFinalTextSourceTranscriptFallback {
				p.mu.Lock()
				tail = finalText[streamedFinalTextOverlap(p.loggedProse, finalText):]
				p.mu.Unlock()
			}
			if tail != "" {
				event, err := json.Marshal(map[string]string{"kind": "text_delta", "text": tail})
				if err != nil {
					return err
				}
				if err := w.AppendAgentEvent(event); err != nil {
					return err
				}
			}
			entry, err := w.EndTurn(stopReason, finalText, control)
			if err == nil {
				seq = entry.Seq
			}
			return err
		})
	} else if source != domain.AgentFinalTextSourceTranscriptFallback {
		if err := p.appendLogFinalText(finalText); err != nil {
			return 0, err
		}
	}
	p.mu.Lock()
	p.turnEnded, p.turnEndSeq = true, seq
	p.mu.Unlock()
	return seq, nil
}

func (p *agentRunProjector) persistTurnMessage() error {
	events := p.pendingTurnMessages()
	if p.events == nil || len(events) == 0 {
		return nil
	}
	if _, _, err := p.events.AppendProjectRunEvents(p.eventCtx, events); err != nil {
		return err
	}
	persisted := make(map[string]struct{}, len(events))
	for _, event := range events {
		persisted[event.ID] = struct{}{}
	}
	p.mu.Lock()
	p.pendingMessages = slices.DeleteFunc(p.pendingMessages, func(event domain.ProjectRunEventRecord) bool { _, ok := persisted[event.ID]; return ok })
	p.mu.Unlock()
	return nil
}

func (p *agentRunProjector) pendingTurnMessages() []domain.ProjectRunEventRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]domain.ProjectRunEventRecord(nil), p.pendingMessages...)
}
