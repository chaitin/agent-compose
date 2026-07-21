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
	"agent-compose/pkg/skills"
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

func (r *AgentRunner) ValidateSessionRuntime(session *domain.Sandbox) error {
	_, err := r.runtimes.ForSession(session)
	return err
}

func (r *AgentRunner) ExecuteAgentRun(ctx context.Context, session *domain.Sandbox, agent, agentDefinitionID, model, runID, message, outputSchemaJSON string, stream domain.ExecStreamWriter) (domain.ExecResult, domain.AgentRunResult, error) {
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return domain.ExecResult{}, domain.AgentRunResult{}, fmt.Errorf("session is not running")
	}
	appconfig.ApplyDefaultGuestPaths(r.config)
	vmState, err := r.store.GetVMState(session.Summary.ID)
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	runtime, err := r.runtimes.ForSession(session)
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
	agentDef, err := r.resolveAgentDefinition(ctx, session, agentDefinitionID)
	if err != nil {
		slog.Warn("resolve agent definition failed", "agent_id", strings.TrimSpace(agentDefinitionID), "error", err)
		agentDef = nil
	}
	systemPrompt := ""
	effectiveModel := strings.TrimSpace(model)
	if agentDef != nil {
		systemPrompt = strings.TrimSpace(agentDef.SystemPrompt)
		if effectiveModel == "" {
			effectiveModel = strings.TrimSpace(agentDef.Model)
		}
		execution.ApplyAgentProviderEnv(session, agentDef.EnvItems)
	}
	if err := execution.WriteAgentSystemPromptFile(session, systemPrompt); err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	var skillNames []string
	if agentDef != nil && len(agentDef.Skills) > 0 {
		resolver := skills.NewResolver(r.config)
		resolver.Env = agentSkillEnv(agentDef.EnvItems)
		resolvedSkills, err := resolver.Resolve(ctx, agentDef.Skills)
		if err != nil {
			return domain.ExecResult{}, domain.AgentRunResult{}, err
		}
		skillNames, err = execution.WriteAgentSkills(session, resolver.Projected(resolvedSkills))
		if err != nil {
			return domain.ExecResult{}, domain.AgentRunResult{}, err
		}
	} else {
		if _, err := execution.WriteAgentSkills(session, nil); err != nil {
			return domain.ExecResult{}, domain.AgentRunResult{}, err
		}
	}
	spec := BuildAgentExecSpec(r.config, session, agent, effectiveModel, promptPath, schemaPath, skillNames)
	managedEnv, err := runtimefacade.EnsureSessionLLMFacadeConfig(ctx, r.config, facadeStoreFor(r.configDB), session, agent, effectiveModel, runtimefacade.TokenSourceAgent, runID)
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	if len(managedEnv) > 0 {
		spec.Env = llms.MergeManagedExecEnv(spec.Env, managedEnv)
		if r.configDB != nil {
			if token := managedEnv["AGENT_COMPOSE_SANDBOX_TOKEN"]; token != "" {
				defer func() { _ = r.configDB.DeleteLLMFacadeToken(context.WithoutCancel(ctx), token) }()
			}
		}
	}
	if err := r.prepareAgentMCPConfig(ctx, session, agentDefinitionID, agent); err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
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

func agentSkillEnv(items []domain.SandboxEnvVar) map[string]string {
	env := domain.SandboxEnvMap(items)
	if env == nil {
		return map[string]string{}
	}
	return env
}

func (r *AgentRunner) prepareAgentMCPConfig(ctx context.Context, session *domain.Sandbox, agentDefinitionID, agent string) error {
	if session == nil {
		return nil
	}
	provider := domain.NormalizeAgentKind(agent)
	clearProvider := func() error {
		if err := execution.WriteAgentMCPConfigFile(session, nil); err != nil {
			return err
		}
		switch provider {
		case "codex":
			return llms.WriteCodexMCPConfig(session, nil)
		case "opencode":
			return llms.WriteOpenCodeMCPConfig(session, nil)
		default:
			return nil
		}
	}
	if r == nil || r.agents == nil {
		return clearProvider()
	}
	agentID := strings.TrimSpace(agentDefinitionID)
	if agentID == "" {
		return clearProvider()
	}
	definition, err := r.agents.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return clearProvider()
	}
	mcps := llms.AgentMCPConfig(definition)
	if err := execution.WriteAgentMCPConfigFile(session, mcps); err != nil {
		return err
	}
	switch provider {
	case "codex":
		return llms.WriteCodexMCPConfig(session, mcps)
	case "opencode":
		return llms.WriteOpenCodeMCPConfig(session, mcps)
	default:
		return nil
	}
}

func (r *AgentRunner) ResolveAgentSystemPrompt(ctx context.Context, session *domain.Sandbox, agentDefinitionID string) (string, error) {
	return r.resolveAgentSystemPrompt(ctx, session, agentDefinitionID)
}

func (r *AgentRunner) resolveAgentSystemPrompt(ctx context.Context, session *domain.Sandbox, agentDefinitionID string) (string, error) {
	agentDef, err := r.resolveAgentDefinition(ctx, session, agentDefinitionID)
	if err != nil {
		slog.Warn("resolve agent system prompt failed", "agent_id", strings.TrimSpace(agentDefinitionID), "error", err)
		return "", nil
	}
	if agentDef == nil {
		return "", nil
	}
	return strings.TrimSpace(agentDef.SystemPrompt), nil
}

func (r *AgentRunner) resolveAgentDefinition(ctx context.Context, session *domain.Sandbox, agentDefinitionID string) (*domain.AgentDefinition, error) {
	if r == nil || r.agents == nil || session == nil {
		return nil, nil
	}
	agentID := strings.TrimSpace(agentDefinitionID)
	if agentID == "" {
		taggedAgentID := execution.SessionTagValue(session.Summary.Tags, domain.AgentSandboxTagID)
		if !domain.SandboxHasAgentTag(session, taggedAgentID) {
			return nil, nil
		}
		agentID = taggedAgentID
	}
	if agentID == "" {
		return nil, nil
	}
	agentDef, err := r.agents.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return &agentDef, nil
}

func BuildAgentExecSpec(config *appconfig.Config, session *domain.Sandbox, agent, model, promptPath, schemaPath string, skillNames []string) domain.ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	agentHome := config.GuestHomePath
	env := execution.BuildSandboxExecEnv(config, session, agentHome)

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
	for _, skillName := range skillNames {
		if strings.TrimSpace(skillName) != "" {
			promptCommand += " --skill " + execution.ShellQuote(strings.TrimSpace(skillName))
		}
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
