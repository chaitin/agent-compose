package llms

import (
	"context"
	"fmt"
	"strings"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

// PiFacadeStore is the persistence surface needed to resolve a Pi model and
// issue its run-scoped runtime facade token.
type PiFacadeStore interface {
	LLMResolverStore
	SaveLLMFacadeToken(context.Context, FacadeToken) error
}

// SplitPiModel parses Pi's required <llm-provider-id>/<model-name> selection.
func SplitPiModel(value string) (string, string, error) {
	providerID, model, ok := strings.Cut(strings.TrimSpace(value), "/")
	providerID = strings.TrimSpace(providerID)
	model = strings.TrimSpace(model)
	if !ok || providerID == "" || model == "" {
		return "", "", domain.ClassifyError(domain.ErrRequired, "pi model must use <llm-provider-id>/<model-name>", nil)
	}
	return providerID, model, nil
}

// EnsurePiFacadeConfig resolves Pi's explicit provider/model selection, writes
// the managed models.json, and returns only facade-scoped credentials.
func EnsurePiFacadeConfig(ctx context.Context, config *appconfig.Config, store PiFacadeStore, sandbox *domain.Sandbox, model, source, runID string) (map[string]string, error) {
	providerID, modelName, err := SplitPiModel(model)
	if err != nil {
		return nil, err
	}
	baseURL := GuestRuntimeBaseURL(config, sandbox)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}

	target, err := resolvePiFacadeTarget(ctx, config, store, sandbox, providerID, modelName)
	if err != nil {
		return nil, err
	}
	piAPI, facadeProtocol, facadeBaseURL, err := piFacadeProtocol(target, baseURL, sandbox.Summary.ID)
	if err != nil {
		return nil, err
	}
	tokenValue, token, err := NewFacadeToken(sandbox.Summary.ID, target.Model.Name, target.Provider.ID, facadeProtocol, source, runID)
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	if err := WritePiRuntimeConfig(sandbox, target.Model.Name, facadeBaseURL, piAPI); err != nil {
		return nil, err
	}

	env := map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            facadeBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            facadeProtocol,
		"PI_CODING_AGENT_DIR":         GuestPiAgentDir(config),
	}
	if target.Provider.ProviderType == ProviderFamilyAnthropic {
		env["ANTHROPIC_API_KEY"] = tokenValue
	} else {
		env["OPENAI_API_KEY"] = tokenValue
	}
	return env, nil
}

func resolvePiFacadeTarget(ctx context.Context, config *appconfig.Config, store PiFacadeStore, sandbox *domain.Sandbox, providerID, model string) (ResolvedTarget, error) {
	envItems := openCodeProviderEnvItems(sandbox)
	sandboxID := sandbox.Summary.ID
	if HasEnabledLLMProviderID(ctx, store, providerID) {
		return ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sandboxID, "", model, providerID, envItems)
	}
	switch providerID {
	case ProviderFamilyAnthropic:
		return ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sandboxID, ProviderFamilyAnthropic, model, "", envItems)
	case ProviderFamilyOpenAI, ProviderIDDefaultOpenAI:
		return ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sandboxID, ProviderFamilyOpenAI, model, "", envItems)
	default:
		return resolveOpenCodeCustomFacadeTarget(ctx, config, store, sandbox, providerID, model)
	}
}

func piFacadeProtocol(target ResolvedTarget, runtimeBaseURL, sandboxID string) (piAPI, facadeProtocol, facadeBaseURL string, err error) {
	runtimeBaseURL = strings.TrimRight(runtimeBaseURL, "/")
	if target.Provider.ProviderType == ProviderFamilyAnthropic {
		return "anthropic-messages", APIProtocolMessages, runtimeBaseURL + "/api/runtime/sandboxes/" + sandboxID + "/llm/anthropic/v1", nil
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
