package api

import (
	"testing"

	"github.com/chaitin/agent-compose/pkg/agentinput"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestAgentInputPartsFromProtoMapsEveryPartKind(t *testing.T) {
	parts := AgentInputPartsFromProto([]*agentcomposev2.AgentInputPart{
		{Part: &agentcomposev2.AgentInputPart_Text{Text: "compare"}},
		{Part: &agentcomposev2.AgentInputPart_File{File: &agentcomposev2.AgentInputFile{
			Filename:  "data.csv",
			MediaType: "text/csv",
			Source: &agentcomposev2.AgentInputFile_Bytes{Bytes: &agentcomposev2.AgentInputFileBytes{
				Data:   []byte("a,b\n"),
				Sha256: "digest",
			}},
		}}},
		{Part: &agentcomposev2.AgentInputPart_File{File: &agentcomposev2.AgentInputFile{
			Filename: "report.docx",
			Source: &agentcomposev2.AgentInputFile_WorkspacePath{WorkspacePath: &agentcomposev2.AgentInputFileWorkspacePath{
				Path:   "docs/report.docx",
				Sha256: "digest2",
			}},
		}}},
		{},
		nil,
	})
	if len(parts) != 5 {
		t.Fatalf("parts = %d, want 5", len(parts))
	}
	if parts[0].Text != "compare" || parts[0].File != nil {
		t.Errorf("text part = %+v", parts[0])
	}
	inline := parts[1].File
	if inline == nil || inline.Name != "data.csv" || inline.MediaType != "text/csv" ||
		inline.Source != agentinput.SourceInline || inline.SHA256 != "digest" || len(inline.Data) == 0 {
		t.Errorf("inline part = %+v", inline)
	}
	workspace := parts[2].File
	if workspace == nil || workspace.Source != agentinput.SourceWorkspacePath ||
		workspace.WorkspacePath != "docs/report.docx" || workspace.SHA256 != "digest2" {
		t.Errorf("workspace part = %+v", workspace)
	}
	if parts[3].File != nil || parts[3].Text != "" {
		t.Errorf("empty part = %+v, want the zero value", parts[3])
	}
	if parts[4].File != nil || parts[4].Text != "" {
		t.Errorf("nil part = %+v, want the zero value", parts[4])
	}
}

func TestAgentInputPartsFromProtoEmpty(t *testing.T) {
	if got := AgentInputPartsFromProto(nil); got != nil {
		t.Fatalf("parts = %v, want nil", got)
	}
}
