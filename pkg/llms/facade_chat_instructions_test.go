package llms

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
)

func TestRuntimeChatInstructionsPrecedeConversation(t *testing.T) {
	text := func(role protocolbridge.Role, value string) protocolbridge.Message {
		return protocolbridge.Message{Role: role, Parts: []protocolbridge.Part{{
			Type: protocolbridge.PartText, Text: &protocolbridge.TextPart{Text: value},
		}}}
	}
	req := &protocolbridge.LLMRequest{Model: "test", Prompt: []protocolbridge.Message{
		text(protocolbridge.RoleSystem, "original instructions"),
		text(protocolbridge.RoleUser, "question"),
		text(protocolbridge.RoleDeveloper, "updated instructions"),
		text(protocolbridge.RoleAssistant, "answer"),
	}}
	before, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := EncodeRuntimeUpstreamRequest(protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIChat, ResolvedTarget{}, req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	roles := []string{}
	for _, message := range decoded.Messages {
		roles = append(roles, message.Role)
	}
	if !reflect.DeepEqual(roles, []string{"system", "user", "assistant"}) {
		t.Fatalf("roles = %v; strict chat templates require one initial system message", roles)
	}
	var instructions string
	if err := json.Unmarshal(decoded.Messages[0].Content, &instructions); err != nil {
		var blocks []struct{ Text string }
		if err := json.Unmarshal(decoded.Messages[0].Content, &blocks); err != nil {
			t.Fatal(err)
		}
		var parts []string
		for _, block := range blocks {
			parts = append(parts, block.Text)
		}
		instructions = strings.Join(parts, "")
	}
	if instructions != "original instructions\n\nupdated instructions" {
		t.Fatalf("instructions = %q", instructions)
	}
	after, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rewriting mutated the caller's conversation")
	}
}

func TestRuntimeRawChatKeepsToolConversationWhenFoldingInstructions(t *testing.T) {
	body := []byte(`{"model":"test","messages":[
		{"role":"user","content":"question","name":"caller"},
		{"role":"developer","content":"first"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
		{"role":"system","content":"second"},
		{"role":"tool","tool_call_id":"call-1","content":"result"}
	]}`)
	rewritten, err := RewriteRuntimeRequestForUpstream(body, ResolvedTarget{}, protocolbridge.ProtocolOpenAIChat)
	if err != nil {
		t.Fatal(err)
	}
	var original, result struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rewritten, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 4 || string(result.Messages[0]["role"]) != `"system"` || string(result.Messages[0]["content"]) != `"first\n\nsecond"` {
		t.Fatalf("unexpected instruction prefix: %s", rewritten)
	}
	for i, originalIndex := range []int{0, 2, 4} {
		if !reflect.DeepEqual(result.Messages[i+1], original.Messages[originalIndex]) {
			t.Fatalf("conversation message %d was changed: %s", i, rewritten)
		}
	}
}

func TestRuntimeChatCanonicalConversationIsUnchanged(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"system","content":"instructions"},{"role":"user","content":"question"}]}`,
		`{"messages":[{"role":"user","content":"question"}]}`,
	} {
		rewritten, err := RewriteRuntimeRequestForUpstream([]byte(body), ResolvedTarget{}, protocolbridge.ProtocolOpenAIChat)
		if err != nil || string(rewritten) != body {
			t.Fatalf("canonical request changed: %s, err=%v", rewritten, err)
		}
	}
}

func TestRuntimeRawChatPreservesInstructionContentBlocks(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"question"},
		{"role":"system","content":[{"type":"text","text":"first","cache_control":{"type":"ephemeral"}}]},
		{"role":"developer","content":"second"}]}`)
	rewritten, err := RewriteRuntimeRequestForUpstream(body, ResolvedTarget{}, protocolbridge.ProtocolOpenAIChat)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatal(err)
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(payload["messages"], &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || string(messages[0]["role"]) != `"system"` {
		t.Fatalf("unexpected prefix: %s", rewritten)
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(messages[0]["content"], &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 || string(parts[0]["text"]) != `"first"` || string(parts[2]["text"]) != `"second"` || string(parts[0]["cache_control"]) != `{"type":"ephemeral"}` {
		t.Fatalf("instruction content changed: %s", rewritten)
	}
}
