package runs

import (
	"errors"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

// AppendHumanMessageFrame records human input before it is sent to the agent.
// A journaled run commits it on its own first: the journal cannot replay
// input that came from the host. A commit needs the decoder at a line
// boundary, so the input waits for the agent's output to reach one.
func (p *agentRunProjector) AppendHumanMessageFrame(message, clientFrameID string) error {
	if err := p.acquireQuiescent(); err != nil {
		return err
	}
	defer func() { <-p.serial }()
	if err := p.appendHumanMessageFrame(message, clientFrameID); err != nil {
		return err
	}
	committed, err := p.commitJournalNow()
	if err != nil {
		return err
	}
	if p.journal && !p.logFailed && !committed {
		return errors.New("human message could not be committed to the run log")
	}
	return nil
}

// acquireQuiescent takes serial once the decoder holds no partial protocol
// line, or the run needs no commit.
func (p *agentRunProjector) acquireQuiescent() error {
	timeout := time.NewTimer(humanInputQuiescenceTimeout)
	defer timeout.Stop()
	for {
		progress := p.progressSignal()
		p.serial <- struct{}{}
		if !p.journal || p.logFailed || p.decoder.Quiescent() {
			return nil
		}
		<-p.serial
		select {
		case <-progress:
		case <-timeout.C:
			return errors.New("human message not sent: the agent's output did not reach a line boundary")
		case <-p.eventCtx.Done():
			return p.eventCtx.Err()
		}
	}
}

func (p *agentRunProjector) appendHumanMessageFrame(message, clientFrameID string) error {
	if strings.TrimSpace(message) == "" {
		return nil
	}
	clientFrameID = strings.TrimSpace(clientFrameID)
	p.mu.Lock()
	if p.terminal != nil {
		p.mu.Unlock()
		return nil
	}
	if p.turnEnded {
		p.loggedProse, p.turnText = "", ""
	}
	p.itemTexts = map[string]string{}
	p.turnEnded, p.turnEndSeq = false, 0
	p.turnNotified = false
	p.turnMessages, p.messageProjected = nil, false
	p.humanIndex++
	humanIndex := p.humanIndex
	separator := p.hasLoggedText && !p.logEndsWithNewline
	p.mu.Unlock()
	var logSeq uint64
	if usesRunLog(p.run) {
		p.writeLog(func(w *runlog.Writer) error {
			entry, err := w.AppendUser(message)
			if err == nil {
				logSeq = entry.Seq
			}
			return err
		})
	} else {
		text := promptAttachHumanLogText(message)
		if separator {
			text = "\n" + text
		}
		if err := p.appendLogChunk(domain.ExecChunk{Text: text}); err != nil {
			return err
		}
	}
	if p.events != nil {
		if _, _, err := p.events.AppendProjectRunEvent(p.eventCtx, domain.ProjectRunEventRecord{
			ID: attachedHumanEventID(p.run.RunID, clientFrameID, humanIndex, message), RunID: p.run.RunID,
			Kind: domain.ProjectRunEventKindUserMessage, Text: message, Agent: p.run.AgentName, LogSeq: logSeq,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (p *agentRunProjector) AppendOutput(chunk domain.ExecChunk) error {
	p.serial <- struct{}{}
	defer func() { <-p.serial }()
	if err := p.appendOutput(chunk); err != nil {
		return err
	}
	return p.commitJournal()
}

func (p *agentRunProjector) appendOutput(chunk domain.ExecChunk) error {
	if chunk.Text == "" {
		return nil
	}
	if usesRunLog(p.run) {
		p.writeLog(func(w *runlog.Writer) error { return w.AppendOutput(runLogStream(chunk.Stream), chunk.Text) })
	} else if err := p.appendLogChunk(chunk); err != nil {
		return err
	}
	p.mu.Lock()
	p.turnText += chunk.Text
	p.mu.Unlock()
	return nil
}

func (p *agentRunProjector) appendAgentEvent(event []byte, text string, prose bool) error {
	if !usesRunLog(p.run) {
		return p.appendLogText(text, prose)
	}
	p.writeLog(func(w *runlog.Writer) error { return w.AppendAgentEvent(event) })
	p.mu.Lock()
	if prose {
		p.loggedProse += text
	}
	p.turnText += text
	p.mu.Unlock()
	return nil
}
