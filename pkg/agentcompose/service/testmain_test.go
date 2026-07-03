package agentcompose

import (
	"agent-compose/pkg/loaders"
	"agent-compose/pkg/sessions"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	for _, key := range []string{
		"LLM_API_ENDPOINT",
		"LLM_API_PROTOCOL",
		"LLM_API_KEY",
		"OPENAI_API_KEY",
		"LLM_MODEL",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_API_ENDPOINT",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_MODEL",
		"CLAUDE_MODEL",
	} {
		_ = os.Unsetenv(key)
	}
	os.Exit(m.Run())
}

func newTestLoaderBus(buffer int) *loaders.Bus {
	return loaders.NewBusWithBuffer(buffer)
}

func newTestSessionStreamBroker() *sessions.StreamBroker {
	return sessions.NewStreamBrokerForTest()
}
