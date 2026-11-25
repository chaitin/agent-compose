package execution

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestAgentOutputDecoderNormalizesBothProtocolsInOrder(t *testing.T) {
	for name, tc := range map[string]struct {
		protocol string
		want     []string
	}{
		// Released guests without frame output write only the result line.
		"legacy": {"__AGENT_RESULT__{\"finalText\":\"answer\"}\r\n", []string{"before\n", "result", "after\n"}},
		"frames": {"{\"v\":1,\"seq\":1,\"type\":\"agent_event\",\"event\":{\"kind\":\"text_delta\",\"text\":\"answer\"}}\r\n{\"v\":1,\"seq\":2,\"type\":\"result\",\"finalText\":\"answer\"}\n",
			[]string{"before\n", "agent_event", "", "result", "after\n"}},
	} {
		protocol := tc.protocol
		t.Run(name, func(t *testing.T) {
			input := "before\n" + protocol + "after\n"
			for _, chunkSize := range []int{1, 7, len(input)} {
				var decoder AgentOutputDecoder
				var parts []string
				var visible strings.Builder
				emit := func(out AgentOutput) error {
					if out.Frame == nil {
						visible.WriteString(out.Chunk.Text)
						return nil
					}
					parts = append(parts, visible.String(), out.Frame.Type)
					visible.Reset()
					if out.Frame.Type == "agent_event" && string(out.Frame.Event) != `{"kind":"text_delta","text":"answer"}` {
						t.Fatalf("event=%s", out.Frame.Event)
					}
					if out.Frame.Type == "result" && out.Frame.FinalText != "answer" {
						t.Fatalf("result=%+v", out.Frame)
					}
					return nil
				}
				for i := 0; i < len(input); i += chunkSize {
					if err := decoder.Push(domain.ExecChunk{Text: input[i:min(i+chunkSize, len(input))]}, emit); err != nil {
						t.Fatal(err)
					}
				}
				if err := decoder.Flush(emit); err != nil {
					t.Fatal(err)
				}
				parts = append(parts, visible.String())
				if want := tc.want; !reflect.DeepEqual(parts, want) {
					t.Fatalf("chunkSize=%d parts=%q", chunkSize, parts)
				}
			}
		})
	}
}

func TestAgentOutputDecoderPreservesDiagnosticsAndLineBoundaries(t *testing.T) {
	var decoder AgentOutputDecoder
	var visible strings.Builder
	emit := func(out AgentOutput) error {
		if out.Frame != nil {
			t.Fatalf("diagnostic became frame: %+v", out.Frame)
		}
		visible.WriteString(out.Chunk.Text)
		return nil
	}
	for _, chunk := range []domain.ExecChunk{
		{Text: "ordinary "},
		{Text: "__AGENT_RESULT__{}\n"},
		{Text: "{\"count\":1}\n\n"},
		{Text: "{\"type\":\"metric\",\"value\":1}\n"},
		{Text: "__AGENT_RESULT__{}\n", Stream: domain.StdioStderr},
		{Text: "tail"},
	} {
		if err := decoder.Push(chunk, emit); err != nil {
			t.Fatal(err)
		}
	}
	if err := decoder.Flush(emit); err != nil {
		t.Fatal(err)
	}
	if want := "ordinary __AGENT_RESULT__{}\n{\"count\":1}\n\n{\"type\":\"metric\",\"value\":1}\n__AGENT_RESULT__{}\ntail"; visible.String() != want {
		t.Fatalf("output=%q", visible.String())
	}
}

func TestAgentOutputDecoderRejectsMalformedProtocol(t *testing.T) {
	for _, input := range []string{
		`__AGENT_RESULT__null`, "__AGENT_RES",
		`{"v":2,"type":"result"}`, `{"type":"agent_event","event":null}`,
	} {
		var decoder AgentOutputDecoder
		emit := func(AgentOutput) error { t.Fatalf("leaked malformed input %q", input); return nil }
		if err := decoder.Push(domain.ExecChunk{Text: input}, emit); err != nil {
			t.Fatal(err)
		}
		if err := decoder.Flush(emit); err == nil {
			t.Fatalf("accepted malformed input %q", input)
		}
	}
}

func TestAgentOutputDecoderBoundsPartialFrames(t *testing.T) {
	var decoder AgentOutputDecoder
	emit := func(AgentOutput) error { t.Fatal("partial frame was forwarded"); return nil }
	if err := decoder.Push(domain.ExecChunk{Text: "{" + strings.Repeat("x", maxAgentFrameBytes)}, emit); err == nil {
		t.Fatal("accepted oversized protocol line")
	}
}

func TestAgentOutputDecoderFlushesTerminalFrameWithoutNewline(t *testing.T) {
	for _, input := range []string{
		`__AGENT_RESULT__{"finalText":"done"}`,
		`{"v":1,"seq":0,"type":"result","finalText":"done"}`,
	} {
		var decoder AgentOutputDecoder
		var got *AgentFrame
		emit := func(out AgentOutput) error { got = out.Frame; return nil }
		if err := decoder.Push(domain.ExecChunk{Text: input}, emit); err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatal("unterminated frame was emitted before flush")
		}
		if err := decoder.Flush(emit); err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Type != "result" || got.FinalText != "done" {
			t.Fatalf("terminal frame=%+v", got)
		}
	}
}

func TestAgentOutputDecoderPropagatesConsumerFailure(t *testing.T) {
	failure := errors.New("consumer failed")
	for _, input := range []string{"text\n", "__AGENT_RESULT__{}\n"} {
		var decoder AgentOutputDecoder
		err := decoder.Push(domain.ExecChunk{Text: input}, func(AgentOutput) error { return failure })
		if !errors.Is(err, failure) {
			t.Fatalf("error=%v, want consumer failure", err)
		}
	}
}

func TestAgentResultParsingDoesNotConfuseEventsWithResults(t *testing.T) {
	for _, input := range []string{
		`{"v":1,"seq":0,"type":"agent_turn_completed","finalText":"answer"}`,
		`{"v":1,"seq":1,"type":"error","message":"failed"}`,
		`{"unrelated":true}`,
	} {
		if _, err := ParseAgentExecResult("codex", domain.ExecResult{Stdout: input, Success: true}); err == nil {
			t.Fatalf("accepted non-result %s", input)
		}
	}
}

func TestAgentResultParsingAndSanitizingBothProtocols(t *testing.T) {
	for _, prefix := range []string{AgentResultPrefix, `{"v":1,"seq":2,"type":"result",`} {
		payload := `{"provider":"codex","finalText":"done","stopReason":"completed"}`
		if strings.HasPrefix(prefix, "{") {
			payload = payload[1:]
		}
		raw := "diagnostic\n" + `{"v":1,"seq":0,"type":"started"}` + "\n" + prefix + payload + "\n"
		parsed, err := ParseAgentExecResult("codex", domain.ExecResult{Stdout: raw, Success: true})
		if err != nil || parsed.FinalText != "done" || !parsed.Success {
			t.Fatalf("parsed=%+v err=%v", parsed, err)
		}
		cleaned := SanitizeAgentExecResult(domain.ExecResult{Stdout: raw, Output: raw, Stderr: "warning"})
		if cleaned.Stdout != "diagnostic\n" || cleaned.Output != "diagnostic\n" || cleaned.Stderr != "warning" {
			t.Fatalf("cleaned=%+v", cleaned)
		}
	}
}
