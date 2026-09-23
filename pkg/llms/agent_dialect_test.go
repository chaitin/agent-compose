package llms

import (
	"errors"
	"testing"
)

func TestDialectForNormalizesAgentAliases(t *testing.T) {
	cases := map[string]string{
		"codex":            "codex",
		"claude":           "claude",
		"claude-code":      "claude",
		"claude_code":      "claude",
		"opencode":         "opencode",
		"open-code":        "opencode",
		"pi":               "pi",
		"pi-agent":         "pi",
		"dsh":              "dsh",
		"deepseek-harness": "dsh",
		"  OpenCode  ":     "opencode",
	}
	for input, wantKind := range cases {
		dialect, err := DialectFor(input)
		if err != nil {
			t.Fatalf("DialectFor(%q) error = %v", input, err)
		}
		if dialect.Kind != wantKind {
			t.Errorf("DialectFor(%q).Kind = %q, want %q", input, dialect.Kind, wantKind)
		}
	}
}

func TestDialectForRejectsAgentsWithoutManagedLLM(t *testing.T) {
	for _, kind := range []string{"gemini", "gemini-cli", "unknown-agent", ""} {
		if _, err := DialectFor(kind); !errors.Is(err, ErrUnsupportedAgentDialect) {
			t.Errorf("DialectFor(%q) error = %v, want ErrUnsupportedAgentDialect", kind, err)
		}
	}
}

// TestDialectConversionMatrix pins the complete (agent, upstream) decision
// table: passthrough whenever the CLI can speak the upstream protocol, and the
// agent's canonical inbound protocol otherwise.
func TestDialectConversionMatrix(t *testing.T) {
	type want struct {
		inbound      Protocol
		needsConvert bool
	}
	matrix := map[string]map[Protocol]want{
		"codex": {
			ProtocolResponses:       {ProtocolResponses, false},
			ProtocolChatCompletions: {ProtocolResponses, true},
			ProtocolMessages:        {ProtocolResponses, true},
		},
		"claude": {
			ProtocolMessages:        {ProtocolMessages, false},
			ProtocolResponses:       {ProtocolMessages, true},
			ProtocolChatCompletions: {ProtocolMessages, true},
		},
		"opencode": {
			ProtocolChatCompletions: {ProtocolChatCompletions, false},
			ProtocolMessages:        {ProtocolMessages, false},
			ProtocolResponses:       {ProtocolChatCompletions, true},
		},
		"pi": {
			ProtocolChatCompletions: {ProtocolChatCompletions, false},
			ProtocolResponses:       {ProtocolResponses, false},
			ProtocolMessages:        {ProtocolMessages, false},
		},
		"dsh": {
			ProtocolChatCompletions: {ProtocolChatCompletions, false},
			ProtocolResponses:       {ProtocolResponses, false},
			ProtocolMessages:        {ProtocolMessages, false},
		},
	}
	// Cells no bridge can serve. It is empty: every combination is servable,
	// including claude against a chat-only upstream, because the protocol
	// library now selects a cross-family bridge by the exact protocol pair. The
	// map stays so that a future gap has to be declared here rather than left
	// implicit, which is how the last one went unnoticed.
	unservable := map[string]bool{}

	for agentKind, byUpstream := range matrix {
		dialect, err := DialectFor(agentKind)
		if err != nil {
			t.Fatalf("DialectFor(%q) error = %v", agentKind, err)
		}
		for upstream, expected := range byUpstream {
			gotInbound := dialect.InboundProtocol(upstream)
			if gotInbound != expected.inbound {
				t.Errorf("%s upstream %s: inbound = %s, want %s", agentKind, upstream, gotInbound, expected.inbound)
			}
			if got := dialect.NeedsConversion(upstream); got != expected.needsConvert {
				t.Errorf("%s upstream %s: NeedsConversion = %v, want %v", agentKind, upstream, got, expected.needsConvert)
			}
			// Deciding to convert is not the same as being able to. Without this
			// the table reads as though all fifteen combinations work, which is
			// how the missing bridge stayed invisible.
			cell := agentKind + "/" + string(upstream)
			servable := !expected.needsConvert || CanConvert(expected.inbound, upstream)
			if wantServable := !unservable[cell]; servable != wantServable {
				t.Errorf("%s: servable = %v, want %v; a cell that needs conversion is only servable when CanConvert agrees", cell, servable, wantServable)
			}
		}
	}
}

func TestDialectGuestModelComposesDaemonOwnedPrefix(t *testing.T) {
	cases := []struct {
		agentKind string
		model     string
		want      string
	}{
		// A literal model id that already contains slashes must survive intact.
		{"pi", "meta-llama/Llama-3.1-8B", "agent-compose/meta-llama/Llama-3.1-8B"},
		{"pi", "gpt-5.5", "agent-compose/gpt-5.5"},
		{"opencode", "openai/gpt-4o", "agent-compose/openai/gpt-4o"},
		// Agents that address the model directly never gain a prefix.
		{"codex", "gpt-5.5", "gpt-5.5"},
		{"claude", "claude-sonnet-4", "claude-sonnet-4"},
		{"dsh", "deepseek-chat", "deepseek-chat"},
		{"pi", "   ", ""},
	}
	for _, tc := range cases {
		dialect, err := DialectFor(tc.agentKind)
		if err != nil {
			t.Fatalf("DialectFor(%q) error = %v", tc.agentKind, err)
		}
		if got := dialect.GuestModel(tc.model); got != tc.want {
			t.Errorf("%s.GuestModel(%q) = %q, want %q", tc.agentKind, tc.model, got, tc.want)
		}
	}
}

func TestProtocolHelpers(t *testing.T) {
	if got := NormalizeProtocol(""); got != ProtocolResponses {
		t.Errorf("NormalizeProtocol(\"\") = %q, want responses", got)
	}
	if got := NormalizeProtocol("chat"); got != ProtocolChatCompletions {
		t.Errorf("NormalizeProtocol(chat) = %q, want chat_completions", got)
	}
	if got := NormalizeProtocol("anthropic_messages"); got != ProtocolMessages {
		t.Errorf("NormalizeProtocol(anthropic_messages) = %q, want anthropic_messages", got)
	}
	if ProtocolResponses.Valid() != true || Protocol("bogus").Valid() != false {
		t.Error("Protocol.Valid did not recognize the protocol set")
	}
	if ProtocolResponses.Family() != ProviderFamilyOpenAI || ProtocolMessages.Family() != ProviderFamilyAnthropic {
		t.Error("Protocol.Family returned the wrong family")
	}
}

// TestCanConvertPinsBridgeCoverage records exactly which (inbound, upstream)
// pairs the daemon can serve. Every same-family pair re-encodes through the
// shared adapters. Cross-family pairs depend on the bridge registry, which now
// registers every pair the daemon's three protocols can form, so PrepareAgentLLM
// never has to reject a combination at configuration time and fail on the first
// request only because a bridge was missing.
func TestCanConvertPinsBridgeCoverage(t *testing.T) {
	cases := []struct {
		inbound  Protocol
		upstream Protocol
		want     bool
	}{
		{ProtocolResponses, ProtocolResponses, true},
		{ProtocolResponses, ProtocolChatCompletions, true},
		{ProtocolResponses, ProtocolMessages, true},
		{ProtocolChatCompletions, ProtocolChatCompletions, true},
		{ProtocolChatCompletions, ProtocolResponses, true},
		{ProtocolChatCompletions, ProtocolMessages, true},
		{ProtocolMessages, ProtocolMessages, true},
		{ProtocolMessages, ProtocolResponses, true},
		{ProtocolMessages, ProtocolChatCompletions, true},
		{ProtocolMessages, Protocol("bogus"), false},
	}
	for _, tc := range cases {
		if got := CanConvert(tc.inbound, tc.upstream); got != tc.want {
			t.Errorf("CanConvert(%s, %s) = %v, want %v", tc.inbound, tc.upstream, got, tc.want)
		}
	}
}
