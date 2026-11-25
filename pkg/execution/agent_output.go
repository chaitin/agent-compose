package execution

import (
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

const (
	// Bound an unfinished protocol line independently of process chunk sizes.
	maxAgentFrameBytes = 32 << 20
)

// AgentOutput is one ordered frame or visible chunk from runtime stdout.
type AgentOutput struct {
	Frame *AgentFrame
	Chunk domain.ExecChunk
}

// AgentOutputDecoder separates protocol lines from process output. The decoder
// owns partial stdout lines; stderr is always diagnostic output, even if it
// happens to contain JSON or a protocol marker.
type AgentOutputDecoder struct {
	partial strings.Builder
	plain   bool
	discard bool
}

// Push delivers frames and visible bytes in source order. Ordinary text can be
// forwarded without waiting for a newline. Protocol candidates are held until
// complete, including when a frame spans multiple exec or journal chunks.
func (d *AgentOutputDecoder) Push(chunk domain.ExecChunk, emit func(AgentOutput) error) error {
	if domain.NormalizeStdioStream(chunk.Stream) == domain.StdioStderr {
		return emit(AgentOutput{Chunk: chunk})
	}
	text := chunk.Text
	for text != "" {
		if d.discard {
			part, rest, complete := strings.Cut(text, "\n")
			if d.partial.Len()+len(part) <= maxAgentFrameBytes {
				d.partial.WriteString(part)
			} else {
				d.partial.Reset()
			}
			if !complete {
				return nil
			}
			frame, matched, err := DecodeAgentFrame([]byte(d.partial.String()))
			d.partial.Reset()
			d.discard = false
			if err == nil && matched {
				if err := emit(AgentOutput{Frame: &frame}); err != nil {
					return err
				}
			}
			text = rest
			continue
		}
		part, rest, complete := strings.Cut(text, "\n")
		if complete {
			part += "\n"
		}
		if d.partial.Len() == 0 && (d.plain || !mayBecomeAgentFrame(part)) {
			d.plain = !complete
			out := chunk
			out.Text = part
			if err := emit(AgentOutput{Chunk: out}); err != nil {
				return err
			}
		} else {
			if d.partial.Len()+len(part) > maxAgentFrameBytes {
				return fmt.Errorf("agent protocol line exceeds %d bytes", maxAgentFrameBytes)
			}
			d.partial.WriteString(part)
			if complete {
				if err := d.emitLine(chunk, emit); err != nil {
					return err
				}
			}
		}
		text = rest
	}
	return nil
}

// Flush handles a final line without a newline. An incomplete legacy result
// marker is an error, rather than visible user output.
func (d *AgentOutputDecoder) Flush(emit func(AgentOutput) error) error {
	if d.discard {
		d.partial.Reset()
		return nil
	}
	if d.partial.Len() == 0 {
		return nil
	}
	return d.emitLine(domain.ExecChunk{Stream: domain.StdioStdout}, emit)
}

func (d *AgentOutputDecoder) emitLine(chunk domain.ExecChunk, emit func(AgentOutput) error) error {
	line := d.partial.String()
	d.partial.Reset()
	frame, matched, err := DecodeAgentFrame([]byte(line))
	if err != nil {
		return err
	}
	if matched {
		return emit(AgentOutput{Frame: &frame})
	}
	trimmed := strings.TrimSpace(line)
	if trimmed != "" && strings.HasPrefix(AgentResultPrefix, trimmed) {
		return fmt.Errorf("incomplete agent protocol marker")
	}
	chunk.Text = line
	return emit(AgentOutput{Chunk: chunk})
}

func mayBecomeAgentFrame(part string) bool {
	part = strings.TrimLeft(part, " \t\r\n")
	return part == "" || strings.HasPrefix(part, "{") ||
		strings.HasPrefix(part, AgentResultPrefix) || strings.HasPrefix(AgentResultPrefix, part)
}

// StripAgentProtocolLines removes envelopes from aggregated stdout using the
// same compatibility boundary as streamed output. Malformed marked lines are
// hidden as well; ParseAgentExecResult reports the missing/invalid result.
func StripAgentProtocolLines(text string) string {
	var visible strings.Builder
	for _, line := range strings.SplitAfter(text, "\n") {
		// Sanitization only needs classification; malformed marked payloads
		// stay hidden and result parsing reports the missing valid result.
		_, matched, _ := DecodeAgentFrame([]byte(line))
		if !matched {
			visible.WriteString(line)
		}
	}
	return visible.String()
}

// AgentDecoderState preserves a stdout line split across journal records.
type AgentDecoderState struct {
	Pending string `json:"pending,omitempty"`
	Plain   bool   `json:"plain,omitempty"`
	Discard bool   `json:"discard,omitempty"`
}

func (d *AgentOutputDecoder) Snapshot() AgentDecoderState {
	return AgentDecoderState{Pending: d.partial.String(), Plain: d.plain, Discard: d.discard}
}

func (d *AgentOutputDecoder) Restore(state AgentDecoderState) error {
	if len(state.Pending) > maxAgentFrameBytes {
		return fmt.Errorf("saved agent protocol line exceeds %d bytes", maxAgentFrameBytes)
	}
	d.partial.Reset()
	d.partial.WriteString(state.Pending)
	d.plain, d.discard = state.Plain, state.Discard
	return nil
}

// DiscardGap abandons an interrupted stdout line after journal retention loss.
// A valid complete frame at the next boundary is accepted; a broken
// continuation is discarded up to its newline before parsing resumes.
func (d *AgentOutputDecoder) DiscardGap() {
	d.partial.Reset()
	d.plain = false
	d.discard = true
}
