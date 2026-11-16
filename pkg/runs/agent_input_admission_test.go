package runs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/agentinput"
	"github.com/chaitin/agent-compose/pkg/capmatrix"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type stubAgentLookup struct {
	agent domain.ProjectAgentRecord
	err   error
}

func (s stubAgentLookup) GetProjectAgent(context.Context, string, string) (domain.ProjectAgentRecord, error) {
	return s.agent, s.err
}

func mediaProviders() map[string]capmatrix.ProviderCapabilities {
	return map[string]capmatrix.ProviderCapabilities{
		"codex": {
			Provider:           "codex",
			MediaTypes:         agentinput.ImageMediaTypes(),
			MediaOrdering:      capmatrix.MediaOrderingAggregate,
			MaxAttachmentBytes: agentinput.DefaultMaxPartBytes,
		},
	}
}

func pngPart(t *testing.T) agentinput.Part {
	t.Helper()
	data := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	return agentinput.MediaPart(agentinput.Media{Type: agentinput.MediaTypePNG, Source: agentinput.SourceInline, Data: data})
}

func TestAdmitAgentInputSkipsTextOnlyRuns(t *testing.T) {
	parts, warnings, err := admitAgentInput(context.Background(), stubAgentLookup{err: errors.New("must not be called")}, mediaProviders(), RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Prompt: "hello",
	})
	if err != nil {
		t.Fatalf("admitAgentInput: %v", err)
	}
	if parts != nil || warnings != nil {
		t.Fatalf("parts = %v, warnings = %v, want both nil", parts, warnings)
	}
}

func TestAdmitAgentInputPrependsPromptToInput(t *testing.T) {
	parts, warnings, err := admitAgentInput(context.Background(), stubAgentLookup{agent: domain.ProjectAgentRecord{Provider: "codex"}}, mediaProviders(), RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Prompt: "compare these", Input: []agentinput.Part{pngPart(t)},
	})
	if err != nil {
		t.Fatalf("admitAgentInput: %v", err)
	}
	if len(parts) != 2 || parts[0].Text != "compare these" || parts[1].Media == nil {
		t.Fatalf("parts = %+v, want prompt followed by media", parts)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "prepended") {
		t.Fatalf("warnings = %v, want one prepend warning", warnings)
	}
	if got := agentinput.Text(parts); got != "compare these" {
		t.Errorf("text projection = %q, want the prompt", got)
	}
}

func TestAdmitAgentInputAcceptsInputWithoutPrompt(t *testing.T) {
	parts, _, err := admitAgentInput(context.Background(), stubAgentLookup{agent: domain.ProjectAgentRecord{Provider: "codex"}}, mediaProviders(), RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Input: []agentinput.Part{agentinput.TextPart("look"), pngPart(t)},
	})
	if err != nil {
		t.Fatalf("admitAgentInput: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %+v, want text and media", parts)
	}
}

func TestAdmitAgentInputRejectsProviderWithoutMedia(t *testing.T) {
	providers := map[string]capmatrix.ProviderCapabilities{
		"codex": {Provider: "codex", MediaTypes: nil},
	}
	_, _, err := admitAgentInput(context.Background(), stubAgentLookup{agent: domain.ProjectAgentRecord{Provider: "codex"}}, providers, RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Prompt: "look", Input: []agentinput.Part{pngPart(t)},
	})
	if !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestAdmitAgentInputRejectsUnknownProvider(t *testing.T) {
	_, _, err := admitAgentInput(context.Background(), stubAgentLookup{agent: domain.ProjectAgentRecord{Provider: "unknown"}}, mediaProviders(), RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Prompt: "look", Input: []agentinput.Part{pngPart(t)},
	})
	if !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestAdmitAgentInputRejectsInvalidMedia(t *testing.T) {
	_, _, err := admitAgentInput(context.Background(), stubAgentLookup{agent: domain.ProjectAgentRecord{Provider: "codex"}}, mediaProviders(), RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Prompt: "look",
		Input: []agentinput.Part{agentinput.MediaPart(agentinput.Media{Type: agentinput.MediaTypePDF, Source: agentinput.SourceInline, Data: []byte("%PDF-1.7")})},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestAdmitAgentInputRejectsCommandWithInput(t *testing.T) {
	_, _, err := admitAgentInput(context.Background(), stubAgentLookup{}, mediaProviders(), RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Command: "echo hi", Input: []agentinput.Part{agentinput.TextPart("x")},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestAdmitAgentInputAppliesProviderLimit(t *testing.T) {
	providers := map[string]capmatrix.ProviderCapabilities{
		"codex": {
			Provider:           "codex",
			MediaTypes:         agentinput.ImageMediaTypes(),
			MediaOrdering:      capmatrix.MediaOrderingAggregate,
			MaxAttachmentBytes: 4,
		},
	}
	_, _, err := admitAgentInput(context.Background(), stubAgentLookup{agent: domain.ProjectAgentRecord{Provider: "codex"}}, providers, RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Prompt: "look", Input: []agentinput.Part{pngPart(t)},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestAdmitAgentInputReportsLookupFailure(t *testing.T) {
	_, _, err := admitAgentInput(context.Background(), stubAgentLookup{err: errors.New("boom")}, mediaProviders(), RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Prompt: "look", Input: []agentinput.Part{pngPart(t)},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}
