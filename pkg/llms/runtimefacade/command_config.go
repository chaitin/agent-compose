package runtimefacade

import (
	"context"
	"errors"
	"fmt"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// CommandFacadeStore adds precise token deletion to the normal facade store.
// A command obtains one run-scoped facade token before it starts, so cleanup
// must target exactly the token created by that command.
type CommandFacadeStore interface {
	FacadeStore
	DeleteLLMFacadeTokenHash(context.Context, string) error
}

type CommandFacadeConfig struct {
	Env         map[string]string
	TokenHashes []string
}

type trackingCommandFacadeStore struct {
	CommandFacadeStore
	tokenHashes []string
}

func (s *trackingCommandFacadeStore) SaveLLMFacadeToken(ctx context.Context, token llms.FacadeToken) error {
	if err := s.CommandFacadeStore.SaveLLMFacadeToken(ctx, token); err != nil {
		return err
	}
	s.tokenHashes = append(s.tokenHashes, token.TokenHash)
	return nil
}

// CommandFacadeConfigRequest bundles the config, credential store, target
// session, and requested agent/model/source/run identifiers
// EnsureSessionCommandFacadeConfig needs to reconstruct the transient facade
// environment on a command's in-memory Sandbox clone.
type CommandFacadeConfigRequest struct {
	Config  *appconfig.Config
	Store   CommandFacadeStore
	Session *domain.Sandbox
	Agent   string
	Model   string
	// AgentEnv is the provider environment this command declares for the agent:
	// the sandbox's own provider environment plus the command's. It decides
	// whether the agent owns its upstream, exactly as it does for a sandbox
	// start, a run, and an attached prompt.
	AgentEnv []domain.SandboxEnvVar
	Source   string
	RunID    string
}

// EnsureSessionCommandFacadeConfig prepares the managed LLM configuration of
// the command's selected agent on the command's in-memory Sandbox clone.
//
// The selected agent determines the dialect and the catalog supplies the
// connection, so there is exactly one preparation. Earlier revisions also
// provisioned a startup facade for both provider families before the agent was
// known; that belonged to the retired resolver stack, where an agent could be
// served by either family depending on the environment. PrepareAgentLLM decides
// that once, for the agent this command actually names.
//
// Any failure removes every token successfully persisted by this invocation.
// Successful callers own the returned token hashes until command termination.
func EnsureSessionCommandFacadeConfig(ctx context.Context, req CommandFacadeConfigRequest) (result CommandFacadeConfig, returnErr error) {
	config, store, session, agent, model, source, runID := req.Config, req.Store, req.Session, req.Agent, req.Model, req.Source, req.RunID
	if config == nil || store == nil || session == nil {
		return CommandFacadeConfig{}, nil
	}

	tracker := &trackingCommandFacadeStore{CommandFacadeStore: store}
	defer func() {
		if returnErr == nil {
			return
		}
		cleanupCtx := context.WithoutCancel(ctx)
		var cleanupErr error
		for _, tokenHash := range tracker.tokenHashes {
			if err := store.DeleteLLMFacadeTokenHash(cleanupCtx, tokenHash); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete command facade token: %w", err))
			}
		}
		result = CommandFacadeConfig{}
		returnErr = errors.Join(returnErr, cleanupErr)
	}()

	prepared, err := llms.PrepareAgentLLM(ctx, llms.AgentLLMRequest{
		Config: config, Store: tracker, Sandbox: session, AgentKind: agent, Model: model, AgentEnv: req.AgentEnv, Source: source, RunID: runID,
	})
	if err != nil {
		if llms.IsUnmanagedAgentLLMError(err) {
			return CommandFacadeConfig{}, nil
		}
		return CommandFacadeConfig{}, err
	}
	return CommandFacadeConfig{
		Env:         prepared.Env,
		TokenHashes: append([]string(nil), tracker.tokenHashes...),
	}, nil
}
