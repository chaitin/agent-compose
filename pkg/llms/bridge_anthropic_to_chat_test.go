package llms

import (
	"encoding/json"
	"testing"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
)

// TestCrossFamilyBridgeServesMessagesToChat pins the pairing the protocol
// library cannot express. Its family lookup returns the Responses bridge for an
// Anthropic inbound whatever the upstream actually speaks, so the daemon has to
// supply the chat target itself; without that, claude could not be pointed at a
// chat-completions-only upstream at all.
func TestCrossFamilyBridgeServesMessagesToChat(t *testing.T) {
	bridge, err := crossFamilyBridge(protocolbridge.ProtocolAnthropicMessages, protocolbridge.ProtocolOpenAIChat, protocolbridge.FamilyOpenAI)
	if err != nil {
		t.Fatalf("crossFamilyBridge() error = %v", err)
	}
	if got := bridge.UpstreamProtocol(); got != protocolbridge.ProtocolOpenAIChat {
		t.Fatalf("UpstreamProtocol() = %q, want %q", got, protocolbridge.ProtocolOpenAIChat)
	}
}

// TestCrossFamilyBridgeStillRejectsUnservedPairs guards the other half: the
// guard must keep failing for pairings nothing can serve, including a family
// lookup that returns a bridge for the wrong protocol.
func TestCrossFamilyBridgeStillRejectsUnservedPairs(t *testing.T) {
	if _, err := crossFamilyBridge(protocolbridge.Protocol("bogus"), protocolbridge.ProtocolOpenAIChat, protocolbridge.FamilyOpenAI); err == nil {
		t.Fatal("crossFamilyBridge() served an unknown inbound protocol")
	}
}

func TestEncodeRuntimeUpstreamRequestForChatUpstream(t *testing.T) {
	target := anthropicToChatTarget(t)
	req := &protocolbridge.LLMRequest{
		Protocol: protocolbridge.ProtocolAnthropicMessages,
		Model:    "claude-sonnet-4",
		Prompt: []protocolbridge.Message{
			{Role: protocolbridge.RoleSystem, Parts: []protocolbridge.Part{{Type: protocolbridge.PartText, Text: &protocolbridge.TextPart{Text: "Be brief."}}}},
			{Role: protocolbridge.RoleUser, Parts: []protocolbridge.Part{{Type: protocolbridge.PartText, Text: &protocolbridge.TextPart{Text: "Hello"}}}},
		},
	}

	raw, err := EncodeRuntimeUpstreamRequest(protocolbridge.ProtocolAnthropicMessages, protocolbridge.ProtocolOpenAIChat, target, req)
	if err != nil {
		t.Fatalf("EncodeRuntimeUpstreamRequest() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if _, present := decoded["input"]; present {
		t.Fatal("a Responses-shaped payload was posted to a chat upstream")
	}
	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %v, want the system and user turns", decoded["messages"])
	}
	if role := messages[0].(map[string]any)["role"]; role != "system" {
		t.Fatalf("first message role = %v, want system", role)
	}
}

func TestEncodeRuntimeClientResponseForChatUpstream(t *testing.T) {
	target := anthropicToChatTarget(t)
	upstream := []byte(`{
		"id": "chatcmpl-1",
		"object": "chat.completion",
		"model": "claude-sonnet-4",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "hi there"}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}
	}`)

	raw, err := EncodeRuntimeClientResponse(protocolbridge.ProtocolAnthropicMessages, protocolbridge.ProtocolOpenAIChat, target, upstream)
	if err != nil {
		t.Fatalf("EncodeRuntimeClientResponse() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded["type"] != "message" {
		t.Fatalf("type = %v, want an anthropic message", decoded["type"])
	}
	content, ok := decoded["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("content = %v, want the assistant text", decoded["content"])
	}
	if text := content[0].(map[string]any)["text"]; text != "hi there" {
		t.Fatalf("content text = %v, want %q", text, "hi there")
	}
}

// TestRuntimeStreamBridgeRebasesChatUsageForAnthropic pins the one piece of
// protocol knowledge the daemon's chat bridge cannot delegate. The chat decoder
// reports prompt tokens the OpenAI way — a total with the cached portion broken
// out — while Anthropic expects input_tokens to exclude the cached tokens.
// Passing them through unchanged would report every cached token twice.
func TestRuntimeStreamBridgeRebasesChatUsageForAnthropic(t *testing.T) {
	_, encoder, err := RuntimeStreamBridge(
		protocolbridge.ProtocolAnthropicMessages,
		protocolbridge.ProtocolOpenAIChat,
		protocolbridge.FamilyOpenAI,
		"claude-sonnet-4",
	)
	if err != nil {
		t.Fatalf("RuntimeStreamBridge() error = %v", err)
	}

	promptTokens := 100
	cachedTokens := 40
	outputTokens := 10
	events, err := encoder.Encode(protocolbridge.StreamPart{
		Type: protocolbridge.StreamFinish,
		Usage: protocolbridge.Usage{
			InputTokens:       &promptTokens,
			CachedInputTokens: &cachedTokens,
			OutputTokens:      &outputTokens,
		},
	})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	usage := anthropicUsageFromEvents(t, events)
	if got := tokenValue(usage.InputTokens); got != 60 {
		t.Errorf("input_tokens = %d, want 60 (100 total minus 40 cached)", got)
	}
	if got := tokenValue(usage.CacheReadInputTokens); got != 40 {
		t.Errorf("cache_read_input_tokens = %d, want 40", got)
	}
	if got := tokenValue(usage.OutputTokens); got != 10 {
		t.Errorf("output_tokens = %d, want 10", got)
	}
}

func anthropicToChatTarget(t *testing.T) ResolvedTarget {
	t.Helper()
	target, err := NewResolvedTarget(
		agentLLMProvider("gateway-chat", ProviderFamilyOpenAI, APIProtocolChatCompletions, "https://gateway.test/v1"),
		Model{ID: "claude-sonnet-4", Name: "claude-sonnet-4", Enabled: true},
		ProviderModelConfig{},
	)
	if err != nil {
		t.Fatalf("NewResolvedTarget() error = %v", err)
	}
	return target
}

// anthropicUsagePayload mirrors the wire names of the Anthropic usage object.
// The library keeps its own struct unexported, and Go cannot match an
// InputTokens field to an input_tokens key without a tag.
type anthropicUsagePayload struct {
	InputTokens          *int `json:"input_tokens"`
	OutputTokens         *int `json:"output_tokens"`
	CacheReadInputTokens *int `json:"cache_read_input_tokens"`
}

func (u anthropicUsagePayload) usage() protocolbridge.Usage {
	return protocolbridge.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadInputTokens: u.CacheReadInputTokens}
}

// anthropicUsageFromEvents reads the usage out of an Anthropic stream, which
// reports it on message_delta and, for a stream that has started, on
// message_start's nested message.
func anthropicUsageFromEvents(t *testing.T, events []protocolbridge.RawStreamEvent) protocolbridge.Usage {
	t.Helper()
	for _, event := range events {
		var envelope struct {
			Usage   *anthropicUsagePayload `json:"usage"`
			Message *struct {
				Usage anthropicUsagePayload `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(event.Data, &envelope); err != nil {
			continue
		}
		if envelope.Usage != nil {
			return envelope.Usage.usage()
		}
		if envelope.Message != nil {
			return envelope.Message.Usage.usage()
		}
	}
	t.Fatalf("no usage found in %d stream events", len(events))
	return protocolbridge.Usage{}
}
