package adapters

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/llms/runtimefacade"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
)

type AgentDefinitionStore interface {
	GetAgentDefinition(context.Context, string) (domain.AgentDefinition, error)
}

type AgentRunner struct {
	config   *appconfig.Config
	store    *sessionstore.Store
	configDB *configstore.ConfigStore
	agents   AgentDefinitionStore
	runtimes RuntimeProvider
}

// facadeStoreFor converts a possibly-nil concrete config store into a
// runtimefacade.FacadeStore. Returning a true nil interface (instead of an
// interface wrapping a nil pointer) keeps runtimefacade's plain `store == nil`
// guard working, so a daemon running without an LLM store skips LLM config
// instead of panicking on a typed-nil dereference.
func facadeStoreFor(configDB *configstore.ConfigStore) runtimefacade.FacadeStore {
	if configDB == nil {
		return nil
	}
	return configDB
}

func NewAgentRunner(config *appconfig.Config, store *sessionstore.Store, configDB *configstore.ConfigStore, agents AgentDefinitionStore, runtimes RuntimeProvider) *AgentRunner {
	return &AgentRunner{config: config, store: store, configDB: configDB, agents: agents, runtimes: runtimes}
}

func (r *AgentRunner) ExecuteAgentRun(ctx context.Context, session *domain.Session, agent, agentDefinitionID, model, runID, message, outputSchemaJSON string, stream domain.ExecStreamWriter) (domain.ExecResult, domain.AgentRunResult, error) {
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return domain.ExecResult{}, domain.AgentRunResult{}, fmt.Errorf("session is not running")
	}
	appconfig.ApplyDefaultGuestPaths(r.config)
	vmState, err := r.store.GetVMState(session.Summary.ID)
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	promptPath, err := execution.WriteAgentPromptFile(r.config, session, agent, message)
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	schemaPath, err := execution.WriteAgentOutputSchemaFile(r.config, session, agent, outputSchemaJSON)
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	systemPrompt, err := r.resolveAgentSystemPrompt(ctx, session, agentDefinitionID)
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	if err := execution.WriteAgentSystemPromptFile(session, systemPrompt); err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	runtime, err := r.runtimes.ForSession(session)
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	spec := BuildAgentExecSpec(r.config, session, agent, model, promptPath, schemaPath)
	managedEnv, err := runtimefacade.EnsureSessionLLMFacadeConfig(ctx, r.config, facadeStoreFor(r.configDB), session, agent, model, runtimefacade.TokenSourceAgent, runID)
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	if len(managedEnv) > 0 {
		spec.Env = llms.MergeManagedExecEnv(spec.Env, managedEnv)
		if r.configDB != nil {
			if token := managedEnv["AGENT_COMPOSE_SESSION_TOKEN"]; token != "" {
				defer func() { _ = r.configDB.DeleteLLMFacadeToken(context.WithoutCancel(ctx), token) }()
			}
		}
	}
	result, err := runtime.ExecStream(ctx, session, vmState, spec, stream)
	if err != nil {
		return execution.SanitizeAgentExecResult(result), domain.AgentRunResult{}, err
	}
	parsed, err := execution.ParseAgentExecResult(agent, result)
	if err != nil {
		return execution.SanitizeAgentExecResult(result), domain.AgentRunResult{}, err
	}
	return execution.SanitizeAgentExecResult(result), parsed, nil
}

func (r *AgentRunner) ResolveAgentSystemPrompt(ctx context.Context, session *domain.Session, agentDefinitionID string) (string, error) {
	return r.resolveAgentSystemPrompt(ctx, session, agentDefinitionID)
}

func (r *AgentRunner) resolveAgentSystemPrompt(ctx context.Context, session *domain.Session, agentDefinitionID string) (string, error) {
	if r == nil || r.agents == nil || session == nil {
		return "", nil
	}
	agentID := strings.TrimSpace(agentDefinitionID)
	if agentID == "" {
		taggedAgentID := execution.SessionTagValue(session.Summary.Tags, domain.AgentSessionTagID)
		if !domain.SessionHasAgentTag(session, taggedAgentID) {
			return "", nil
		}
		agentID = taggedAgentID
	}
	if agentID == "" {
		return "", nil
	}
	agentDef, err := r.agents.GetAgentDefinition(ctx, agentID)
	if err != nil {
		slog.Warn("resolve agent system prompt failed", "agent_id", agentID, "error", err)
		return "", nil
	}
	return strings.TrimSpace(agentDef.SystemPrompt), nil
}

func BuildAgentExecSpec(config *appconfig.Config, session *domain.Session, agent, model, promptPath, schemaPath string) domain.ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	agentHome := config.GuestHomePath
	env := execution.BuildSessionExecEnv(config, session, agentHome)

	promptCommand := "agent-compose-runtime prompt" +
		" --provider " + execution.ShellQuote(agent) +
		" --message-file " + execution.ShellQuote(promptPath) +
		" --state-root " + execution.ShellQuote(config.GuestStateRoot) +
		" --workspace " + execution.ShellQuote(config.GuestWorkspacePath) +
		" --home " + execution.ShellQuote(agentHome)
	if strings.TrimSpace(model) != "" {
		promptCommand += " --model " + execution.ShellQuote(strings.TrimSpace(model))
	}
	if strings.TrimSpace(schemaPath) != "" {
		promptCommand += " --output-schema-file " + execution.ShellQuote(schemaPath)
	}
	command := strings.Join([]string{
		"set -e",
		"cd " + execution.ShellQuote(config.GuestWorkspacePath),
		"mkdir -p " + execution.ShellQuote(agentHome),
		promptCommand,
	}, " && ")

	return domain.ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", command},
		Env:     env,
		Cwd:     config.GuestWorkspacePath,
	}
}
