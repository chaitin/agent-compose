package llms

import (
	"context"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// CodexFacadeStore is the persistence surface needed to resolve a Codex model
// and issue its run-scoped runtime facade token.
type CodexFacadeStore interface {
	LLMResolverStore
	SaveLLMFacadeToken(context.Context, FacadeToken) error
}

// CodexFacadeConfigRequest bundles the config, credential store, target
// sandbox, and requested model/source/run identifiers
// EnsureCodexFacadeConfig needs to resolve and mint a Codex facade token.
type CodexFacadeConfigRequest struct {
	Config  *appconfig.Config
	Store   CodexFacadeStore
	Sandbox *domain.Sandbox
	Model   string
	Source  string
	RunID   string
}

// EnsureCodexFacadeConfig resolves the managed Codex provider and model,
// requires a sandbox-reachable facade, and returns its managed environment.
// A missing managed provider remains a no-op so Codex can use its own login.
func EnsureCodexFacadeConfig(ctx context.Context, req CodexFacadeConfigRequest) (map[string]string, error) {
	config, store, sandbox, model, source, runID := req.Config, req.Store, req.Sandbox, req.Model, req.Source, req.RunID
	providerEnv, err := SandboxProviderEnvItems(ctx, store, sandbox, ProviderFamilyOpenAI)
	if err != nil {
		return nil, err
	}
	target, err := ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
		Config: config, SessionID: sandbox.Summary.ID, PreferredProviderFamily: ProviderFamilyOpenAI, ProviderFamilyIsRequired: true, RequestedModel: model, ProviderID: "", EnvItems: providerEnv,
	})
	if err != nil {
		if OptionalFacadeConfigError(err) {
			return nil, nil
		}
		return nil, err
	}
	if NormalizeProviderType(target.Provider.ProviderType) != ProviderFamilyOpenAI {
		return nil, domain.ClassifyError(domain.ErrFailedPrecondition, "codex requires an OpenAI-compatible model", nil)
	}
	baseURL, err := RequireGuestRuntimeBaseURL(config, sandbox)
	if err != nil {
		return nil, err
	}

	tokenValue, token, err := NewFacadeToken(NewFacadeTokenRequest{
		SandboxID: sandbox.Summary.ID, Model: target.Model.Name, ProviderID: target.Provider.ID, WireAPI: APIProtocolResponses, Source: source, RunID: runID,
	})
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + sandbox.Summary.ID + "/llm/openai/v1"
	if err := WriteCodexRuntimeConfig(sandbox, CodexRuntimeConfig{Model: target.Model.Name, BaseURL: openAIBaseURL, WireAPI: APIProtocolResponses, Policy: CodexRuntimePolicyFromConfig(config)}); err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            openAIBaseURL,
		"LLM_API_KEY":                 tokenValue,
		// This declaration describes the upstream the gateway serves, not the
		// wire api the guest speaks. Codex always talks Responses to its own
		// facade route — that is what the facade token above and the runtime
		// config below pin — while the model behind the gateway may only serve
		// chat completions. Publishing Responses here made the persisted session
		// provider advertise an upstream protocol the logical model does not
		// have, so the proxy sent /v1/responses to a chat-only model and the
		// gateway rejected the call with 403. The runtime bridge converts the
		// guest's Responses request to whatever this value declares.
		"LLM_API_PROTOCOL": NormalizeWireAPI(target.WireAPI),
		"LLM_MODEL":        target.Model.Name,
		"CODEX_MODEL":      target.Model.Name,
		GuestModelEnvName:  target.Model.Name,
		"OPENAI_API_KEY":   tokenValue,
		"OPENAI_BASE_URL":  openAIBaseURL,
	}, nil
}
