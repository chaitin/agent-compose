package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	loaderspkg "agent-compose/pkg/loaders"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestResolveAgentSystemPromptReturnsEmptyWhenAgentPromptUnset(t *testing.T) {
	ctx := context.Background()
	configDB := newTestConfigStore(t)
	created, err := configDB.CreateAgentDefinition(ctx, AgentDefinition{
		ID:       "agent-empty-prompt",
		Name:     "Empty Prompt",
		Provider: "codex",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	executor := &Executor{configDB: configDB}
	session := &Session{
		Summary: SessionSummary{
			Tags: []SessionTag{
				{Name: agentSessionTagSource, Value: agentSessionTagSourceVal},
				{Name: agentSessionTagID, Value: created.ID},
			},
		},
	}

	got, err := executor.resolveAgentSystemPrompt(ctx, session, "")
	if err != nil {
		t.Fatalf("resolveAgentSystemPrompt returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("resolveAgentSystemPrompt = %q, want empty", got)
	}
}

func TestResolveAgentSystemPromptFromProviderAgentID(t *testing.T) {
	ctx := context.Background()
	configDB := newTestConfigStore(t)
	created, err := configDB.CreateAgentDefinition(ctx, AgentDefinition{
		ID:           "agent-explicit-id",
		Name:         "Explicit",
		Provider:     "codex",
		SystemPrompt: "Use explicit agent id",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	executor := &Executor{configDB: configDB}
	session := &Session{Summary: SessionSummary{Tags: []SessionTag{}}}

	got, err := executor.resolveAgentSystemPrompt(ctx, session, created.ID)
	if err != nil {
		t.Fatalf("resolveAgentSystemPrompt returned error: %v", err)
	}
	if got != "Use explicit agent id" {
		t.Fatalf("resolveAgentSystemPrompt = %q, want %q", got, "Use explicit agent id")
	}
}

func TestResolveAgentSystemPromptPrefersProviderAgentOverSessionTag(t *testing.T) {
	ctx := context.Background()
	configDB := newTestConfigStore(t)
	tagged, err := configDB.CreateAgentDefinition(ctx, AgentDefinition{
		ID:           "agent-tagged",
		Name:         "Tagged",
		Provider:     "codex",
		SystemPrompt: "From session tag",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	explicit, err := configDB.CreateAgentDefinition(ctx, AgentDefinition{
		ID:           "agent-explicit",
		Name:         "Explicit",
		Provider:     "codex",
		SystemPrompt: "From provider agent id",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	executor := &Executor{configDB: configDB}
	session := &Session{
		Summary: SessionSummary{
			Tags: []SessionTag{
				{Name: agentSessionTagSource, Value: agentSessionTagSourceVal},
				{Name: agentSessionTagID, Value: tagged.ID},
			},
		},
	}

	got, err := executor.resolveAgentSystemPrompt(ctx, session, explicit.ID)
	if err != nil {
		t.Fatalf("resolveAgentSystemPrompt returned error: %v", err)
	}
	if got != "From provider agent id" {
		t.Fatalf("resolveAgentSystemPrompt = %q, want %q", got, "From provider agent id")
	}
}

func TestResolveAgentSystemPromptReturnsEmptyWhenAgentMissing(t *testing.T) {
	ctx := context.Background()
	executor := &Executor{configDB: newTestConfigStore(t)}
	session := &Session{Summary: SessionSummary{Tags: []SessionTag{}}}

	got, err := executor.resolveAgentSystemPrompt(ctx, session, "missing-agent-id")
	if err != nil {
		t.Fatalf("resolveAgentSystemPrompt returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("resolveAgentSystemPrompt = %q, want empty", got)
	}
}

func TestResolveAgentSystemPromptFromSessionTags(t *testing.T) {
	ctx := context.Background()
	configDB := newTestConfigStore(t)
	created, err := configDB.CreateAgentDefinition(ctx, AgentDefinition{
		ID:           "agent-with-prompt",
		Name:         "Runner",
		Provider:     "codex",
		SystemPrompt: "  Reply only in Chinese  ",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	executor := &Executor{configDB: configDB}
	session := &Session{
		Summary: SessionSummary{
			Tags: []SessionTag{
				{Name: agentSessionTagSource, Value: agentSessionTagSourceVal},
				{Name: agentSessionTagID, Value: created.ID},
			},
		},
	}

	got, err := executor.resolveAgentSystemPrompt(ctx, session, "")
	if err != nil {
		t.Fatalf("resolveAgentSystemPrompt returned error: %v", err)
	}
	if got != "Reply only in Chinese" {
		t.Fatalf("resolveAgentSystemPrompt = %q, want %q", got, "Reply only in Chinese")
	}
}

func TestWriteAgentSystemPromptFileWritesFixedPath(t *testing.T) {
	root := t.TempDir()
	session := &Session{Summary: SessionSummary{WorkspacePath: filepath.Join(root, "workspace")}}

	if err := writeAgentSystemPromptFile(session, "Reply only in Chinese"); err != nil {
		t.Fatalf("writeAgentSystemPromptFile returned error: %v", err)
	}
	hostPath := filepath.Join(root, "state", "agents", "system-prompts", agentSystemPromptFileName)
	content, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", hostPath, err)
	}
	if string(content) != "Reply only in Chinese" {
		t.Fatalf("file content = %q", string(content))
	}
}

func TestWriteAgentSystemPromptFileRemovesFileWhenPromptEmpty(t *testing.T) {
	root := t.TempDir()
	session := &Session{Summary: SessionSummary{WorkspacePath: filepath.Join(root, "workspace")}}
	hostPath := hostAgentSystemPromptPath(session)

	if err := writeAgentSystemPromptFile(session, "Reply only in Chinese"); err != nil {
		t.Fatalf("writeAgentSystemPromptFile returned error: %v", err)
	}
	if err := writeAgentSystemPromptFile(session, "   "); err != nil {
		t.Fatalf("writeAgentSystemPromptFile returned error: %v", err)
	}
	if _, err := os.Stat(hostPath); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, stat err = %v", hostPath, err)
	}
}

func TestHostAgentSystemPromptPathUsesSessionWorkspace(t *testing.T) {
	root := t.TempDir()
	session := &Session{Summary: SessionSummary{WorkspacePath: filepath.Join(root, "workspace")}}

	got := hostAgentSystemPromptPath(session)
	want := filepath.Join(root, "state", "agents", "system-prompts", agentSystemPromptFileName)
	if got != want {
		t.Fatalf("hostAgentSystemPromptPath = %q, want %q", got, want)
	}
}

func TestHostAgentSystemPromptPathReturnsEmptyWhenWorkspaceMissing(t *testing.T) {
	if got := hostAgentSystemPromptPath(nil); got != "" {
		t.Fatalf("hostAgentSystemPromptPath(nil) = %q, want empty", got)
	}
	if got := hostAgentSystemPromptPath(&Session{}); got != "" {
		t.Fatalf("hostAgentSystemPromptPath(empty session) = %q, want empty", got)
	}
}

func TestWriteAgentSystemPromptFileReturnsErrorWhenWorkspaceMissing(t *testing.T) {
	err := writeAgentSystemPromptFile(&Session{}, "Reply only in Chinese")
	if err == nil {
		t.Fatal("writeAgentSystemPromptFile returned nil error, want error")
	}
	if !strings.Contains(err.Error(), "session workspace path is required") {
		t.Fatalf("writeAgentSystemPromptFile error = %v, want workspace required", err)
	}
	if err := writeAgentSystemPromptFile(&Session{}, ""); err != nil {
		t.Fatalf("writeAgentSystemPromptFile(empty prompt) returned error: %v", err)
	}
}

func TestBuildAgentExecSpecPassesStateRootForConventionPathDiscovery(t *testing.T) {
	cfg := &appconfig.Config{
		GuestStateRoot:     "/data/state",
		GuestWorkspacePath: "/workspace",
	}
	session := &Session{Summary: SessionSummary{ID: "session-1"}}

	spec := buildAgentExecSpec(cfg, session, "codex", "", "/data/state/agents/prompts/prompt.txt", "")
	command := strings.Join(spec.Args, " ")
	if !strings.Contains(command, "--state-root '/data/state'") {
		t.Fatalf("command missing --state-root for convention-based system prompt discovery: %q", command)
	}
}

type conventionSystemPromptRuntime struct {
	commands []string
}

func (r *conventionSystemPromptRuntime) EnsureSession(context.Context, *Session, VMState, ProxyState) (SessionVMInfo, error) {
	return SessionVMInfo{}, nil
}

func (r *conventionSystemPromptRuntime) StopSession(context.Context, *Session, VMState) (bool, error) {
	return false, nil
}

func (r *conventionSystemPromptRuntime) Exec(context.Context, *Session, VMState, ExecSpec) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("unexpected Exec call")
}

func (r *conventionSystemPromptRuntime) ExecStream(_ context.Context, _ *Session, _ VMState, spec ExecSpec, _ ExecStreamWriter) (ExecResult, error) {
	command := strings.Join(spec.Args, " ")
	r.commands = append(r.commands, command)
	payload := agentResultPrefix + `{"provider":"codex","sessionId":"agent-runtime-session","stopReason":"completed","finalText":"done","transcript":"done"}`
	return ExecResult{Stdout: payload + "\n", Success: true}, nil
}

type conventionSystemPromptRuntimeProvider struct {
	runtime BoxRuntime
}

func (p conventionSystemPromptRuntimeProvider) ForDriver(string) (BoxRuntime, error) {
	return p.runtime, nil
}

func (p conventionSystemPromptRuntimeProvider) ForSession(*Session) (BoxRuntime, error) {
	return p.runtime, nil
}

func TestExecuteAgentRunWritesConventionSystemPromptBeforeExec(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sessionID := "session-agent-system-prompt"
	cfg := &appconfig.Config{
		SessionRoot:        root,
		GuestStateRoot:     "/data/state",
		GuestWorkspacePath: "/workspace",
	}
	store := mustTestStore(t, cfg)
	if err := os.MkdirAll(filepath.Join(root, sessionID, "vm"), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := store.SaveVMState(sessionID, VMState{Driver: "docker"}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}

	configDB := newTestConfigStore(t)
	agent, err := configDB.CreateAgentDefinition(ctx, AgentDefinition{
		ID:           "agent-convention",
		Name:         "Convention",
		Provider:     "codex",
		SystemPrompt: "Reply only in Chinese",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}

	runtime := &conventionSystemPromptRuntime{}
	executor := &Executor{
		config:   cfg,
		store:    store,
		configDB: configDB,
		runtimes: conventionSystemPromptRuntimeProvider{runtime: runtime},
	}
	session := &Session{
		Summary: SessionSummary{
			ID:            sessionID,
			VMStatus:      VMStatusRunning,
			WorkspacePath: filepath.Join(root, sessionID, "workspace"),
		},
	}

	result, parsed, err := executor.executeAgentRun(ctx, session, "codex", agent.ID, "", "", "hello", "", nil)
	if err != nil {
		t.Fatalf("executeAgentRun returned error: %v", err)
	}
	if !result.Success || !parsed.Success {
		t.Fatalf("executeAgentRun success = (%v, %v), want both true", result.Success, parsed.Success)
	}
	if len(runtime.commands) != 1 {
		t.Fatalf("ExecStream calls = %d, want 1", len(runtime.commands))
	}
	hostPath := filepath.Join(root, sessionID, "state", "agents", "system-prompts", agentSystemPromptFileName)
	content, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", hostPath, err)
	}
	if string(content) != "Reply only in Chinese" {
		t.Fatalf("system prompt file content = %q", string(content))
	}
}

func TestLoaderRunHostAgentWritesSystemPromptFromBoundAgentDefinition(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "loader-box:latest",
		GuestWorkspacePath:   "/data/workspace",
		GuestStateRoot:       "/data/state",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
	}
	if err := os.MkdirAll(config.SessionRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(session root) returned error: %v", err)
	}

	configDB := newTestConfigStore(t)
	agent, err := configDB.CreateAgentDefinition(ctx, AgentDefinition{
		ID:           "loader-agent-prompt",
		Name:         "Loader Prompt Agent",
		Provider:     "codex",
		SystemPrompt: "Reply only from loader",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	loader, err := configDB.CreateLoader(ctx, Loader{
		Summary: LoaderSummary{
			Name:         "Loader With Agent Prompt",
			Runtime:      LoaderRuntimeScheduler,
			Enabled:      true,
			AgentID:      agent.ID,
			DefaultAgent: "codex",
		},
		Script: "function main() {}",
	})
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}

	store := mustTestStore(t, config)
	runtime := &fakeLoaderAgentRuntime{}
	driver := &fakeSessionDriver{}
	runtimes := fixedRuntimeProvider{runtime: runtime}
	manager := newTestLoaderManager(t, loaderspkg.ManagerDeps{
		Config:   config,
		RootCtx:  ctx,
		Store:    store,
		ConfigDB: configDB,
		Driver:   driver,
		Executor: loaderspkg.NewExecutor(config, store, configDB, runtimes, nil),
		Engine:   &QJSLoaderEngine{},
	})
	host := newLoaderRunHost(manager, loader, &LoaderRunSummary{ID: "run-loader-system-prompt", LoaderID: loader.Summary.ID}, loaderTriggerEventMetadata{})

	result, err := host.Agent(ctx, "summarize loader state", LoaderAgentRequest{})
	if err != nil {
		t.Fatalf("Agent returned error: %v", err)
	}
	if !result.Success || result.SessionID == "" {
		t.Fatalf("loader agent result = %#v", result)
	}

	hostPath := filepath.Join(config.SessionRoot, result.SessionID, "state", "agents", "system-prompts", agentSystemPromptFileName)
	content, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", hostPath, err)
	}
	if string(content) != "Reply only from loader" {
		t.Fatalf("system prompt file content = %q", string(content))
	}
}

func TestRunServiceProjectRunWritesSystemPromptFromManagedAgent(t *testing.T) {
	spec := newProjectServiceTestSpec("demo", "gpt-test")
	spec.Agents[0].SystemPrompt = "Reply only in project runs"
	store, service, projectID := setupRunPreparationProject(t, spec, t.TempDir())
	client, closeServer := newRunServiceTestClient(t, service)
	defer closeServer()
	ctx := context.Background()

	events, err := collectRunAgentStreamEvents(ctx, client, &agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "review with system prompt",
		Source:          agentcomposev2.RunSource_RUN_SOURCE_API,
		ClientRequestId: "system-prompt-run-request",
	})
	if err != nil {
		t.Fatalf("RunAgentStream returned error: %v", err)
	}
	completed := lastRunAgentStreamEvent(events, agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED)
	if completed == nil {
		t.Fatalf("RunAgentStream events missing completion: %#v", events)
	}
	stored, err := store.GetProjectRun(ctx, completed.GetRunId())
	if err != nil {
		t.Fatalf("GetProjectRun returned error: %v", err)
	}
	if stored.SessionID == "" {
		t.Fatalf("stored run missing session id: %#v", stored)
	}

	hostPath := filepath.Join(service.config.SessionRoot, stored.SessionID, "state", "agents", "system-prompts", agentSystemPromptFileName)
	content, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", hostPath, err)
	}
	if string(content) != "Reply only in project runs" {
		t.Fatalf("system prompt file content = %q", string(content))
	}
}
