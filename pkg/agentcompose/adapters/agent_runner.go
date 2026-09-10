package adapters

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/llms"
	"github.com/chaitin/agent-compose/pkg/llms/runtimefacade"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/skills"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

type AgentDefinitionStore interface {
	GetAgentDefinition(context.Context, string) (domain.AgentDefinition, error)
}

type AgentRunner struct {
	config   *appconfig.Config
	store    *sandboxstore.Store
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

// AgentRunnerDeps bundles NewAgentRunner's dependencies.
type AgentRunnerDeps struct {
	Config   *appconfig.Config
	Store    *sandboxstore.Store
	ConfigDB *configstore.ConfigStore
	Agents   AgentDefinitionStore
	Runtimes RuntimeProvider
}

func NewAgentRunner(deps AgentRunnerDeps) *AgentRunner {
	return &AgentRunner{config: deps.Config, store: deps.Store, configDB: deps.ConfigDB, agents: deps.Agents, runtimes: deps.Runtimes}
}

func (r *AgentRunner) ValidateSessionRuntime(session *domain.Sandbox) error {
	_, err := r.runtimes.ForSession(session)
	return err
}

// AgentRunRequest bundles the session and run identifiers/inputs
// ExecuteAgentRun needs, as opposed to stream, the output callback.
type AgentRunRequest struct {
	Session           *domain.Sandbox
	Agent             string
	AgentDefinitionID string
	Model             string
	RunID             string
	Message           string
	OutputSchemaJSON  string
}

func (r *AgentRunner) ExecuteAgentRun(ctx context.Context, req AgentRunRequest, stream domain.ExecStreamWriter) (domain.ExecResult, domain.AgentRunResult, error) {
	session, agent, agentDefinitionID := req.Session, req.Agent, req.AgentDefinitionID
	model, runID, message, outputSchemaJSON := req.Model, req.RunID, req.Message, req.OutputSchemaJSON
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return domain.ExecResult{}, domain.AgentRunResult{}, fmt.Errorf("session is not running")
	}
	// Defaulting writes fields even when they already have their default value.
	// Each execution owns its config copy so concurrent sandboxes do not race.
	localRunner, localConfig := *r, *r.config
	appconfig.ApplyDefaultGuestPaths(&localConfig)
	localRunner.config = &localConfig
	r = &localRunner
	vmState, err := r.store.GetVMState(session.Summary.ID)
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	runtime, err := r.runtimes.ForSession(session)
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	guestFileWriter := r.guestFileWriterFor(session)
	promptPath, err := execution.WriteAgentPromptFile(ctx, execution.AgentPromptFileRequest{
		Config: r.config, Sandbox: session, Agent: agent, Message: message, WriteGuestFile: guestFileWriter,
	})
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	schemaPath, err := execution.WriteAgentOutputSchemaFile(ctx, execution.AgentOutputSchemaFileRequest{
		Config: r.config, Sandbox: session, Agent: agent, SchemaJSON: outputSchemaJSON, WriteGuestFile: guestFileWriter,
	})
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	agentDef, err := r.resolveAgentDefinition(ctx, session, agentDefinitionID)
	if err != nil {
		slog.Warn("resolve agent definition failed", "agent_id", strings.TrimSpace(agentDefinitionID), "error", err)
		agentDef = nil
	}
	effectiveModel := strings.TrimSpace(model)
	if agentDef != nil {
		if effectiveModel == "" {
			effectiveModel = strings.TrimSpace(agentDef.Model)
		}
	}
	skillNames, err := r.prepareAgentFiles(ctx, session, execution.AgentConfig{
		Provider:          agent,
		AgentDefinitionID: agentDefinitionID,
		Model:             effectiveModel,
	}, agentDef)
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	runtimeConfig, err := runtimefacade.EnsureSessionAgentRuntimeConfig(ctx, runtimefacade.SessionFacadeConfigRequest{
		Config: r.config, Store: facadeStoreFor(r.configDB), Session: session, Agent: agent, Model: effectiveModel, Source: runtimefacade.TokenSourceAgent, RunID: runID,
	})
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	runtimeModel := effectiveModel
	if strings.TrimSpace(runtimeConfig.Model) != "" {
		runtimeModel = strings.TrimSpace(runtimeConfig.Model)
	}
	spec := BuildAgentExecSpec(r.config, AgentExecSpecRequest{
		Session:    session,
		Agent:      agent,
		Model:      runtimeModel,
		PromptPath: promptPath,
		SchemaPath: schemaPath,
		SkillNames: skillNames,
	})
	managedEnv := runtimeConfig.Env
	retainFacadeToken := false
	if len(managedEnv) > 0 {
		spec.Env = llms.MergeManagedExecEnv(spec.Env, managedEnv)
		if r.configDB != nil {
			if token := managedEnv["AGENT_COMPOSE_SANDBOX_TOKEN"]; token != "" {
				defer func() {
					if !retainFacadeToken {
						_ = r.configDB.DeleteLLMFacadeToken(context.WithoutCancel(ctx), token)
					}
				}()
			}
		}
	}
	if err := r.syncPiRuntimeConfigToGuest(ctx, session, agent); err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	if err := r.prepareAgentMCPConfig(ctx, session, agent, agentDef); err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	result, err := runtime.ExecStream(ctx, session, vmState, spec, stream)
	if err != nil {
		retainFacadeToken = errors.Is(err, domain.ErrExecTerminationUnconfirmed)
		return execution.SanitizeAgentExecResult(result), domain.AgentRunResult{}, err
	}
	parsed, err := execution.ParseAgentExecResult(agent, result)
	if err != nil {
		return execution.SanitizeAgentExecResult(result), domain.AgentRunResult{}, err
	}
	if strings.EqualFold(strings.TrimSpace(parsed.StopReason), "cancelled") {
		return execution.SanitizeAgentExecResult(result), parsed, context.Canceled
	}
	return execution.SanitizeAgentExecResult(result), parsed, nil
}

func (r *AgentRunner) PrepareSandboxAgentEnvironment(ctx context.Context, session *domain.Sandbox, agent execution.AgentConfig, definition *domain.AgentDefinition) error {
	if session == nil {
		return fmt.Errorf("sandbox is required")
	}
	// Defaulting writes fields even when they already have their default value.
	// Each execution owns its config copy so concurrent sandboxes do not race.
	localRunner, localConfig := *r, *r.config
	appconfig.ApplyDefaultGuestPaths(&localConfig)
	localRunner.config = &localConfig
	r = &localRunner
	agent.Provider = domain.NormalizeAgentKind(agent.Provider)
	if agent.Provider == "" {
		agent.Provider = domain.DefaultAgentProvider
	}
	if definition != nil {
		if agent.AgentDefinitionID == "" {
			agent.AgentDefinitionID = strings.TrimSpace(definition.ID)
		}
		if agent.Model == "" {
			agent.Model = strings.TrimSpace(definition.Model)
		}
	}
	if r.configDB != nil {
		if err := r.configDB.RevokeLLMFacadeTokensForSandbox(ctx, session.Summary.ID); err != nil {
			return err
		}
	}
	startupEnv, err := runtimefacade.EnsureSessionStartupFacadeConfig(ctx, runtimefacade.SessionFacadeConfigRequest{
		Config: r.config, Store: facadeStoreFor(r.configDB), Session: session, Source: runtimefacade.TokenSourceAgent, RunID: "",
	})
	if err != nil {
		if r.configDB != nil {
			_ = r.configDB.RevokeLLMFacadeTokensForSandbox(context.WithoutCancel(ctx), session.Summary.ID)
		}
		return err
	}
	// Seed private directories before publishing per-run managed files. In
	// particular, a later home archive must not replace the canonical skills
	// publication or materialize the provider projection a second time.
	if err := r.syncSandboxGuestDirectories(ctx, session); err != nil {
		if r.configDB != nil {
			_ = r.configDB.RevokeLLMFacadeTokensForSandbox(context.WithoutCancel(ctx), session.Summary.ID)
		}
		return err
	}
	if _, err := r.prepareAgentFiles(ctx, session, agent, definition); err != nil {
		if r.configDB != nil {
			_ = r.configDB.RevokeLLMFacadeTokensForSandbox(context.WithoutCancel(ctx), session.Summary.ID)
		}
		return err
	}
	managedEnv, err := runtimefacade.EnsureSessionLLMFacadeConfig(ctx, runtimefacade.SessionFacadeConfigRequest{
		Config: r.config, Store: facadeStoreFor(r.configDB), Session: session, Agent: agent.Provider, Model: agent.Model, Source: "session", RunID: "",
	})
	if err != nil {
		if r.configDB != nil {
			_ = r.configDB.RevokeLLMFacadeTokensForSandbox(context.WithoutCancel(ctx), session.Summary.ID)
		}
		return err
	}
	if err := r.syncPiRuntimeConfigToGuest(ctx, session, agent.Provider); err != nil {
		if r.configDB != nil {
			_ = r.configDB.RevokeLLMFacadeTokensForSandbox(context.WithoutCancel(ctx), session.Summary.ID)
		}
		return err
	}
	if err := r.prepareAgentMCPConfig(ctx, session, agent.Provider, definition); err != nil {
		if r.configDB != nil {
			_ = r.configDB.RevokeLLMFacadeTokensForSandbox(context.WithoutCancel(ctx), session.Summary.ID)
		}
		return err
	}
	if len(startupEnv) > 0 {
		session.RuntimeEnvItems = domain.MergeEnvItems(session.RuntimeEnvItems, llms.EnvItemsFromMap(startupEnv, true))
	}
	if len(managedEnv) > 0 {
		session.RuntimeEnvItems = domain.MergeEnvItems(session.RuntimeEnvItems, llms.EnvItemsFromMap(managedEnv, true))
	}
	return nil
}

func (r *AgentRunner) PrepareSandboxAgentEnvironmentFromTags(ctx context.Context, session *domain.Sandbox) error {
	if r == nil {
		return fmt.Errorf("agent runner is required")
	}
	if session == nil {
		return fmt.Errorf("sandbox is required")
	}
	providerTag := domain.NormalizeAgentKind(execution.SessionTagValue(session.Summary.Tags, domain.AgentSandboxTagProvider))
	provider := providerTag
	if provider == "" {
		provider = domain.DefaultAgentProvider
	}
	agent := execution.AgentConfig{Provider: provider}
	var definition *domain.AgentDefinition
	taggedAgentID := execution.SessionTagValue(session.Summary.Tags, domain.AgentSandboxTagID)
	if taggedAgentID != "" && (domain.SandboxHasAgentTag(session, taggedAgentID) || execution.SessionTagValue(session.Summary.Tags, domain.AgentSandboxTagProvider) != "") {
		if r.agents == nil {
			return fmt.Errorf("agent definition store is required")
		}
		resolved, err := r.agents.GetAgentDefinition(ctx, taggedAgentID)
		if err != nil {
			return fmt.Errorf("resolve sandbox agent definition %s: %w", taggedAgentID, err)
		}
		if !resolved.Enabled {
			return fmt.Errorf("sandbox agent definition %s is disabled", taggedAgentID)
		}
		definition = &resolved
		agent = execution.AgentConfigFromDefinition(resolved, domain.DefaultAgentProvider)
		if providerTag != "" {
			agent.Provider = providerTag
		}
	}
	return r.PrepareSandboxAgentEnvironment(ctx, session, agent, definition)
}

func (r *AgentRunner) prepareAgentFiles(ctx context.Context, session *domain.Sandbox, agent execution.AgentConfig, definition *domain.AgentDefinition) ([]string, error) {
	systemPrompt := ""
	if definition != nil {
		systemPrompt = strings.TrimSpace(definition.SystemPrompt)
	}
	if err := execution.WriteAgentSystemPromptFile(ctx, r.config, session, systemPrompt, r.guestFileWriterFor(session)); err != nil {
		return nil, err
	}
	var skillNames []string
	if definition != nil && len(definition.Skills) > 0 {
		resolver := skills.NewResolver(r.config)
		resolver.Env = agentSkillEnv(definition.EnvItems)
		err := resolver.WithResolved(ctx, definition.Skills, func(resolvedSkills []skills.ResolvedSkill) error {
			var writeErr error
			skillNames, writeErr = execution.WriteAgentSkills(ctx, r.config, session, resolver.Projected(resolvedSkills), r.guestSkillsWriterFor(session))
			return writeErr
		})
		if err != nil {
			return nil, err
		}
	} else if _, err := execution.WriteAgentSkills(ctx, r.config, session, nil, r.guestSkillsWriterFor(session)); err != nil {
		return nil, err
	}
	return skillNames, nil
}

func agentSkillEnv(items []domain.SandboxEnvVar) map[string]string {
	env := domain.SandboxEnvMap(items)
	if env == nil {
		return map[string]string{}
	}
	return env
}

func (r *AgentRunner) prepareAgentMCPConfig(ctx context.Context, session *domain.Sandbox, agent string, definition *domain.AgentDefinition) error {
	var mcps map[string]compose.NormalizedMCPServerSpec
	if definition != nil {
		mcps = llms.AgentMCPConfig(*definition)
	}
	guestFileWriter := r.guestFileWriterFor(session)
	if err := execution.WriteAgentMCPConfigFile(ctx, r.config, session, mcps, guestFileWriter); err != nil {
		return err
	}
	switch domain.NormalizeAgentKind(agent) {
	case "codex":
		return llms.WriteCodexMCPConfig(ctx, r.config, session, mcps, guestFileWriter)
	case "opencode":
		return llms.WriteOpenCodeMCPConfig(ctx, r.config, session, mcps, guestFileWriter)
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

// AgentExecSpecRequest bundles the session and prompt/schema/skill inputs
// BuildAgentExecSpec needs to build the guest exec command for an agent run.
type AgentExecSpecRequest struct {
	Session    *domain.Sandbox
	Agent      string
	Model      string
	PromptPath string
	SchemaPath string
	SkillNames []string
}

func BuildAgentExecSpec(config *appconfig.Config, req AgentExecSpecRequest) domain.ExecSpec {
	session, agent, model := req.Session, req.Agent, req.Model
	promptPath, schemaPath, skillNames := req.PromptPath, req.SchemaPath, req.SkillNames
	localConfig := *config
	appconfig.ApplyDefaultGuestPaths(&localConfig)
	config = &localConfig
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
