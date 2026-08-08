package runtimefacade

import (
	"context"
	"errors"
	"fmt"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

// CommandFacadeStore adds precise token deletion to the normal facade store.
// A command can create multiple family-specific tokens before it starts, so
// cleanup must target exactly the tokens created by that command.
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

// EnsureSessionCommandFacadeConfig reconstructs the transient facade
// environment on the command's in-memory Sandbox clone. Startup family
// variables are applied first and the selected agent variables are applied
// last, so the selected provider remains authoritative for overlapping names.
//
// Any failure removes every token successfully persisted by this invocation.
// Successful callers own the returned token hashes until command termination.
func EnsureSessionCommandFacadeConfig(
	ctx context.Context,
	config *appconfig.Config,
	store CommandFacadeStore,
	session *domain.Sandbox,
	agent, model, source, runID string,
) (result CommandFacadeConfig, returnErr error) {
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

	startupEnv, err := EnsureSessionStartupFacadeConfig(ctx, config, tracker, session, source, runID)
	if err != nil {
		return CommandFacadeConfig{}, err
	}
	selectedEnv, err := EnsureSessionLLMFacadeConfig(ctx, config, tracker, session, agent, model, source, runID)
	if err != nil {
		return CommandFacadeConfig{}, err
	}

	managedEnv := make(map[string]string, len(startupEnv)+len(selectedEnv))
	for name, value := range startupEnv {
		managedEnv[name] = value
	}
	for name, value := range selectedEnv {
		managedEnv[name] = value
	}
	if len(managedEnv) == 0 {
		managedEnv = nil
	}
	return CommandFacadeConfig{
		Env:         managedEnv,
		TokenHashes: append([]string(nil), tracker.tokenHashes...),
	}, nil
}
