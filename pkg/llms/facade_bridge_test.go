package llms

import (
	"encoding/json"
	"strings"
	"testing"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
)

func TestRuntimeStreamBridgeCompletesChatToolCallForResponses(t *testing.T) {
	decoder, encoder, err := RuntimeStreamBridge(
		protocolbridge.ProtocolOpenAIResponses,
		protocolbridge.ProtocolOpenAIChat,
		ProviderFamilyOpenAI,
		"deepseek-chat",
	)
	if err != nil {
		t.Fatalf("RuntimeStreamBridge() error = %v", err)
	}

	chunks := []string{
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":0,"model":"deepseek-chat","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"exec_command","arguments":""}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":0,"model":"deepseek-chat","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":\"echo bridge"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":0,"model":"deepseek-chat","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"-response-canary\"}"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":0,"model":"deepseek-chat","choices":[{"index":0,"delta":{"content":""},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
	}

	var encoded []protocolbridge.RawStreamEvent
	for _, chunk := range chunks {
		parts, decodeErr := decoder.Decode(protocolbridge.RawStreamEvent{Data: []byte(chunk)})
		if decodeErr != nil {
			t.Fatalf("Decode() error = %v", decodeErr)
		}
		for _, part := range parts {
			events, encodeErr := encoder.Encode(part)
			if encodeErr != nil {
				t.Fatalf("Encode(%s) error = %v", part.Type, encodeErr)
			}
			encoded = append(encoded, events...)
		}
	}
	parts, err := decoder.Close()
	if err != nil {
		t.Fatalf("decoder.Close() error = %v", err)
	}
	for _, part := range parts {
		events, encodeErr := encoder.Encode(part)
		if encodeErr != nil {
			t.Fatalf("Encode(%s) after close error = %v", part.Type, encodeErr)
		}
		encoded = append(encoded, events...)
	}
	events, err := encoder.Close()
	if err != nil {
		t.Fatalf("encoder.Close() error = %v", err)
	}
	encoded = append(encoded, events...)

	var body strings.Builder
	for _, event := range encoded {
		body.Write(event.Data)
		body.WriteByte('\n')
	}
	got := body.String()
	for _, want := range []string{
		`"type":"response.output_item.added"`,
		`"type":"response.function_call_arguments.delta"`,
		`"type":"response.function_call_arguments.done"`,
		`"type":"response.output_item.done"`,
		`"type":"response.completed"`,
		`"arguments":"{\"cmd\":\"echo bridge-response-canary\"}"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("encoded stream missing %s:\n%s", want, got)
		}
	}
}

func TestRewriteRuntimeRequestForUpstreamPreservesAssistantTextAfterToolCall(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Call the demo tool."}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I'll call the demo tool."}]},
			{"type":"function_call","name":"search_demo","arguments":"{\"query\":\"demo\"}","call_id":"call_1"},
			{"type":"function_call_output","call_id":"call_1","output":"mcp-tool-ok"}
		]
	}`)

	rewritten, err := RewriteRuntimeRequestForUpstream(body, ResolvedTarget{}, protocolbridge.ProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("RewriteRuntimeRequestForUpstream() error = %v", err)
	}

	var payload struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("unmarshal rewritten request: %v", err)
	}
	if got := payload.Input[1].Content[0].Type; got != "output_text" {
		t.Fatalf("assistant preamble text type = %q, want output_text", got)
	}
	if got := payload.Input[0].Content[0].Type; got != "input_text" {
		t.Fatalf("user text type = %q, want input_text", got)
	}
	if got := payload.Input[2].Type; got != "function_call" {
		t.Fatalf("tool call item type = %q, want function_call", got)
	}
	if got := payload.Input[3].Type; got != "function_call_output" {
		t.Fatalf("tool output item type = %q, want function_call_output", got)
	}
}

func TestRewriteRuntimeRequestForUpstreamNormalizesResponsesTextTypesByRole(t *testing.T) {
	tests := []struct {
		name   string
		target ResolvedTarget
		want   []string
	}{
		{
			name: "standard responses",
			want: []string{"input_text", "input_text", "output_text"},
		},
		{
			name: "generic provider",
			target: ResolvedTarget{Provider: Provider{
				UseGenericResponsesTextParts: true,
			}},
			want: []string{"text", "text", "text"},
		},
	}

	body := []byte(`{
		"input":[
			{"role":"developer","content":[{"text":"Follow the instructions."}]},
			{"role":"user","content":[{"type":"output_text","text":"Question"}]},
			{"role":"assistant","content":[{"type":"input_text","text":"Answer"}]}
		]
	}`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rewritten, err := RewriteRuntimeRequestForUpstream(body, tt.target, protocolbridge.ProtocolOpenAIResponses)
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
			for i, want := range tt.want {
				if got := payload.Input[i].Content[0].Type; got != want {
					t.Errorf("input[%d] text type = %q, want %q", i, got, want)
				}
			}
		})
	}
}
