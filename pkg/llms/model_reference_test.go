package llms

import "testing"

func TestGuestModelReferencePreservesLiteralNamespace(t *testing.T) {
	for _, tc := range []struct{ provider, model, want string }{
		{"anthropic", "anthropic/claude-test", "anthropic/anthropic/claude-test"},
		{"agent-compose", "agent-compose/model", "agent-compose/agent-compose/model"},
		{"agent-compose", "org/model", "agent-compose/org/model"},
		{"", "org/model", "org/model"},
		{"agent-compose", "", ""},
	} {
		if got := GuestModelReference(tc.provider, tc.model); got != tc.want {
			t.Errorf("GuestModelReference(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

func TestRuntimeModelArgumentSupportsLegacyGuests(t *testing.T) {
	for _, tc := range []struct{ agent, model, want string }{
		{"dsh", "org/model", "agent-compose/org/model"},
		{"dsh", "agent-compose/model", "agent-compose/agent-compose/model"},
		{"dsh", "model", "agent-compose/model"},
		{"dsh", "", ""},
		{"pi", "agent-compose/org/model", "agent-compose/org/model"},
		{"opencode", "anthropic/anthropic/model", "anthropic/anthropic/model"},
		{"codex", "org/model", "org/model"},
		{"claude", "org/model", "org/model"},
	} {
		if got := RuntimeModelArgument(tc.agent, tc.model); got != tc.want {
			t.Errorf("RuntimeModelArgument(%q, %q) = %q, want %q", tc.agent, tc.model, got, tc.want)
		}
	}
}
