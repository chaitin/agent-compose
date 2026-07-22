package llms

import (
	"encoding/json"
	"testing"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
)

func TestRewriteRuntimeRequestForUpstreamNormalizesResumedAssistantText(t *testing.T) {
	body := []byte(`{
		"model":"old",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"remember kiwi"}]},
			{"role":"assistant","content":[{"type":"output_text","text":"I will remember kiwi"}]},
			{"role":"user","content":[{"type":"input_text","text":"what did I ask you to remember?"}]}
		]
	}`)

	rewritten, err := RewriteRuntimeRequestForUpstream(body, ResolvedTarget{}, protocolbridge.ProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("RewriteRuntimeRequestForUpstream() error = %v", err)
	}

	var payload struct {
		Input []struct {
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("unmarshal rewritten request: %v", err)
	}
	if got := payload.Input[1].Content[0].Type; got != "input_text" {
		t.Fatalf("resumed assistant text type = %q, want input_text", got)
	}
	if got := payload.Input[0].Content[0].Type; got != "input_text" {
		t.Fatalf("existing user text type = %q, want input_text", got)
	}
}
