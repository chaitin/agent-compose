package execution

import (
	"reflect"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestAgentStdoutSplitterSeparatesEventLinesAcrossChunks(t *testing.T) {
	var s AgentStdoutSplitter
	var visible string
	var events []string
	feed := func(text string, stream domain.StdioStream) {
		out, ev := s.Split(domain.ExecChunk{Text: text, Stream: stream})
		visible += out.Text
		for _, e := range ev {
			events = append(events, string(e))
		}
	}
	feed("hello\n__AGENT_EV", domain.StdioStdout)
	feed(`ENT__{"kind":"text_delta","text":"a"}`+"\n", domain.StdioStdout)
	feed("diag on stderr", domain.StdioStderr)
	feed("plain partial", domain.StdioStdout)
	feed(" line\n__AGENT_EVENT__{\"kind\":\"usage\"}\r\n__AGENT_RESULT__{}\n", domain.StdioStdout)
	visible += s.Flush()

	if visible != "hello\ndiag on stderrplain partial line\n__AGENT_RESULT__{}\n" {
		t.Fatalf("visible = %q", visible)
	}
	if want := []string{`{"kind":"text_delta","text":"a"}`, `{"kind":"usage"}`}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v", events)
	}
}

func TestAgentStdoutSplitterDropsAnUnfinishedEventLine(t *testing.T) {
	var s AgentStdoutSplitter
	if out, _ := s.Split(domain.ExecChunk{Text: `__AGENT_EVENT__{"kind":`}); out.Text != "" {
		t.Fatalf("visible = %q", out.Text)
	}
	if rest := s.Flush(); rest != "" {
		t.Fatalf("flush = %q", rest)
	}
}

func TestSanitizeAgentExecResultStripsEventLines(t *testing.T) {
	raw := "__AGENT_EVENT__{\"kind\":\"text_delta\"}\nlog\n" + AgentResultPrefix + `{"provider":"codex","finalText":"done"}` + "\n"
	cleaned := SanitizeAgentExecResult(domain.ExecResult{Stdout: raw, Output: raw})
	if cleaned.Stdout != "log\n" || cleaned.Output != "log\n" {
		t.Fatalf("cleaned = %#v", cleaned)
	}
	parsed, err := ParseAgentExecResult("codex", domain.ExecResult{Stdout: raw})
	if err != nil || parsed.FinalText != "done" {
		t.Fatalf("parsed = %#v err=%v", parsed, err)
	}
}
