package runs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/agentinput"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

func inputController() *Controller {
	return &Controller{config: &appconfig.Config{
		AgentInputFileLimitBytes: 1024,
		AgentInputTurnLimitBytes: 4096,
		AgentInputFileLimit:      5,
	}}
}

func csvPart(t *testing.T) agentinput.Part {
	t.Helper()
	return agentinput.FilePart(agentinput.File{Name: "data.csv", MediaType: "text/csv", Source: agentinput.SourceInline, Data: []byte("a,b\n1,2\n")})
}

func TestAdmitAgentInputSkipsTextOnlyRuns(t *testing.T) {
	parts, warnings, err := inputController().admitAgentInput(context.Background(), RunAgentRequest{ProjectID: "project-1", AgentName: "worker", Prompt: "hello"})
	if err != nil {
		t.Fatalf("admitAgentInput: %v", err)
	}
	if parts != nil || warnings != nil {
		t.Fatalf("parts = %v, warnings = %v, want both nil", parts, warnings)
	}
}

func TestAdmitAgentInputPrependsPromptToInput(t *testing.T) {
	parts, warnings, err := inputController().admitAgentInput(context.Background(), RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Prompt: "compare these", Input: []agentinput.Part{csvPart(t)},
	})
	if err != nil {
		t.Fatalf("admitAgentInput: %v", err)
	}
	if len(parts) != 2 || parts[0].Text != "compare these" || parts[1].File == nil {
		t.Fatalf("parts = %+v, want prompt followed by file", parts)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "prepended") {
		t.Fatalf("warnings = %v, want one prepend warning", warnings)
	}
	if got := agentinput.Text(parts); got != "compare these" {
		t.Errorf("text projection = %q, want the prompt", got)
	}
}

func TestAdmitAgentInputAcceptsAnyFileType(t *testing.T) {
	file := agentinput.FilePart(agentinput.File{
		Name: "bundle.zip", MediaType: "application/zip", Source: agentinput.SourceInline, Data: []byte("PK\x03\x04"),
	})
	parts, _, err := inputController().admitAgentInput(context.Background(), RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Input: []agentinput.Part{agentinput.TextPart("look"), file},
	})
	if err != nil {
		t.Fatalf("admitAgentInput: %v", err)
	}
	if len(parts) != 2 || parts[1].File == nil {
		t.Fatalf("parts = %+v, want text and file", parts)
	}
}

func TestAdmitAgentInputRejectsInvalidFiles(t *testing.T) {
	cases := map[string]agentinput.Part{
		"oversize": agentinput.FilePart(agentinput.File{
			Name: "big.bin", Source: agentinput.SourceInline, Data: make([]byte, 2048),
		}),
		"digest mismatch": agentinput.FilePart(agentinput.File{
			Name: "a.csv", Source: agentinput.SourceInline, Data: []byte("x"),
			SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		}),
		"traversal": agentinput.FilePart(agentinput.File{
			Name: "a.csv", Source: agentinput.SourceWorkspacePath, WorkspacePath: "../secret",
		}),
		"missing source": agentinput.FilePart(agentinput.File{Name: "a.csv"}),
	}
	for name, file := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := inputController().admitAgentInput(context.Background(), RunAgentRequest{
				ProjectID: "project-1", AgentName: "worker", Prompt: "look", Input: []agentinput.Part{file},
			})
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestAdmitAgentInputRejectsCommandWithInput(t *testing.T) {
	_, _, err := inputController().admitAgentInput(context.Background(), RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Command: "echo hi", Input: []agentinput.Part{agentinput.TextPart("x")},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}
