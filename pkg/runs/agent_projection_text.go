package runs

import (
	"encoding/json"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

// agentEventText derives the frame's name and human-readable text from a
// runtime agent event.
//
// The runtime publishes provider-neutral events: the frame name is the event
// kind, and the transcript is assembled from the kinds a reader needs to
// follow the run — assistant prose plus the tool activity that produced it.
// Reasoning deliberately contributes no text, so a consumer reading just that
// field never splices the model's thinking into the answer.
func (p *agentRunProjector) agentEventText(raw json.RawMessage) (string, string) {
	var event struct {
		Kind    string              `json:"kind"`
		Text    string              `json:"text"`
		Type    string              `json:"type"`
		ID      string              `json:"id"`
		Name    string              `json:"name"`
		Command string              `json:"command"`
		Input   json.RawMessage     `json:"input"`
		Changes []runlog.FileChange `json:"changes"`
		Output  string              `json:"output"`
		Error   string              `json:"error"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return "agent_event", ""
	}
	switch event.Kind {
	case "":
		// Legacy shape: a raw provider event with no neutral kind. Keep the
		// frame addressable but contribute nothing to the transcript.
		return firstNonEmpty(event.Type, "agent_event"), ""
	case "text_delta":
		return event.Kind, event.Text
	case "tool_call":
		// Rendered by the run log so this transcript and a run log's text
		// view show a tool call the same way.
		return event.Kind, p.newToolText(event.ID, runlog.Render(runlog.Entry{
			Type: runlog.EntryToolCall,
			Tool: &runlog.Tool{Name: event.Name, Command: event.Command, Input: event.Input, Changes: event.Changes},
		}))
	case "tool_result":
		return event.Kind, p.newToolText(event.ID+"\x00result", runlog.Render(runlog.Entry{
			Type: runlog.EntryToolResult,
			Tool: &runlog.Tool{Output: event.Output, Error: event.Error},
		}))
	default:
		return event.Kind, ""
	}
}

// newToolText returns only the part of text not already written under key.
//
// Tool events are announced more than once and carry cumulative payloads:
// claude re-emits a tool call once its arguments finish streaming, and codex
// resends a command's aggregated output on every update. Without this the
// transcript would repeat each tool's header and output.
func (p *agentRunProjector) newToolText(key, text string) string {
	if text == "" {
		return ""
	}
	previous := p.itemTexts[key]
	p.itemTexts[key] = text
	if previous != "" && strings.HasPrefix(text, previous) {
		return text[len(previous):]
	}
	return text
}

// appendLogText writes text to the transcript, tracking assistant prose
// separately from tool activity.
//
// appendLogFinalText reconciles the turn's final text against what the run
// already streamed, and it does that by prefix. Only assistant prose can serve
// as that prefix: tool headers, commands and output are interleaved into the
// same transcript but never appear in FinalText, so counting them would break
// the comparison and silently drop the final text's tail.
func (p *agentRunProjector) appendLogText(text string, prose bool) error {
	if text == "" {
		return nil
	}
	if err := p.appendLogChunk(domain.ExecChunk{Text: text}); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if prose {
		p.loggedProse += text
	}
	p.turnText += text
	return nil
}

// appendLogFinalText appends the part of the turn's final text that never
// reached the transcript as a text_delta.
//
// Providers that deliver the answer only on the terminal frame land here whole;
// providers that streamed it land here with nothing left to write. Both the
// turn-completed and the result frame carry the same final text, so the second
// one finds the first's work already done and appends nothing.
func (p *agentRunProjector) appendLogFinalText(finalText string) error {
	if finalText == "" {
		return nil
	}
	p.mu.Lock()
	text := finalText[streamedFinalTextOverlap(p.loggedProse, finalText):]
	p.mu.Unlock()
	if text == "" {
		return nil
	}
	if err := p.appendLogChunk(domain.ExecChunk{Text: text}); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loggedProse += text
	p.turnText += text
	return nil
}

// streamedFinalTextOverlap returns the length of the longest prefix of
// finalText that the prose logged so far ends with.
//
// Requiring finalText to be prefixed by the whole prose log would only hold
// for the first turn of a run that never used a tool: tool activity splits a
// turn's prose in the transcript, and every turn after the first is preceded
// by another turn's prose. Matching the overlap instead keeps the reconciliation
// anchored on what this turn actually streamed, so an unstreamed tail still
// lands in the log.
//
// The overlap is computed with the KMP prefix function over
// finalText + sentinel + the tail of logged, keeping the cost linear in
// len(finalText). The sentinel is only assumed to be rare, not absent, so the
// result is verified before it is trusted.
func streamedFinalTextOverlap(logged, finalText string) int {
	if finalText == "" || logged == "" {
		return 0
	}
	if len(logged) > len(finalText) {
		logged = logged[len(logged)-len(finalText):]
	}
	combined := finalText + "\x00" + logged
	failure := make([]int, len(combined))
	for i := 1; i < len(combined); i++ {
		length := failure[i-1]
		for length > 0 && combined[i] != combined[length] {
			length = failure[length-1]
		}
		if combined[i] == combined[length] {
			length++
		}
		failure[i] = length
	}
	overlap := failure[len(combined)-1]
	if overlap > len(finalText) || !strings.HasSuffix(logged, finalText[:overlap]) {
		return 0
	}
	return overlap
}

func (p *agentRunProjector) appendLogChunk(chunk domain.ExecChunk) error {
	offset, err := appendProjectRunLogChunk(p.logsPath, chunk)
	if err != nil {
		p.health.record(err)
		return nil
	}
	if chunk.Text != "" {
		p.mu.Lock()
		p.hasLoggedText = true
		p.logEndsWithNewline = strings.HasSuffix(chunk.Text, "\n")
		p.mu.Unlock()
	}
	publishRunLogChunk(p.runLogs, p.run, chunk, offset)
	return nil
}

func promptAttachHumanLogText(message string) string {
	if strings.TrimSpace(message) == "" {
		return ""
	}
	if strings.HasSuffix(message, "\n") {
		return message
	}
	return message + "\n"
}
