package runtimefacade

import (
	"context"
	"os"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// FacadeStore is the persistence surface the runtime LLM facade needs: the LLM
// resolution / provider-bootstrap surface plus facade-token persistence.
// *configstore.ConfigStore satisfies it; depending on this interface keeps the
// facade off a direct configstore import.
//
// Callers that hold a possibly-nil concrete store must pass a true nil
// interface when the store is absent (see adapters.facadeStoreFor); wrapping a
// nil pointer in the interface would bypass the `store == nil` guards here.
type FacadeStore interface {
	llms.LLMResolverStore
	SaveLLMFacadeToken(ctx context.Context, token llms.FacadeToken) error
}

const (
	TokenSourceAgent            = "agent"
	TokenSourceSchedulerCommand = "scheduler_command"
)

// AgentRuntimeConfig is the result of configuring one agent's runtime facade.
//
// Model is the runtime command argument, including encoding for legacy guests.
// Env carries the authoritative guest model in llms.GuestModelEnvName. Model is
// empty when the agent authenticates outside the facade or nothing was resolved.
type AgentRuntimeConfig struct {
	Env   map[string]string
	Model string
}

// SessionFacadeConfigRequest bundles the config, credential store, target
// session, and requested agent/model/source/run identifiers the
// EnsureSessionXxxFacadeConfig entry points need to resolve and mint a
// facade token. EnsureSessionStartupFacadeConfig ignores Agent and Model.
type SessionFacadeConfigRequest struct {
	Config  *appconfig.Config
	Store   FacadeStore
	Session *domain.Sandbox
	Agent   string
	Model   string
	Source  string
	RunID   string
}

func EnsureSessionLLMFacadeConfig(ctx context.Context, req SessionFacadeConfigRequest) (map[string]string, error) {
	runtimeConfig, err := EnsureSessionAgentRuntimeConfig(ctx, req)
	if err != nil {
		return nil, err
	}
	return runtimeConfig.Env, nil
}

func EnsureSessionAgentRuntimeConfig(ctx context.Context, req SessionFacadeConfigRequest) (AgentRuntimeConfig, error) {
	config, store, session, agent, model, source, runID := req.Config, req.Store, req.Session, req.Agent, req.Model, req.Source, req.RunID
	if config == nil || store == nil || session == nil {
		return AgentRuntimeConfig{}, nil
	}
	var (
		env map[string]string
		err error
	)
	switch domain.NormalizeAgentKind(agent) {
	case "codex":
		env, err = llms.EnsureCodexFacadeConfig(ctx, llms.CodexFacadeConfigRequest{
			Config: config, Store: store, Sandbox: session, Model: model, Source: source, RunID: runID,
		})
	case "claude":
		env, err = ensureSessionClaudeConfig(ctx, sessionFacadeCall{Config: config, Store: store, Session: session, Model: model, Source: source, RunID: runID})
	case "opencode":
		env, err = ensureSessionOpenCodeConfig(ctx, sessionFacadeCall{Config: config, Store: store, Session: session, Model: model, Source: source, RunID: runID})
	case "pi":
		env, err = llms.EnsurePiFacadeConfig(ctx, llms.PiFacadeConfigRequest{
			Config: config, Store: store, Sandbox: session, Model: model, Source: source, RunID: runID,
		})
	case "dsh":
		env, err = llms.EnsureDshFacadeConfig(ctx, llms.DshFacadeConfigRequest{
			Config: config, Store: store, Sandbox: session, Model: model, Source: source, RunID: runID,
		})
	default:
		return AgentRuntimeConfig{}, nil
	}
	return AgentRuntimeConfig{Env: env, Model: llms.RuntimeModelArgument(agent, env[llms.GuestModelEnvName])}, err
}

// sessionFacadeCall groups the environment (Config/Store/Session) and
// per-call attribution (Model/Source/RunID) shared by the session facade
// config helpers below.
type sessionFacadeCall struct {
	Config  *appconfig.Config
	Store   FacadeStore
	Session *domain.Sandbox
	Model   string
	Source  string
	RunID   string
}

func ensureSessionClaudeConfig(ctx context.Context, call sessionFacadeCall) (map[string]string, error) {
	config, store, session, model, source, runID := call.Config, call.Store, call.Session, call.Model, call.Source, call.RunID
	baseURL := llms.GuestRuntimeBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	providerEnv, err := llms.SandboxProviderEnvItems(ctx, store, session, llms.ProviderFamilyAnthropic)
	if err != nil {
		return nil, err
	}
	// An unknown connection prefix is a configuration error, not "no managed
	// configuration". Rejecting it before resolution keeps it from being
	// swallowed below into claude's own-login fallback and from being forwarded
	// upstream as part of the model name.
	if err := llms.ValidateFacadeModelReference(ctx, llms.FacadeModelReferenceQuery{
		Config: config, Store: store, SessionID: session.Summary.ID, Model: model, EnvItems: providerEnv,
	}); err != nil {
		return nil, err
	}
	target, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
		Config: config, SessionID: session.Summary.ID, PreferredProviderFamily: llms.ProviderFamilyAnthropic, RequestedModel: model, ProviderID: "", EnvItems: providerEnv,
	})
	tokenModel := ""
	tokenProvider := ""
	if err != nil {
		if !llms.OptionalFacadeConfigError(err) || !HasAnthropicProviderKey(ctx, config, store) {
			return nil, err
		}
	} else {
		tokenModel = target.Model.Name
		tokenProvider = target.Provider.ID
	}
	tokenValue, token, err := llms.NewFacadeToken(llms.NewFacadeTokenRequest{
		SandboxID: session.Summary.ID, Model: tokenModel, ProviderID: tokenProvider, WireAPI: llms.APIProtocolMessages, Source: source, RunID: runID,
	})
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	anthropicBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + session.Summary.ID + "/llm/anthropic"
	env := map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            anthropicBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            llms.APIProtocolMessages,
		"ANTHROPIC_API_KEY":           tokenValue,
		"ANTHROPIC_AUTH_TOKEN":        tokenValue,
		"ANTHROPIC_BASE_URL":          anthropicBaseURL,
	}
	if tokenModel != "" {
		env["ANTHROPIC_MODEL"] = tokenModel
		env["CLAUDE_MODEL"] = tokenModel
		env[llms.GuestModelEnvName] = tokenModel
	}
	return env, nil
}

func ensureSessionOpenCodeConfig(ctx context.Context, call sessionFacadeCall) (map[string]string, error) {
	return llms.EnsureOpenCodeFacadeConfig(ctx, llms.OpenCodeFacadeConfigRequest{
		Config: call.Config, Store: call.Store, Sandbox: call.Session, Model: call.Model, Source: call.Source, RunID: call.RunID,
	})
}

func HasAnthropicProviderKey(ctx context.Context, config *appconfig.Config, store FacadeStore) bool {
	configKey := ""
	if config != nil {
		configKey = config.LLMAPIKey
	}
	return strings.TrimSpace(firstNonEmpty(
		llms.LookupEnvValue(ctx, store, "ANTHROPIC_API_KEY"),
		llms.LookupEnvValue(ctx, store, "ANTHROPIC_AUTH_TOKEN"),
		llms.LookupEnvValue(ctx, store, "LLM_API_KEY"),
		os.Getenv("ANTHROPIC_API_KEY"),
		os.Getenv("ANTHROPIC_AUTH_TOKEN"),
		os.Getenv("LLM_API_KEY"),
		configKey,
	)) != ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
