package llms

import (
	"context"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// OpenCodeFacadeStore is the persistence surface needed to resolve an
// OpenCode provider target and issue a runtime facade token.
type OpenCodeFacadeStore interface {
	LLMResolverStore
	SaveLLMFacadeToken(context.Context, FacadeToken) error
}

// openCodeFacadeCall groups the environment (Config/Store/Sandbox) and
// per-call attribution (Source/RunID) shared by every OpenCode facade
// resolution helper below, plus whichever of ProviderID/Model/Target the
// specific step needs.
type openCodeFacadeCall struct {
	Config     *appconfig.Config
	Store      OpenCodeFacadeStore
	Sandbox    *domain.Sandbox
	ProviderID string
	Model      string
	Target     ResolvedTarget
	Source     string
	RunID      string
}

// OpenCodeFacadeConfigRequest bundles the config, credential store, target
// sandbox, and requested model/source/run identifiers
// EnsureOpenCodeFacadeConfig needs to resolve and mint an OpenCode facade
// token.
type OpenCodeFacadeConfigRequest struct {
	Config  *appconfig.Config
	Store   OpenCodeFacadeStore
	Sandbox *domain.Sandbox
	Model   string
	Source  string
	RunID   string
}

// EnsureOpenCodeFacadeConfig resolves an OpenCode provider/model pair, writes
// its guest runtime config, and returns the managed facade environment.
func EnsureOpenCodeFacadeConfig(ctx context.Context, req OpenCodeFacadeConfigRequest) (map[string]string, error) {
	config, store, sandbox, model, source, runID := req.Config, req.Store, req.Sandbox, req.Model, req.Source, req.RunID
	providerID, modelName, err := SplitOpenCodeModel(model)
	if err != nil {
		return nil, err
	}
	baseURL := GuestRuntimeBaseURL(config, sandbox)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	if providerID == "opencode" {
		return nil, nil
	}
	call := openCodeFacadeCall{Config: config, Store: store, Sandbox: sandbox, ProviderID: providerID, Model: modelName, Source: source, RunID: runID}
	if HasEnabledLLMProviderID(ctx, store, providerID) {
		return ensureOpenCodeConfiguredFacadeConfig(ctx, call)
	}
	switch providerID {
	case ProviderFamilyAnthropic:
		return ensureOpenCodeAnthropicFacadeConfig(ctx, call)
	case ProviderFamilyOpenAI:
		return ensureOpenCodeOpenAIFacadeConfig(ctx, call)
	default:
		return ensureOpenCodeCustomFacadeConfig(ctx, call)
	}
}

func ensureOpenCodeConfiguredFacadeConfig(ctx context.Context, call openCodeFacadeCall) (map[string]string, error) {
	config, store, sandbox := call.Config, call.Store, call.Sandbox
	providerEnv, err := SandboxProviderEnvItems(ctx, store, sandbox, "")
	if err != nil {
		return nil, err
	}
	target, err := ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
		Config: config, SessionID: sandbox.Summary.ID, PreferredProviderFamily: "", RequestedModel: call.Model, ProviderID: call.ProviderID, EnvItems: providerEnv,
	})
	if err != nil {
		return nil, err
	}
	switch NormalizeProviderType(target.Provider.ProviderType) {
	case ProviderFamilyOpenAI:
		familyEnv, err := SandboxProviderEnvItems(ctx, store, sandbox, ProviderFamilyOpenAI)
		if err != nil {
			return nil, err
		}
		if HasOpenAIEnvProviderInput(familyEnv) {
			return ensureOpenCodeOpenAIFacadeConfig(ctx, call)
		}
	case ProviderFamilyAnthropic:
		familyEnv, err := SandboxProviderEnvItems(ctx, store, sandbox, ProviderFamilyAnthropic)
		if err != nil {
			return nil, err
		}
		if HasAnthropicEnvProviderInput(familyEnv) {
			return ensureOpenCodeAnthropicFacadeConfig(ctx, call)
		}
	}
	call.Target = target
	return ensureOpenCodeResolvedFacadeConfig(ctx, call)
}

func ensureOpenCodeResolvedFacadeConfig(ctx context.Context, call openCodeFacadeCall) (map[string]string, error) {
	config, store, sandbox, target, source, runID := call.Config, call.Store, call.Sandbox, call.Target, call.Source, call.RunID
	baseURL := GuestRuntimeBaseURL(config, sandbox)
	providerFamily := NormalizeProviderType(target.Provider.ProviderType)
	if providerFamily == ProviderFamilyAnthropic {
		tokenValue, token, err := NewFacadeToken(NewFacadeTokenRequest{
			SandboxID: sandbox.Summary.ID, Model: target.Model.Name, ProviderID: target.Provider.ID, WireAPI: APIProtocolMessages, Source: source, RunID: runID,
		})
		if err != nil {
			return nil, err
		}
		if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
			return nil, err
		}
		anthropicBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + sandbox.Summary.ID + "/llm/anthropic"
		if err := WriteOpenCodeAnthropicRuntimeConfig(sandbox, target.Model.Name, anthropicBaseURL+"/v1"); err != nil {
			return nil, err
		}
		return map[string]string{
			"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue, "LLM_API_ENDPOINT": anthropicBaseURL,
			"LLM_API_KEY": tokenValue, "LLM_API_PROTOCOL": APIProtocolMessages,
			"LLM_MODEL": "anthropic/" + target.Model.Name, "OPENCODE_MODEL": "anthropic/" + target.Model.Name,
			"ANTHROPIC_API_KEY": tokenValue, "ANTHROPIC_AUTH_TOKEN": tokenValue,
			"ANTHROPIC_BASE_URL": anthropicBaseURL, "OPENCODE_CONFIG": GuestOpenCodeConfigPath(config),
		}, nil
	}
	if providerFamily != ProviderFamilyOpenAI {
		return nil, domain.ClassifyError(domain.ErrFailedPrecondition, "opencode requires an OpenAI-compatible or Anthropic model", nil)
	}
	// The guard still rejects a provider the facade could not proxy to, so an
	// unroutable target fails at run start rather than on the first request.
	// It does not decide the token's wire api: see openCodeGuestWireAPI.
	if upstream := NormalizeWireAPI(target.WireAPI); upstream != APIProtocolResponses && upstream != APIProtocolChatCompletions {
		return nil, domain.ClassifyError(domain.ErrFailedPrecondition, "opencode model uses an unsupported wire protocol", nil)
	}
	tokenValue, token, err := NewFacadeToken(NewFacadeTokenRequest{
		SandboxID: sandbox.Summary.ID, Model: target.Model.Name, ProviderID: target.Provider.ID, WireAPI: openCodeGuestWireAPI, Source: source, RunID: runID,
	})
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + sandbox.Summary.ID + "/llm/openai/v1"
	if err := WriteOpenCodeRuntimeConfig(sandbox, "agent-compose", target.Model.Name, openAIBaseURL); err != nil {
		return nil, err
	}
	env := openCodeOpenAIEnv(tokenValue, openAIBaseURL, openCodeGuestWireAPI, config)
	env["LLM_MODEL"] = "agent-compose/" + target.Model.Name
	env["OPENCODE_MODEL"] = "agent-compose/" + target.Model.Name
	return env, nil
}

func ensureOpenCodeAnthropicFacadeConfig(ctx context.Context, call openCodeFacadeCall) (map[string]string, error) {
	config, store, sandbox, model, source, runID := call.Config, call.Store, call.Sandbox, call.Model, call.Source, call.RunID
	providerEnv, err := SandboxProviderEnvItems(ctx, store, sandbox, ProviderFamilyAnthropic)
	if err != nil {
		return nil, err
	}
	target, err := ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
		Config: config, SessionID: sandbox.Summary.ID, PreferredProviderFamily: ProviderFamilyAnthropic, RequestedModel: model, ProviderID: "", EnvItems: providerEnv,
	})
	if err != nil {
		return nil, err
	}
	baseURL := GuestRuntimeBaseURL(config, sandbox)
	tokenValue, token, err := NewFacadeToken(NewFacadeTokenRequest{
		SandboxID: sandbox.Summary.ID, Model: target.Model.Name, ProviderID: target.Provider.ID, WireAPI: APIProtocolMessages, Source: source, RunID: runID,
	})
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	anthropicBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + sandbox.Summary.ID + "/llm/anthropic"
	if err := WriteOpenCodeAnthropicRuntimeConfig(sandbox, target.Model.Name, anthropicBaseURL+"/v1"); err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            anthropicBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            APIProtocolMessages,
		"LLM_MODEL":                   "anthropic/" + target.Model.Name,
		"ANTHROPIC_API_KEY":           tokenValue,
		"ANTHROPIC_AUTH_TOKEN":        tokenValue,
		"ANTHROPIC_BASE_URL":          anthropicBaseURL,
		"OPENCODE_CONFIG":             GuestOpenCodeConfigPath(config),
		"OPENCODE_MODEL":              "anthropic/" + target.Model.Name,
	}, nil
}

func ensureOpenCodeOpenAIFacadeConfig(ctx context.Context, call openCodeFacadeCall) (map[string]string, error) {
	config, store, sandbox, model, source, runID := call.Config, call.Store, call.Sandbox, call.Model, call.Source, call.RunID
	providerEnv, err := SandboxProviderEnvItems(ctx, store, sandbox, ProviderFamilyOpenAI)
	if err != nil {
		return nil, err
	}
	target, err := ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
		Config: config, SessionID: sandbox.Summary.ID, PreferredProviderFamily: ProviderFamilyOpenAI, RequestedModel: model, ProviderID: "", EnvItems: providerEnv,
	})
	if err != nil {
		return nil, err
	}
	baseURL := GuestRuntimeBaseURL(config, sandbox)
	tokenValue, token, err := NewFacadeToken(NewFacadeTokenRequest{
		SandboxID: sandbox.Summary.ID, Model: target.Model.Name, ProviderID: target.Provider.ID, WireAPI: openCodeGuestWireAPI, Source: source, RunID: runID,
	})
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + sandbox.Summary.ID + "/llm/openai/v1"
	if err := WriteOpenCodeRuntimeConfig(sandbox, "agent-compose", target.Model.Name, openAIBaseURL); err != nil {
		return nil, err
	}
	env := openCodeOpenAIEnv(tokenValue, openAIBaseURL, openCodeGuestWireAPI, config)
	env["LLM_MODEL"] = "agent-compose/" + target.Model.Name
	env["OPENCODE_MODEL"] = "agent-compose/" + target.Model.Name
	return env, nil
}

func ensureOpenCodeCustomFacadeConfig(ctx context.Context, call openCodeFacadeCall) (map[string]string, error) {
	config, store, sandbox, providerID, model, source, runID := call.Config, call.Store, call.Sandbox, call.ProviderID, call.Model, call.Source, call.RunID
	target, err := resolveCustomOpenAIFacadeTarget(ctx, customOpenAIFacadeTargetRequest{
		Config:     config,
		Store:      store,
		Sandbox:    sandbox,
		ProviderID: providerID,
		Model:      model,
	})
	if err != nil {
		return nil, err
	}
	baseURL := GuestRuntimeBaseURL(config, sandbox)
	tokenValue, token, err := NewFacadeToken(NewFacadeTokenRequest{
		SandboxID: sandbox.Summary.ID, Model: target.Model.Name, ProviderID: target.Provider.ID, WireAPI: openCodeGuestWireAPI, Source: source, RunID: runID,
	})
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + sandbox.Summary.ID + "/llm/openai/v1"
	if err := WriteOpenCodeRuntimeConfig(sandbox, providerID, target.Model.Name, openAIBaseURL); err != nil {
		return nil, err
	}
	return openCodeOpenAIEnv(tokenValue, openAIBaseURL, openCodeGuestWireAPI, config), nil
}

// openCodeGuestWireAPI is the protocol opencode speaks to the facade, which is
// a property of the guest client rather than of the upstream provider.
// WriteOpenCodeRuntimeConfig registers the facade with `@ai-sdk/openai-compatible`
// (and `@ai-sdk/openai` in its default mode), both of which post chat
// completions whatever the provider behind the facade speaks. Minting the token
// for the provider's wire api instead made every responses-only provider
// unusable from opencode: the guest's chat-completions request was refused with
// "llm facade token wire api mismatch" before it could be proxied. The facade
// bridges the two protocols itself — UpstreamProtocolAndEndpoint still derives
// the upstream call from the resolved target — so the token only has to describe
// the inbound half.
const openCodeGuestWireAPI = APIProtocolChatCompletions

func openCodeOpenAIEnv(tokenValue, baseURL, protocol string, config *appconfig.Config) map[string]string {
	return map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            baseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            protocol,
		"OPENAI_API_KEY":              tokenValue,
		"OPENAI_BASE_URL":             baseURL,
		"OPENCODE_CONFIG":             GuestOpenCodeConfigPath(config),
	}
}
