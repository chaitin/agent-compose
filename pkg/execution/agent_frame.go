package execution

import (
	"encoding/json"
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// AgentOutputEnv selects the prompt wire format without adding a CLI option
// older guests would reject. Older guests ignore it; their prefixed output is
// normalized by DecodeAgentFrame at the same boundary as native frames.
const AgentOutputEnv = "AGENT_COMPOSE_RUNTIME_OUTPUT"

// AgentFrame is the runtime's transport-neutral prompt envelope. Raw retains
// fields unknown to this daemon for forwarding and result persistence.
type AgentFrame struct {
	Type            string                      `json:"type"`
	Version         *int                        `json:"v,omitempty"`
	Seq             uint64                      `json:"seq,omitempty"`
	Event           json.RawMessage             `json:"event,omitempty"`
	Provider        string                      `json:"provider,omitempty"`
	ThreadID        string                      `json:"threadId,omitempty"`
	SessionID       string                      `json:"sessionId,omitempty"`
	SandboxID       string                      `json:"sandboxId,omitempty"`
	StopReason      string                      `json:"stopReason,omitempty"`
	FinalText       string                      `json:"finalText,omitempty"`
	FinalTextSource domain.AgentFinalTextSource `json:"finalTextSource,omitempty"`
	Transcript      string                      `json:"transcript,omitempty"`
	Stderr          string                      `json:"stderr,omitempty"`
	Code            string                      `json:"code,omitempty"`
	Message         string                      `json:"message,omitempty"`
	Raw             json.RawMessage             `json:"-"`
}

// DecodeAgentFrame recognizes NDJSON envelopes and the legacy result prefix
// that released guests without frame output still write. Ordinary JSON output without a type is not a frame. matched remains
// true on malformed protocol input so it cannot leak into visible output or be
// mistaken for a successful result. Versionless envelopes predate versioning.
func DecodeAgentFrame(line []byte) (frame AgentFrame, matched bool, err error) {
	raw := strings.TrimSpace(string(line))
	switch {
	case strings.HasPrefix(raw, AgentResultPrefix):
		matched = true
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimPrefix(raw, AgentResultPrefix)), &fields); err != nil {
			return frame, true, fmt.Errorf("decode legacy agent result: %w", err)
		}
		if fields == nil {
			return frame, true, fmt.Errorf("decode legacy agent result: expected JSON object")
		}
		fields["type"] = json.RawMessage(`"result"`)
		encoded, err := json.Marshal(fields)
		if err != nil {
			return frame, true, fmt.Errorf("normalize legacy agent result: %w", err)
		}
		raw = string(encoded)
	}
	if !matched {
		var header struct {
			Type    string          `json:"type"`
			Version json.RawMessage `json:"v"`
		}
		if json.Unmarshal([]byte(raw), &header) != nil || header.Type == "" {
			return frame, false, nil
		}
		if len(header.Version) == 0 && !isAgentFrameType(header.Type) {
			return frame, false, nil
		}
	}
	if err := json.Unmarshal([]byte(raw), &frame); err != nil {
		return frame, true, fmt.Errorf("decode agent frame: %w", err)
	}
	if frame.Version != nil && *frame.Version != 1 {
		return frame, true, fmt.Errorf("unsupported agent frame version %d", *frame.Version)
	}
	if frame.Type == "agent_event" && (len(frame.Event) == 0 || frame.Event[0] != '{') {
		return frame, true, fmt.Errorf("agent_event frame requires an event object")
	}
	frame.Raw = json.RawMessage(raw)
	return frame, true, nil
}

func isAgentFrameType(kind string) bool {
	switch kind {
	case "started", "agent_event", "agent_turn_completed", "result", "error", "stdout", "stderr", "output", "command":
		return true
	default:
		return false
	}
}
