package app

import (
	"testing"

	"github.com/chaitin/agent-compose/pkg/runs"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestRunInputFrameMappingPreservesManifestAndBytes(t *testing.T) {
	start := runAttachInputFromProto(&agentcomposev2.AttachAgentRunRequest{Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{Request: &agentcomposev2.RunAgentRequest{ClientRequestId: "original", InputFiles: []*agentcomposev2.RunInputFile{{FileId: "file", DisplayName: "a", SizeBytes: 2, Sha256: []byte("hash")}}}}}})
	if start.Request.ClientRequestID != "original" || len(start.Request.InputFiles) != 1 || start.Request.InputFiles[0].ID != "file" {
		t.Fatalf("start=%+v", start)
	}
	chunk := runAttachInputFromProto(&agentcomposev2.AttachAgentRunRequest{Frame: &agentcomposev2.AttachAgentRunRequest_FileChunk{FileChunk: &agentcomposev2.RunInputFileChunk{FileId: "file", Offset: 1, Data: []byte("x")}}})
	if chunk.Kind != runs.RunAttachInputFileChunk || chunk.FileID != "file" || chunk.Offset != 1 || string(chunk.Data) != "x" {
		t.Fatalf("chunk=%+v", chunk)
	}
	complete := runAttachInputFromProto(&agentcomposev2.AttachAgentRunRequest{Frame: &agentcomposev2.AttachAgentRunRequest_InputComplete{InputComplete: &agentcomposev2.RunInputComplete{}}})
	if complete.Kind != runs.RunAttachInputComplete {
		t.Fatalf("complete=%+v", complete)
	}
}
