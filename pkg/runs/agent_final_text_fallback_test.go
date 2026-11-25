package runs

import (
	"context"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

// A runner with no provider message reports its whole transcript as the final
// text (transcript_fallback): the streamed prose plus tool headers. The run
// log already holds that prose, so the turn must not record it again.
func TestAgentTranscriptFallbackIsNotRecordedAsProseAgain(t *testing.T) {
	run := projectionTestRun(t)
	p := newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
	for _, event := range []string{
		`{"kind":"text_delta","text":"Let me check the files.\n"}`,
		`{"kind":"tool_call","id":"t1","name":"Bash","command":"ls","status":"completed"}`,
		`{"kind":"tool_result","id":"t1","ok":true,"output":"a.go\n"}`,
		`{"kind":"text_delta","text":"Found a.go.\n"}`,
	} {
		if _, err := p.ProjectFrame(execution.AgentFrame{Type: "agent_event", Event: []byte(event)}); err != nil {
			t.Fatal(err)
		}
	}
	// What claude's runner builds when the stream ends before its result
	// message (runtime/javascript/src/runners/claude.ts, writer.line calls).
	transcript := "Let me check the files.\n\n[tool:Bash]\n{\"command\":\"ls\"}\n\nFound a.go.\n"
	if _, err := p.ProjectFrame(execution.AgentFrame{Type: "result", StopReason: "error", FinalText: transcript,
		FinalTextSource: domain.AgentFinalTextSourceTranscriptFallback}); err != nil {
		t.Fatal(err)
	}
	entries := readProjectionLog(t, run)
	text := runlog.RenderText(entries)
	if n := strings.Count(text, "Let me check the files."); n != 1 {
		t.Fatalf("streamed prose appears %d times in the log text:\n%s", n, text)
	}
}
