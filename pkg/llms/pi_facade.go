package llms

import (
	"context"
	"fmt"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// PiFacadeStore is the persistence surface needed to resolve a Pi model and
// issue its run-scoped runtime facade token.
type PiFacadeStore interface {
	LLMResolverStore
	SaveLLMFacadeToken(context.Context, FacadeToken) error
}

// PiFacadeConfigRequest bundles the config, credential store, target
// sandbox, and requested model/source/run identifiers
// EnsurePiFacadeConfig needs to resolve and mint a Pi facade token.
type PiFacadeConfigRequest struct {
	Config  *appconfig.Config
	Store   PiFacadeStore
	Sandbox *domain.Sandbox
	Model   string
	Source  string
	RunID   string
}

// EnsurePiFacadeConfig resolves Pi's model selection, writes the managed
// models.json, and returns only facade-scoped credentials. The
// <connection>/<model> prefix is optional: without it the daemon's default
// connection resolves the literal model name.
func EnsurePiFacadeConfig(ctx context.Context, req PiFacadeConfigRequest) (map[string]string, error) {
	config, store, sandbox, model, source, runID := req.Config, req.Store, req.Sandbox, req.Model, req.Source, req.RunID
	providerID, modelName, err := SplitModelReference(model)
	if err != nil {
		return nil, err
	}
	baseURL := GuestRuntimeBaseURL(config, sandbox)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}

	target, err := resolvePiFacadeTarget(ctx, piFacadeTargetInput{Config: config, Store: store, Sandbox: sandbox, ProviderID: providerID, Model: modelName})
	if err != nil {
		return nil, err
	}
	piAPI, facadeProtocol, facadeBaseURL, err := piFacadeProtocol(target, baseURL, sandbox.Summary.ID)
	if err != nil {
		return nil, err
	}
	tokenValue, token, err := NewFacadeToken(NewFacadeTokenRequest{
		SandboxID: sandbox.Summary.ID, Model: target.Model.Name, ProviderID: target.Provider.ID, WireAPI: facadeProtocol, Source: source, RunID: runID,
	})
	if err != nil {
		return nil, err
	}
	if err := WritePiRuntimeConfig(sandbox, target.Model.Name, facadeBaseURL, piAPI); err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}

	env := map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            facadeBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            facadeProtocol,
		"PI_CODING_AGENT_DIR":         GuestPiAgentDir(config),
	}
	// Pi addresses the model through the provider key WritePiRuntimeConfig
	// registers, so the guest-facing reference carries the namespace. The runner
	// passes this value to `pi --model` untouched.
	env[GuestModelEnvName] = GuestModelReference(piFacadeProviderID, target.Model.Name)
	if target.Provider.ProviderType == ProviderFamilyAnthropic {
		env["ANTHROPIC_API_KEY"] = tokenValue
	} else {
		env["OPENAI_API_KEY"] = tokenValue
	}
	return env, nil
}

// piFacadeTargetInput groups resolvePiFacadeTarget's inputs.
type piFacadeTargetInput struct {
	Config     *appconfig.Config
	Store      PiFacadeStore
	Sandbox    *domain.Sandbox
	ProviderID string
	Model      string
}

func resolvePiFacadeTarget(ctx context.Context, in piFacadeTargetInput) (ResolvedTarget, error) {
	config, store, sandbox, providerID, model := in.Config, in.Store, in.Sandbox, in.ProviderID, in.Model
	sandboxID := sandbox.Summary.ID
	envItems, err := SandboxProviderEnvItems(ctx, store, sandbox, "")
	if err != nil {
		return ResolvedTarget{}, err
	}
	if HasSessionEnvProviderInput(envItems) {
		return resolvePiEnvFacadeTarget(ctx, piEnvFacadeTargetInput{Config: config, Store: store, SandboxID: sandboxID, RequestedProviderID: providerID, Model: model, EnvItems: envItems})
	}
	if HasEnabledLLMProviderID(ctx, store, providerID) {
		return ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
			Config: config, SessionID: sandboxID, PreferredProviderFamily: "", RequestedModel: model, ProviderID: providerID, EnvItems: envItems,
		})
	}
	switch providerID {
	case "":
		// No connection prefix: the daemon default connection owns routing, and
		// the requested value is a literal model name for it.
		return ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
			Config: config, SessionID: sandboxID, PreferredProviderFamily: "", RequestedModel: model, ProviderID: "", EnvItems: envItems,
		})
	case ProviderFamilyAnthropic:
		return ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
			Config: config, SessionID: sandboxID, PreferredProviderFamily: ProviderFamilyAnthropic, RequestedModel: model, ProviderID: "", EnvItems: envItems,
		})
	case ProviderFamilyOpenAI, ProviderIDDefaultOpenAI:
		return ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
			Config: config, SessionID: sandboxID, PreferredProviderFamily: ProviderFamilyOpenAI, RequestedModel: model, ProviderID: "", EnvItems: envItems,
		})
	default:
		return resolveCustomOpenAIFacadeTarget(ctx, customOpenAIFacadeTargetRequest{
			Config:     config,
			Store:      store,
			Sandbox:    sandbox,
			ProviderID: providerID,
			Model:      model,
		})
	}
}

// piEnvFacadeTargetInput groups resolvePiEnvFacadeTarget's inputs.
type piEnvFacadeTargetInput struct {
	Config              *appconfig.Config
	Store               PiFacadeStore
	SandboxID           string
	RequestedProviderID string
	Model               string
	EnvItems            []domain.SandboxEnvVar
}

func resolvePiEnvFacadeTarget(ctx context.Context, in piEnvFacadeTargetInput) (ResolvedTarget, error) {
	config, store, sandboxID, model, envItems := in.Config, in.Store, in.SandboxID, in.Model, in.EnvItems
	family := piEnvProviderFamily(ctx, store, in.RequestedProviderID, envItems)
	if family == ProviderFamilyAnthropic {
		providerID, err := ensureSessionAnthropicEnvProviderWithConfig(ctx, store, SessionEnvProviderQuery{Config: config, SessionID: sandboxID, RequestedModel: model, EnvItems: envItems})
		if err != nil {
			return ResolvedTarget{}, err
		}
		return ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
			Config: config, SessionID: sandboxID, PreferredProviderFamily: family, RequestedModel: model, ProviderID: providerID, EnvItems: envItems,
		})
	}
	providerID, err := ensureSessionOpenAIEnvProviderWithConfig(ctx, store, SessionEnvProviderQuery{Config: config, SessionID: sandboxID, RequestedModel: model, EnvItems: envItems})
	if err != nil {
		return ResolvedTarget{}, err
	}
	return ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
		Config: config, SessionID: sandboxID, PreferredProviderFamily: ProviderFamilyOpenAI, RequestedModel: model, ProviderID: providerID, EnvItems: envItems,
	})
}

func piEnvProviderFamily(ctx context.Context, store ProviderListStore, requestedProviderID string, envItems []domain.SandboxEnvVar) string {
	switch strings.TrimSpace(requestedProviderID) {
	case ProviderFamilyAnthropic:
		return ProviderFamilyAnthropic
	case ProviderFamilyOpenAI, ProviderIDDefaultOpenAI:
		return ProviderFamilyOpenAI
	}
	if store != nil {
		providers, err := store.ListEnabledLLMProviders(ctx)
		if err == nil {
			for _, provider := range providers {
				if provider.ID == strings.TrimSpace(requestedProviderID) {
					return NormalizeProviderType(provider.ProviderType)
				}
			}
		}
	}
	if hasGenericLLMEnvProviderInput(envItems) && NormalizeWireAPI(EnvItemValue(envItems, "LLM_API_PROTOCOL")) != APIProtocolMessages {
		return ProviderFamilyOpenAI
	}
	return genericLLMEnvProviderFamily(envItems)
}

func piFacadeProtocol(target ResolvedTarget, runtimeBaseURL, sandboxID string) (piAPI, facadeProtocol, facadeBaseURL string, err error) {
	runtimeBaseURL = strings.TrimRight(runtimeBaseURL, "/")
	if target.Provider.ProviderType == ProviderFamilyAnthropic {
		// Pi's Anthropic SDK appends /v1/messages to the configured base URL.
		// Keep the facade base at the family root so the version segment appears
		// exactly once in the resulting request path.
		return "anthropic-messages", APIProtocolMessages, runtimeBaseURL + "/api/runtime/sandboxes/" + sandboxID + "/llm/anthropic", nil
	}
	if target.Provider.ProviderType != ProviderFamilyOpenAI {
		return "", "", "", fmt.Errorf("unsupported pi llm provider family %q", target.Provider.ProviderType)
	}
	protocol := NormalizeWireAPI(target.WireAPI)
	switch protocol {
	case APIProtocolResponses:
		return "openai-responses", protocol, runtimeBaseURL + "/api/runtime/sandboxes/" + sandboxID + "/llm/openai/v1", nil
	case APIProtocolChatCompletions:
		return "openai-completions", protocol, runtimeBaseURL + "/api/runtime/sandboxes/" + sandboxID + "/llm/openai/v1", nil
	default:
		return "", "", "", fmt.Errorf("unsupported pi openai wire api %q", target.WireAPI)
	}
}
