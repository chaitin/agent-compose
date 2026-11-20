package execution

import (
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

const (
	// AgentEventPrefix marks one neutral agent event line on a
	// non-interactive agent's stdout (runtime/javascript/src/constants.ts).
	AgentEventPrefix = "__AGENT_EVENT__"
	// AgentEventsEnv asks the guest runtime to write AgentEventPrefix lines.
	// A runtime that predates it ignores the variable and keeps mirroring its
	// text transcript to stderr instead.
	AgentEventsEnv = "AGENT_COMPOSE_RUNTIME_EVENTS"
)

// AgentStdoutSplitter separates the runtime's agent event lines from the rest
// of a non-interactive agent's output. Event lines can span chunks, so a
// partial stdout line is held until its newline arrives — only while it could
// still turn out to be an event line.
type AgentStdoutSplitter struct {
	partial strings.Builder
}

// Split returns chunk without its event lines, and the JSON of each complete
// event line. Stderr passes through unchanged.
func (s *AgentStdoutSplitter) Split(chunk domain.ExecChunk) (domain.ExecChunk, [][]byte) {
	if domain.NormalizeStdioStream(chunk.Stream) == domain.StdioStderr {
		return chunk, nil
	}
	s.partial.WriteString(chunk.Text)
	buffered := s.partial.String()
	s.partial.Reset()
	var visible strings.Builder
	var events [][]byte
	for buffered != "" {
		line, rest, complete := strings.Cut(buffered, "\n")
		if !complete {
			if mayBecomeEventLine(line) {
				s.partial.WriteString(line)
			} else {
				visible.WriteString(line)
			}
			break
		}
		if raw, ok := strings.CutPrefix(line, AgentEventPrefix); ok {
			events = append(events, []byte(strings.TrimRight(raw, "\r")))
		} else {
			visible.WriteString(line)
			visible.WriteByte('\n')
		}
		buffered = rest
	}
	filtered := chunk
	filtered.Text = visible.String()
	return filtered, events
}

// Flush returns a held partial line that never became an event line.
func (s *AgentStdoutSplitter) Flush() string {
	rest := s.partial.String()
	s.partial.Reset()
	if strings.HasPrefix(rest, AgentEventPrefix) {
		return ""
	}
	return rest
}

func mayBecomeEventLine(partial string) bool {
	return strings.HasPrefix(partial, AgentEventPrefix) || strings.HasPrefix(AgentEventPrefix, partial)
}

// StripAgentEventLines removes event lines from a complete output, for the
// aggregated stdout an exec result carries alongside its streamed chunks.
func StripAgentEventLines(text string) string {
	if !strings.Contains(text, AgentEventPrefix) {
		return text
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(text, "\n") {
		if strings.HasPrefix(line, AgentEventPrefix) {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}
