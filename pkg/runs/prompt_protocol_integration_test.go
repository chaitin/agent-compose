package runs

import (
	"path/filepath"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

func TestIntegrationPromptProtocolsShareLogProjection(t *testing.T) {
	for format, wire := range map[string]string{
		// A released guest without frame output: no events, only the result.
		"legacy": "__AGENT_RESULT__{\"provider\":\"codex\",\"finalText\":\"answer\",\"finalTextSource\":\"provider_message\",\"stopReason\":\"completed\"}\n",
		"frames": "{\"v\":1,\"seq\":0,\"type\":\"agent_event\",\"event\":{\"kind\":\"text_delta\",\"text\":\"answer\"}}\n" +
			"{\"v\":1,\"seq\":1,\"type\":\"agent_turn_completed\",\"finalText\":\"answer\",\"finalTextSource\":\"provider_message\"}\n" +
			"{\"v\":1,\"seq\":2,\"type\":\"result\",\"provider\":\"codex\",\"finalText\":\"answer\",\"finalTextSource\":\"provider_message\",\"stopReason\":\"completed\"}\n",
	} {
		t.Run(format, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), runlog.FileName)
			t.Cleanup(func() { _ = closeRunLog(path) })
			run := domain.ProjectRunRecord{RunID: "r", Prompt: "go", LogsPath: path, LogFormat: runlog.FormatJSONL}
			p := newPromptAttachProjector(run, &domain.Sandbox{Summary: domain.SandboxSummary{ID: "s"}}, path, nil)
			var terminal *TransitionRequest
			for i := range len(wire) {
				_, next, err := p.Project([]byte(wire[i : i+1]))
				if err != nil {
					t.Fatal(err)
				}
				if next != nil {
					terminal = next
				}
			}
			if terminal == nil || terminal.Output != "answer" || terminal.ExitCode != 0 {
				t.Fatalf("transition=%+v", terminal)
			}
			if err := closeRunLog(path); err != nil {
				t.Fatal(err)
			}
			entries, err := runlog.ReadEntries(path, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 3 || entries[0].Type != runlog.EntryUser || entries[1].Type != runlog.EntryText || entries[2].Type != runlog.EntryTurnEnd {
				t.Fatalf("entries=%+v", entries)
			}
			if entries[2].FinalText != "answer" || entries[2].Seq != 3 || runlog.RenderText(entries) != "answer" {
				t.Fatalf("log=%+v", entries)
			}
		})
	}
}
