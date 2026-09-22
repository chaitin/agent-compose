package runtimefacade

import (
	"context"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// FacadeStore is the persistence surface the runtime LLM facade needs: the
// connection catalog it resolves against plus facade-token persistence.
// *configstore.ConfigStore satisfies it; depending on this interface keeps the
// facade off a direct configstore import.
//
// Callers that hold a possibly-nil concrete store must pass a true nil
// interface when the store is absent (see adapters.facadeStoreFor); wrapping a
// nil pointer in the interface would bypass the `store == nil` guards here.
type FacadeStore interface {
	llms.AgentLLMStore
}

const (
	TokenSourceAgent            = "agent"
	TokenSourceSchedulerCommand = "scheduler_command"
)

// AgentRuntimeConfig is the result of configuring one agent's runtime facade.
//
// Model is the guest-facing model reference the agent CLI must be launched
// with, which is not always the model the agent declared: pi and opencode
// address models as <provider>/<model>, and the provider key belongs to the
// daemon. Env carries the authoritative value in llms.GuestModelEnvName. Model
// is empty when the agent authenticates outside the facade, uses no managed
// LLM at all, or nothing was resolved.
type AgentRuntimeConfig struct {
	Env   map[string]string
	Model string
}

// SessionFacadeConfigRequest bundles the config, credential store, target
// session, and requested agent/model/source/run identifiers the
// EnsureSessionXxxFacadeConfig entry points need.
type SessionFacadeConfigRequest struct {
	Config  *appconfig.Config
	Store   FacadeStore
	Session *domain.Sandbox
	Agent   string
	Model   string
	// ConnectionID names the daemon connection the agent's llm_connection
	// declared. Empty infers the connection from the model.
	ConnectionID string
	// AgentEnv is the environment the agent declared for itself. When it
	// publishes an LLM connection there, the daemon configures the agent
	// against it and does not use the catalog.
	AgentEnv []domain.SandboxEnvVar
	Source   string
	RunID    string
}

func EnsureSessionLLMFacadeConfig(ctx context.Context, req SessionFacadeConfigRequest) (map[string]string, error) {
	runtimeConfig, err := EnsureSessionAgentRuntimeConfig(ctx, req)
	if err != nil {
		return nil, err
	}
	return runtimeConfig.Env, nil
}

// EnsureSessionAgentRuntimeConfig prepares the managed LLM configuration for
// one agent run.
//
// ErrNoModel means the daemon has no model to apply and the agent keeps its own
// authentication. ErrUnsupportedAgentDialect means the agent has no LLM facade
// at all. Both are ordinary outcomes for agents this daemon does not manage;
// every other error is a configuration fault the operator must see.
func EnsureSessionAgentRuntimeConfig(ctx context.Context, req SessionFacadeConfigRequest) (AgentRuntimeConfig, error) {
	if req.Config == nil || req.Store == nil || req.Session == nil {
		return AgentRuntimeConfig{}, nil
	}
	prepared, err := llms.PrepareAgentLLM(ctx, llms.AgentLLMRequest{
		Config:       req.Config,
		Store:        req.Store,
		Sandbox:      req.Session,
		AgentKind:    req.Agent,
		Model:        req.Model,
		ConnectionID: req.ConnectionID,
		AgentEnv:     req.AgentEnv,
		Source:       req.Source,
		RunID:        req.RunID,
	})
	if err != nil {
		if llms.IsUnmanagedAgentLLMError(err) {
			return AgentRuntimeConfig{}, nil
		}
		return AgentRuntimeConfig{}, err
	}
	return AgentRuntimeConfig{Env: prepared.Env, Model: prepared.GuestModel}, nil
}
