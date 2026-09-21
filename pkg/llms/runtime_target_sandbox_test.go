package llms

import (
	"context"
	"testing"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// A sandbox that publishes LLM_API_PROTOCOL=chat_completions is served by a
// chat-completions upstream. Resolving the proxied call without the sandbox
// used to lose that: the target fell back to a stored connection, the wire api
// became `responses`, and the call was posted to a /v1/responses endpoint the
// gateway does not route for a chat model.
func TestSandboxRuntimeLLMTargetKeepsInjectedWireAPI(t *testing.T) {
	isolateLLMEnv(t)
	ctx := context.Background()
	for _, test := range []struct {
		name         string
		wireAPI      string
		wantProtocol protocolbridge.Protocol
		wantEndpoint string
	}{
		{name: "chat completions", wireAPI: APIProtocolChatCompletions, wantProtocol: protocolbridge.ProtocolOpenAIChat, wantEndpoint: "https://gateway.test/v1/chat/completions"},
		{name: "responses", wireAPI: APIProtocolResponses, wantProtocol: protocolbridge.ProtocolOpenAIResponses, wantEndpoint: "https://gateway.test/v1/responses"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := newBareModelFacadeStore()
			sandbox := bareModelSandbox(root, "session-env-"+test.wireAPI)
			SetSandboxProviderEnvItems(sandbox, []domain.SandboxEnvVar{
				{Name: "LLM_API_ENDPOINT", Value: "https://gateway.test/v1"},
				{Name: "LLM_API_KEY", Value: "gateway-key"},
				{Name: "LLM_MODEL", Value: "matrix/deepseek-flash"},
				{Name: "LLM_API_PROTOCOL", Value: test.wireAPI},
			})

			target, err := SandboxRuntimeLLMTarget(ctx, bareModelConfig(root), store, sandbox, ProviderFamilyOpenAI, "matrix/deepseek-flash", "")
			if err != nil {
				t.Fatalf("SandboxRuntimeLLMTarget returned error: %v", err)
			}
			if got := NormalizeWireAPI(target.WireAPI); got != test.wireAPI {
				t.Fatalf("target wire api = %q, want %q", got, test.wireAPI)
			}
			if got := target.Model.Name; got != "matrix/deepseek-flash" {
				t.Fatalf("target model = %q, want the published literal", got)
			}
			protocol, endpoint, err := UpstreamProtocolAndEndpoint(target)
			if err != nil {
				t.Fatalf("UpstreamProtocolAndEndpoint returned error: %v", err)
			}
			if protocol != test.wantProtocol || endpoint != test.wantEndpoint {
				t.Fatalf("upstream = %v %s, want %v %s", protocol, endpoint, test.wantProtocol, test.wantEndpoint)
			}
		})
	}
}

// A sandbox that publishes no provider environment keeps the daemon
// configuration precedence: the proxy must not invent a session connection.
func TestSandboxRuntimeLLMTargetFallsBackToDaemonConfig(t *testing.T) {
	isolateLLMEnv(t)
	ctx := context.Background()
	root := t.TempDir()
	store := newBareModelFacadeStore()
	sandbox := bareModelSandbox(root, "session-env-empty")

	_, fromSandbox := SandboxRuntimeLLMTarget(ctx, bareModelConfig(root), store, sandbox, ProviderFamilyOpenAI, "matrix/deepseek-flash", "")
	_, fromDaemon := ResolveRuntimeLLMTarget(ctx, bareModelConfig(root), store, "matrix/deepseek-flash", "")
	if (fromSandbox == nil) != (fromDaemon == nil) {
		t.Fatalf("sandbox path error = %v, daemon path error = %v; want the same outcome", fromSandbox, fromDaemon)
	}
	if fromSandbox != nil && fromSandbox.Error() != fromDaemon.Error() {
		t.Fatalf("sandbox path error = %v, daemon path error = %v; want the daemon message", fromSandbox, fromDaemon)
	}
}
