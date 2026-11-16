package api

import (
	"github.com/chaitin/agent-compose/pkg/agentinput"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// AgentInputPartsFromProto maps the wire turn into the domain value object.
//
// A missing or malformed part is preserved as its zero value instead of being
// dropped: admission must reject it with a message naming the part index, and a
// transport that silently discarded it would let a caller believe the content
// reached the agent.
func AgentInputPartsFromProto(values []*agentcomposev2.AgentInputPart) []agentinput.Part {
	if len(values) == 0 {
		return nil
	}
	parts := make([]agentinput.Part, 0, len(values))
	for _, value := range values {
		if value == nil {
			parts = append(parts, agentinput.Part{})
			continue
		}
		if text, ok := value.GetPart().(*agentcomposev2.AgentInputPart_Text); ok {
			parts = append(parts, agentinput.TextPart(text.Text))
			continue
		}
		if file := value.GetFile(); file != nil {
			parts = append(parts, agentinput.FilePart(agentInputFileFromProto(file)))
			continue
		}
		parts = append(parts, agentinput.Part{})
	}
	return parts
}

func agentInputFileFromProto(file *agentcomposev2.AgentInputFile) agentinput.File {
	out := agentinput.File{
		Name:      file.GetFilename(),
		MediaType: file.GetMediaType(),
	}
	switch source := file.GetSource().(type) {
	case *agentcomposev2.AgentInputFile_Bytes:
		out.Source = agentinput.SourceInline
		out.Data = append([]byte(nil), source.Bytes.GetData()...)
		out.SHA256 = source.Bytes.GetSha256()
	case *agentcomposev2.AgentInputFile_WorkspacePath:
		out.Source = agentinput.SourceWorkspacePath
		out.WorkspacePath = source.WorkspacePath.GetPath()
		out.SHA256 = source.WorkspacePath.GetSha256()
	}
	return out
}
