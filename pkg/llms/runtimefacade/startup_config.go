package runtimefacade

import (
	"context"
	"fmt"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// EnsureSessionStartupFacadeConfig creates the short-lived environment needed
// by commands that may run before the selected agent's exact facade config is
// applied. Each provider family gets its own provider-bound token. The common
// facade variables are intentionally left to EnsureSessionAgentRuntimeConfig,
// because one process environment cannot represent two different common
// facade tokens at once.
func EnsureSessionStartupFacadeConfig(ctx context.Context, req SessionFacadeConfigRequest) (map[string]string, error) {
	config, store, session, source, runID := req.Config, req.Store, req.Session, req.Source, req.RunID
	if config == nil || store == nil || session == nil {
		return nil, nil
	}
	baseURL := llms.GuestRuntimeBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}

	env := make(map[string]string)
	for _, family := range []string{llms.ProviderFamilyAnthropic, llms.ProviderFamilyOpenAI} {
		familyEnv, err := ensureStartupFamilyConfig(ctx, startupFamilyCall{Config: config, Store: store, Session: session, Family: family, Source: source, RunID: runID, BaseURL: baseURL})
		if err != nil {
			return nil, err
		}
		for name, value := range familyEnv {
			env[name] = value
		}
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
}

// startupFamilyCall groups ensureStartupFamilyConfig's inputs.
type startupFamilyCall struct {
	Config  *appconfig.Config
	Store   FacadeStore
	Session *domain.Sandbox
	Family  string
	Source  string
	RunID   string
	BaseURL string
}

func ensureStartupFamilyConfig(ctx context.Context, call startupFamilyCall) (map[string]string, error) {
	config, store, session, family, source, runID, baseURL := call.Config, call.Store, call.Session, call.Family, call.Source, call.RunID, call.BaseURL
	provider, available, err := llms.SelectRuntimeFacadeProvider(ctx, store, session.Summary.ID, family)
	if err != nil {
		return nil, fmt.Errorf("select %s startup facade provider: %w", family, err)
	}
	providerEnv, err := llms.SandboxProviderEnvItems(ctx, store, session, family)
	if err != nil {
		return nil, fmt.Errorf("load %s startup facade environment: %w", family, err)
	}
	famEnv := startupFamilyEnv{Config: config, Store: store, Family: family, ProviderEnv: providerEnv}
	if !available && !startupFamilyHasInput(ctx, famEnv) {
		return nil, nil
	}
	if !available {
		target, resolveErr := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
			Config: config, SessionID: session.Summary.ID, PreferredProviderFamily: family, RequestedModel: startupFamilyModel(ctx, famEnv), ProviderID: "", EnvItems: providerEnv,
		})
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve %s startup facade provider: %w", family, resolveErr)
		}
		provider = target.Provider
	}

	wireAPI := ""
	if family == llms.ProviderFamilyAnthropic {
		wireAPI = llms.APIProtocolMessages
	}
	rawToken, token, err := llms.NewFacadeToken(llms.NewFacadeTokenRequest{
		SandboxID: session.Summary.ID, Model: "", ProviderID: provider.ID, WireAPI: wireAPI, Source: source, RunID: runID,
	})
	if err != nil {
		return nil, fmt.Errorf("issue %s startup facade token: %w", family, err)
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, fmt.Errorf("save %s startup facade token: %w", family, err)
	}

	familyBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + session.Summary.ID
	if family == llms.ProviderFamilyAnthropic {
		familyBaseURL += "/llm/anthropic"
		return map[string]string{
			"ANTHROPIC_API_KEY":    rawToken,
			"ANTHROPIC_AUTH_TOKEN": rawToken,
			"ANTHROPIC_BASE_URL":   familyBaseURL,
		}, nil
	}
	return map[string]string{
		"OPENAI_API_KEY":  rawToken,
		"OPENAI_BASE_URL": familyBaseURL + "/llm/openai/v1",
	}, nil
}

// startupFamilyEnv groups the config/store plus which provider family and
// its already-loaded env items, shared by the startup-family helpers below.
type startupFamilyEnv struct {
	Config      *appconfig.Config
	Store       FacadeStore
	Family      string
	ProviderEnv []domain.SandboxEnvVar
}

func startupFamilyHasInput(ctx context.Context, in startupFamilyEnv) bool {
	config, store, family, providerEnv := in.Config, in.Store, in.Family, in.ProviderEnv
	if family == llms.ProviderFamilyAnthropic && llms.HasAnthropicEnvProviderInput(providerEnv) {
		return true
	}
	if family == llms.ProviderFamilyOpenAI && llms.HasOpenAIEnvProviderInput(providerEnv) {
		return true
	}
	protocol := startupEnvValue(ctx, config, store, "LLM_API_PROTOCOL")
	switch family {
	case llms.ProviderFamilyAnthropic:
		return firstNonEmpty(
			startupEnvValue(ctx, config, store, "ANTHROPIC_API_KEY"),
			startupEnvValue(ctx, config, store, "ANTHROPIC_AUTH_TOKEN"),
			startupEnvValue(ctx, config, store, "ANTHROPIC_BASE_URL"),
		) != "" || (llms.NormalizeWireAPI(protocol) == llms.APIProtocolMessages && startupGenericInput(ctx, config, store))
	case llms.ProviderFamilyOpenAI:
		return firstNonEmpty(
			startupEnvValue(ctx, config, store, "OPENAI_API_KEY"),
			startupEnvValue(ctx, config, store, "OPENAI_BASE_URL"),
		) != "" || (llms.NormalizeWireAPI(protocol) != llms.APIProtocolMessages && startupGenericInput(ctx, config, store))
	default:
		return false
	}
}

func startupGenericInput(ctx context.Context, config *appconfig.Config, store FacadeStore) bool {
	return firstNonEmpty(
		startupEnvValue(ctx, config, store, "LLM_API_ENDPOINT"),
		startupEnvValue(ctx, config, store, "LLM_API_KEY"),
	) != ""
}

func startupFamilyModel(ctx context.Context, in startupFamilyEnv) string {
	config, store, family, providerEnv := in.Config, in.Store, in.Family, in.ProviderEnv
	if family == llms.ProviderFamilyAnthropic {
		return firstNonEmpty(
			llms.SessionAnthropicEnvModel(providerEnv),
			startupEnvValue(ctx, config, store, "ANTHROPIC_MODEL"),
			startupEnvValue(ctx, config, store, "CLAUDE_MODEL"),
			startupEnvValue(ctx, config, store, "LLM_MODEL"),
		)
	}
	return firstNonEmpty(
		llms.EnvItemValue(providerEnv, "LLM_MODEL"),
		startupEnvValue(ctx, config, store, "LLM_MODEL"),
	)
}

func startupEnvValue(ctx context.Context, config *appconfig.Config, store FacadeStore, key string) string {
	if value := llms.LookupEnvValue(ctx, store, key); value != "" {
		return value
	}
	if config == nil {
		return ""
	}
	switch strings.ToUpper(strings.TrimSpace(key)) {
	case "LLM_API_ENDPOINT":
		return config.LLMAPIEndpoint
	case "LLM_API_PROTOCOL":
		return config.LLMAPIProtocol
	case "LLM_API_KEY":
		return config.LLMAPIKey
	case "LLM_MODEL":
		return config.LLMModel
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
