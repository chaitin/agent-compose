package api

import (
	"testing"

	"github.com/chaitin/agent-compose/pkg/agentinput"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestAgentInputPartsFromProtoMapsEveryPartKind(t *testing.T) {
	parts := AgentInputPartsFromProto([]*agentcomposev2.AgentInputPart{
		{Part: &agentcomposev2.AgentInputPart_Text{Text: "compare"}},
		{Part: &agentcomposev2.AgentInputPart_Media{Media: &agentcomposev2.AgentInputMedia{
			MediaType: "image/png",
			Filename:  "before.png",
			Source: &agentcomposev2.AgentInputMedia_Bytes{Bytes: &agentcomposev2.AgentInputMediaBytes{
				Data:   []byte("\x89PNG\r\n\x1a\n"),
				Sha256: "digest",
			}},
		}}},
		{Part: &agentcomposev2.AgentInputPart_Media{Media: &agentcomposev2.AgentInputMedia{
			MediaType: "application/pdf",
			Source: &agentcomposev2.AgentInputMedia_WorkspacePath{WorkspacePath: &agentcomposev2.AgentInputMediaWorkspacePath{
				Path:   "reports/q3.pdf",
				Sha256: "digest2",
			}},
		}}},
		{},
		nil,
	})
	if len(parts) != 5 {
		t.Fatalf("parts = %d, want 5", len(parts))
	}
	if parts[0].Text != "compare" || parts[0].Media != nil {
		t.Errorf("text part = %+v", parts[0])
	}
	inline := parts[1].Media
	if inline == nil || inline.Type != "image/png" || inline.Filename != "before.png" ||
		inline.Source != agentinput.SourceInline || inline.SHA256 != "digest" || len(inline.Data) == 0 {
		t.Errorf("inline part = %+v", inline)
	}
	workspace := parts[2].Media
	if workspace == nil || workspace.Source != agentinput.SourceWorkspacePath ||
		workspace.WorkspacePath != "reports/q3.pdf" || workspace.SHA256 != "digest2" {
		t.Errorf("workspace part = %+v", workspace)
	}
	if parts[3].Media != nil || parts[3].Text != "" {
		t.Errorf("empty part = %+v, want the zero value", parts[3])
	}
	if parts[4].Media != nil || parts[4].Text != "" {
		t.Errorf("nil part = %+v, want the zero value", parts[4])
	}
}

func TestAgentInputPartsFromProtoEmpty(t *testing.T) {
	if got := AgentInputPartsFromProto(nil); got != nil {
		t.Fatalf("parts = %v, want nil", got)
	}
}
