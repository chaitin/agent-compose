package adapters

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/llms"
	"github.com/chaitin/agent-compose/pkg/llms/runtimefacade"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
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
	// Detach runs the agent behind the guest supervisor; nil keeps the
	// direct exec.
	Detach *AgentRunDetach
}

// AgentRunDetach is the detached-execution part of an agent run.
type AgentRunDetach struct {
	ExecutionID string
	// CursorPath is the host file recording the journal offset already
	// delivered, so a later daemon resumes without duplicating output.
	CursorPath string
	// OnStart records the execution on the run before the task starts.
	OnStart func(deadline time.Time) error
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
	// The agent's own environment decides which side owns the upstream: an
	// agent that publishes an LLM connection there is configured against it,
	// and one that publishes none falls back to the daemon's catalog.
	//
	// The declaration is the sandbox's prepared provider environment merged
	// with the definition this run resolved, the same one the sandbox start
	// path used, so a run cannot decide the mode differently from the
	// environment the guest was prepared with.
	var agentDefEnvItems []domain.SandboxEnvVar
	if agentDef != nil {
		agentDefEnvItems = agentDef.EnvItems
	}
	runtimeConfig, err := runtimefacade.EnsureSessionAgentRuntimeConfig(ctx, runtimefacade.SessionFacadeConfigRequest{
		Config: r.config, Store: facadeStoreFor(r.configDB), Session: session, Agent: agent, Model: effectiveModel, AgentEnv: session.DeclaredProviderEnv(agentDefEnvItems), Source: runtimefacade.TokenSourceAgent, RunID: runID,
	})
	if err != nil {
		return domain.ExecResult{}, domain.AgentRunResult{}, err
	}
	runtimeModel := effectiveModel
	if strings.TrimSpace(runtimeConfig.Model) != "" {
		runtimeModel = strings.TrimSpace(runtimeConfig.Model)
	}
	spec := BuildAgentExecSpec(ctx, r.config, AgentExecSpecRequest{
		Session:    session,
		Agent:      agent,
		Model:      runtimeModel,
		PromptPath: promptPath,
		SchemaPath: schemaPath,
		SkillNames: skillNames,
	})
	spec.Env["AGENT_COMPOSE_RUN_ID"] = runID
	spec.Env["AGENT_COMPOSE_PROJECT_ID"] = ""
	if agentDef != nil {
		spec.Env["AGENT_COMPOSE_PROJECT_ID"] = agentDef.ProjectID
	}
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
	var result domain.ExecResult
	if req.Detach != nil {
		result, err = r.executeDetachedAgent(ctx, detachedAgentExec{
			Session: session, VMState: vmState, Runtime: runtime, Env: spec.Env, Detach: req.Detach, Stream: stream,
			TaskArgs: agentPromptArgs(r.config, AgentExecSpecRequest{
				Agent: agent, Model: runtimeModel, PromptPath: promptPath, SchemaPath: schemaPath, SkillNames: skillNames,
			}),
		})
	} else {
		result, err = runtime.ExecStream(ctx, session, vmState, spec, stream)
	}
	if err != nil {
		// A disconnected detached run keeps running in the guest and still
		// needs its per-run token; the daemon that finishes it retires it.
		retainFacadeToken = errors.Is(err, domain.ErrExecTerminationUnconfirmed) || errors.Is(err, runs.ErrRunDisconnected)
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

type detachedAgentExec struct {
	Session  *domain.Sandbox
	VMState  domain.VMState
	Runtime  SandboxRuntime
	Env      map[string]string
	TaskArgs []string
	Detach   *AgentRunDetach
	Stream   domain.ExecStreamWriter
}

// executeDetachedAgent starts the agent behind the guest supervisor and
// follows it. The run is recorded as detached before the task starts.
func (r *AgentRunner) executeDetachedAgent(ctx context.Context, in detachedAgentExec) (domain.ExecResult, error) {
	if in.Detach.OnStart != nil {
		deadline, _ := ctx.Deadline()
		if err := in.Detach.OnStart(deadline); err != nil {
			return domain.ExecResult{}, fmt.Errorf("persist detached run fields: %w", err)
		}
	}
	task := r.detachedAgentTask(in)
	task.TaskTimeout = runs.RemainingTimeout(ctx)
	return runs.ExecuteDetachedTask(ctx, task)
}

func (r *AgentRunner) detachedAgentTask(in detachedAgentExec) runs.DetachedTask {
	cursorPath := in.Detach.CursorPath
	return runs.DetachedTask{
		Config:      r.config,
		Runtime:     in.Runtime,
		Sandbox:     in.Session,
		VMState:     in.VMState,
		ExecutionID: in.Detach.ExecutionID,
		TaskArgs:    in.TaskArgs,
		Env:         in.Env,
		Cursor:      runs.ReadDetachCursorFile(cursorPath),
		Writer:      in.Stream,
		PersistCursor: func(offset uint64) error {
			return runs.WriteDetachCursorFile(cursorPath, offset)
		},
	}
}

// AgentResumeRequest identifies a detached agent run to re-attach to.
type AgentResumeRequest struct {
	Session *domain.Sandbox
	Agent   string
	RunID   string
	Detach  *AgentRunDetach
}

// ResumeAgentRun re-attaches to a detached agent run started by an earlier
// daemon and parses its result. It replays the whole journal so the result
// payload is found even when it precedes the delivered cursor; output already
// delivered is not forwarded again. Once the run is over the per-run facade
// token that was kept for it is retired.
func (r *AgentRunner) ResumeAgentRun(ctx context.Context, req AgentResumeRequest, stream domain.ExecStreamWriter) (domain.ExecResult, domain.AgentRunResult, error) {
	session, agent, runID := req.Session, req.Agent, req.RunID
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
	env := execution.BuildSandboxExecEnv(ctx, r.config, session, r.config.GuestHomePath)
	task := r.detachedAgentTask(detachedAgentExec{Session: session, VMState: vmState, Runtime: runtime, Env: env, Detach: req.Detach, Stream: stream})
	task.ReplayAll = true
	result, err := runs.FollowDetachedTask(ctx, task)
	if errors.Is(err, runs.ErrRunDisconnected) {
		return execution.SanitizeAgentExecResult(result), domain.AgentRunResult{}, err
	}
	if r.configDB != nil && !errors.Is(err, domain.ErrExecTerminationUnconfirmed) {
		_ = r.configDB.DeleteLLMFacadeTokensForRun(context.WithoutCancel(ctx), runID, runtimefacade.TokenSourceAgent)
	}
	if err != nil {
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
	var definitionEnvItems []domain.SandboxEnvVar
	if definition != nil {
		definitionEnvItems = definition.EnvItems
	}
	startupEnv, err := runtimefacade.EnsureSessionStartupFacadeConfig(ctx, runtimefacade.SessionFacadeConfigRequest{
		Config: r.config, Store: facadeStoreFor(r.configDB), Session: session,
		Agent: agent.Provider, Model: agent.Model,
		AgentEnv: session.DeclaredProviderEnv(definitionEnvItems), Source: runtimefacade.TokenSourceAgent, RunID: "",
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
	// A sandbox start, a release resume, and a run all decide direct versus
	// managed from the same declaration: the provider environment the sandbox
	// carries plus the definition it was prepared for.
	managedEnv, err := runtimefacade.EnsureSessionLLMFacadeConfig(ctx, runtimefacade.SessionFacadeConfigRequest{
		Config: r.config, Store: facadeStoreFor(r.configDB), Session: session, Agent: agent.Provider, Model: agent.Model, AgentEnv: session.DeclaredProviderEnv(definitionEnvItems), Source: "session", RunID: "",
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

// agentPromptArgs is the `agent-compose-runtime prompt` subcommand and flags
// for one agent run. The legacy path runs it through a shell; the detached
// path hands it to the guest supervisor as task args. config must already
// have its guest path defaults applied.
func agentPromptArgs(config *appconfig.Config, req AgentExecSpecRequest) []string {
	model, schemaPath := req.Model, req.SchemaPath
	args := []string{
		"prompt",
		"--provider", req.Agent,
		"--message-file", req.PromptPath,
		"--state-root", config.GuestStateRoot,
		"--workspace", config.GuestWorkspacePath,
		"--home", config.GuestHomePath,
	}
	if strings.TrimSpace(model) != "" {
		args = append(args, "--model", strings.TrimSpace(model))
	}
	if strings.TrimSpace(schemaPath) != "" {
		args = append(args, "--output-schema-file", schemaPath)
	}
	for _, skillName := range req.SkillNames {
		if strings.TrimSpace(skillName) != "" {
			args = append(args, "--skill", strings.TrimSpace(skillName))
		}
	}
	return args
}

func BuildAgentExecSpec(ctx context.Context, config *appconfig.Config, req AgentExecSpecRequest) domain.ExecSpec {
	session, agent, model := req.Session, req.Agent, req.Model
	promptPath, schemaPath, skillNames := req.PromptPath, req.SchemaPath, req.SkillNames
	localConfig := *config
	appconfig.ApplyDefaultGuestPaths(&localConfig)
	config = &localConfig
	agentHome := config.GuestHomePath
	env := execution.BuildSandboxExecEnv(ctx, config, session, agentHome)

	// The args are the subcommand followed by flag/value pairs; only the
	// values are quoted, as the command line always was.
	args := agentPromptArgs(config, AgentExecSpecRequest{
		Agent: agent, Model: model, PromptPath: promptPath, SchemaPath: schemaPath, SkillNames: skillNames,
	})
	words := make([]string, 0, len(args))
	for i, arg := range args {
		if i > 0 && i%2 == 0 {
			arg = execution.ShellQuote(arg)
		}
		words = append(words, arg)
	}
	promptCommand := "agent-compose-runtime " + strings.Join(words, " ")
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
