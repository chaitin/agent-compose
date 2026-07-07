package runs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/capability"
	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/images"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/sessionstore"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestRunsCoordinatorAndHelperWorkflows(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 3, 8, 0, 0, 0, time.UTC)
	store := &fakeRunStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 3},
		projectAgent: domain.ProjectAgentRecord{
			ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Driver: "boxlite", Image: "agent-image:latest",
		},
		agent: ManagedAgentDefinition{
			ID: "agent-1", Enabled: true, Driver: "docker", GuestImage: "guest:latest", ManagedProjectID: "project-1", ManagedAgentName: "worker",
		},
		runs: map[string]domain.ProjectRunRecord{},
	}
	coord := NewCoordinator(store, func(projectID, agentName, source, idempotencyKey string) (string, error) {
		return strings.Join([]string{projectID, agentName, source, idempotencyKey}, ":"), nil
	})
	coord.SetNow(func() time.Time { return now })
	run, err := coord.BeginRun(ctx, StartRequest{ProjectID: "project-1", AgentName: "worker", Source: domain.ProjectRunSourceAPI, ClientRequestID: "request-1", SchedulerID: "scheduler", TriggerID: "trigger", Prompt: "do work"})
	if err != nil {
		t.Fatalf("BeginRun returned error: %v", err)
	}
	if run.Status != domain.ProjectRunStatusPending || run.Driver != "docker" || run.ImageRef != "guest:latest" {
		t.Fatalf("run = %#v", run)
	}
	if existing, err := coord.BeginRun(ctx, StartRequest{ProjectID: "project-1", AgentName: "worker", Source: domain.ProjectRunSourceAPI, ClientRequestID: "request-1"}); err != nil || existing.RunID != run.RunID {
		t.Fatalf("idempotent BeginRun existing=%#v err=%v", existing, err)
	}
	if override, err := coord.BeginRun(ctx, StartRequest{ProjectID: "project-1", AgentName: "worker", Source: domain.ProjectRunSourceAPI, ClientRequestID: "request-driver", Driver: "msb"}); err != nil || override.Driver != driverpkg.RuntimeDriverMicrosandbox {
		t.Fatalf("driver override run=%#v err=%v", override, err)
	}
	store.agent.Driver = ""
	if fallback, err := coord.BeginRun(ctx, StartRequest{ProjectID: "project-1", AgentName: "worker", Source: domain.ProjectRunSourceAPI, ClientRequestID: "request-project-driver"}); err != nil || fallback.Driver != driverpkg.RuntimeDriverBoxlite {
		t.Fatalf("project driver fallback run=%#v err=%v", fallback, err)
	}
	beforeInvalid := len(store.runs)
	if _, err := coord.BeginRun(ctx, StartRequest{ProjectID: "project-1", AgentName: "worker", Source: domain.ProjectRunSourceAPI, ClientRequestID: "request-bad-driver", Driver: "bad"}); err == nil {
		t.Fatalf("expected invalid driver error")
	}
	if len(store.runs) != beforeInvalid {
		t.Fatalf("invalid driver created run: before=%d after=%d", beforeInvalid, len(store.runs))
	}
	if running, err := coord.MarkRunning(ctx, run.RunID, "session-1"); err != nil || running.Status != domain.ProjectRunStatusRunning || running.SessionID != "session-1" {
		t.Fatalf("MarkRunning run=%#v err=%v", running, err)
	}
	if succeeded, err := coord.MarkSucceeded(ctx, TransitionRequest{RunID: run.RunID, ExitCode: 0, Output: "ok", ResultJSON: `{"ok":true}`, LogsPath: "/logs", ArtifactsDir: "/artifacts"}); err != nil || succeeded.Status != domain.ProjectRunStatusSucceeded || succeeded.DurationMs < 0 {
		t.Fatalf("MarkSucceeded run=%#v err=%v", succeeded, err)
	}
	if _, err := coord.MarkFailed(ctx, TransitionRequest{RunID: run.RunID, Error: "late"}); err == nil {
		t.Fatalf("expected terminal transition error")
	}
	if _, err := (*Coordinator)(nil).BeginRun(ctx, StartRequest{}); err == nil {
		t.Fatalf("expected nil coordinator error")
	}
	if _, err := NewCoordinator(store, nil).BeginRun(ctx, StartRequest{ProjectID: "project-1", AgentName: "worker"}); err == nil {
		t.Fatalf("expected missing stable id function error")
	}
	store.agent.Enabled = false
	if _, err := coord.BeginRun(ctx, StartRequest{ProjectID: "project-1", AgentName: "worker", ClientRequestID: "request-2"}); err == nil {
		t.Fatalf("expected disabled agent error")
	}

	for _, status := range []string{domain.ProjectRunStatusPending, domain.ProjectRunStatusRunning, domain.ProjectRunStatusSucceeded, domain.ProjectRunStatusFailed, domain.ProjectRunStatusCanceled, "bad"} {
		_ = NormalizeStatus(status)
		_ = StatusIsTerminal(status)
	}
	for _, source := range []string{domain.ProjectRunSourceScheduler, domain.ProjectRunSourceAPI, domain.ProjectRunSourceManual, "bad"} {
		_ = NormalizeSource(source)
	}

	title := SessionTitle(run)
	tags := MergeSessionTags([]domain.SessionTag{{Name: "project", Value: "project-1"}}, SessionTags(run))
	if title == "" || len(tags) < 4 || WorkspaceID(run, "git") == "" || WorkspaceName(run, "git") == "" {
		t.Fatalf("session helpers title=%q tags=%#v", title, tags)
	}
	cell := domain.NotebookCell{ID: "cell-1", Type: execution.CellTypeAgent, Agent: "codex", AgentSessionID: "agent-session", Output: "output", Success: false, ExitCode: 0, Stderr: "stderr"}
	transition := TransitionFromAgentCell(run, &domain.Session{Summary: domain.SessionSummary{ID: "session-1", WorkspacePath: t.TempDir()}}, cell, nil)
	if transition.ExitCode == 0 || !strings.Contains(transition.Error, "stderr") || transition.ArtifactsDir == "" {
		t.Fatalf("transition from failed cell = %#v", transition)
	}
	transition = TransitionFromAgentCell(run, nil, cell, errors.New("boom"))
	if transition.ExitCode == 0 || !strings.Contains(transition.Error, "boom") {
		t.Fatalf("transition from exec error = %#v", transition)
	}
	if !CleanupPolicyStopsSession(agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_STOP_ON_COMPLETION) ||
		!CleanupPolicyStopsSession(agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_REMOVE_ON_COMPLETION) ||
		CleanupPolicyStopsSession(agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING) ||
		!CleanupPolicyRemovesSession(agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_REMOVE_ON_COMPLETION) ||
		CleanupPolicyRemovesSession(agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_STOP_ON_COMPLETION) {
		t.Fatalf("cleanup policy mapping failed")
	}
}

func TestIntegrationRunsCoordinatorAndHelperWorkflows(t *testing.T) {
	TestRunsCoordinatorAndHelperWorkflows(t)
}

func TestE2ERunsCoordinatorAndHelperWorkflows(t *testing.T) {
	TestRunsCoordinatorAndHelperWorkflows(t)
}

func TestRunsPreparationWorkspaceAndStatusWorkflows(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := `{"variables":[{"name":"PROJECT_VAR","value":"project"}],"workspace":{"provider":"git","url":"https://example.test/repo.git","branch":"main","path":"."},"agents":[{"name":"worker","workspace":{"provider":"local","path":"."}}]}`
	store := &fakePreparationStore{
		project:  domain.ProjectRecord{ID: "project-1", Name: "Project", SourcePath: sourceDir},
		revision: domain.ProjectRevisionRecord{ProjectID: "project-1", Revision: 1, SpecJSON: spec},
		agent:    domain.AgentDefinition{ID: "agent-1", Name: "Agent", EnvItems: []domain.SessionEnvVar{{Name: "AGENT_VAR", Value: "agent"}}, CapsetIDs: []string{"dev"}},
		global:   []domain.SessionEnvVar{{Name: "GLOBAL_VAR", Value: "global"}},
	}
	controller := &Controller{config: &appconfig.Config{DataRoot: root}}
	run := domain.ProjectRunRecord{RunID: "run-1", ProjectID: "project-1", ProjectRevision: 1, ProjectName: "Project", AgentName: "worker", ManagedAgentID: "agent-1"}
	prepared, err := PrepareProjectRun(ctx, store, projectRunWorkspaceResolver{controller: controller}, run, []*agentcomposev2.EnvVarSpec{{Name: "REQUEST_VAR", Value: "request"}})
	if err != nil {
		t.Fatalf("PrepareProjectRun returned error: %v", err)
	}
	if len(prepared.EnvItems) < 3 || prepared.Workspace == nil || prepared.WorkspaceConfig == nil || len(prepared.CapsetIDs) != 1 {
		t.Fatalf("prepared = %#v", prepared)
	}
	decoded, err := DecodeRevisionSpec(spec)
	if err != nil || decoded.GetAgents()[0].GetName() != "worker" {
		t.Fatalf("DecodeRevisionSpec decoded=%#v err=%v", decoded, err)
	}
	if agent, ok := AgentSpecByName(decoded, "worker"); !ok || agent.GetName() != "worker" {
		t.Fatalf("AgentSpecByName failed")
	}
	if _, ok := AgentSpecByName(nil, "worker"); ok {
		t.Fatalf("nil AgentSpecByName should not match")
	}
	if env := EnvItemsFromV2([]*agentcomposev2.EnvVarSpec{nil, {Name: "A", Value: "B"}}); len(env) != 1 {
		t.Fatalf("EnvItemsFromV2 env=%#v", env)
	}
	if ComposeWorkspaceSpecFromV2(nil) != nil || ComposeWorkspaceSpecFromV2(&agentcomposev2.WorkspaceSpec{Provider: "git", Url: "url"}).Provider != "git" {
		t.Fatalf("ComposeWorkspaceSpecFromV2 failed")
	}
	if merged := MergeEnvItems([]domain.SessionEnvVar{{Name: "A", Value: "1"}}, []domain.SessionEnvVar{{Name: "A", Value: "2"}}); len(merged) != 1 || merged[0].Value != "2" {
		t.Fatalf("MergeEnvItems merged=%#v", merged)
	}
	if clean, err := CleanLocalWorkspacePath("."); err != nil || clean != "." {
		t.Fatalf("CleanLocalWorkspacePath clean=%q err=%v", clean, err)
	}
	for _, raw := range []string{"", "/abs", "../escape"} {
		if _, err := CleanLocalWorkspacePath(raw); err == nil {
			t.Fatalf("expected CleanLocalWorkspacePath error for %q", raw)
		}
	}
	if path, err := ResolveLocalProjectWorkspacePath(store.project, "."); err != nil || path == "" {
		t.Fatalf("ResolveLocalProjectWorkspacePath path=%q err=%v", path, err)
	}
	if _, err := projectRunGitWorkspaceConfig(run, &compose.WorkspaceSpec{Provider: "git", URL: "https://example.test/repo.git", Branch: "main", Path: "."}); err != nil {
		t.Fatalf("projectRunGitWorkspaceConfig returned error: %v", err)
	}
	if _, err := projectRunGitWorkspaceConfig(run, &compose.WorkspaceSpec{Provider: "git"}); err == nil {
		t.Fatalf("expected git workspace url error")
	}
	if workspace, err := controller.prepareProjectRunWorkspace(ctx, run, store.project, nil, nil); err != nil || workspace != nil {
		t.Fatalf("nil workspace = %#v/%v", workspace, err)
	}
	if _, err := controller.prepareProjectRunWorkspace(ctx, run, store.project, &compose.WorkspaceSpec{}, nil); err == nil || !strings.Contains(err.Error(), "provider is required") {
		t.Fatalf("missing provider err=%v", err)
	}
	if _, err := controller.prepareProjectRunWorkspace(ctx, run, store.project, &compose.WorkspaceSpec{Provider: "s3"}, nil); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported provider err=%v", err)
	}
	localWorkspace, err := controller.prepareProjectRunWorkspace(ctx, run, store.project, &compose.WorkspaceSpec{Provider: "git", URL: "https://example.test/project.git", Path: "."}, &compose.WorkspaceSpec{Provider: "local", Path: "."})
	if err != nil || localWorkspace == nil || localWorkspace.Type != "file" {
		t.Fatalf("agent local workspace = %#v/%v", localWorkspace, err)
	}
	if _, err := (&Controller{}).materializeLocalProjectRunWorkspace(run, store.project, &compose.WorkspaceSpec{Provider: "local", Path: "."}); err == nil {
		t.Fatalf("materialize without config returned nil error")
	}
	if _, err := controller.materializeLocalProjectRunWorkspace(run, store.project, &compose.WorkspaceSpec{Provider: "local", Path: "missing"}); err == nil {
		t.Fatalf("materialize missing local path returned nil error")
	}
	if snapshot := toSessionWorkspaceSnapshot(domain.WorkspaceConfig{ID: "workspace", Name: "Workspace", Type: "file", ConfigJSON: "{}"}); snapshot.ID != "workspace" {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	statusStore := fakeProjectSessionRunStore{runs: []domain.ProjectRunRecord{{RunID: "run-1", SessionID: "session-1"}, {RunID: "run-2", SessionID: "session-1"}, {RunID: "run-3", SessionID: "missing"}}}
	statuses, err := ListProjectSessionStatuses(ctx, statusStore, fakeSessionStatusStore{sessions: map[string]*domain.Session{"session-1": {Summary: domain.SessionSummary{ID: "session-1"}}}}, domain.ProjectSessionRelationFilter{})
	if err != nil || len(statuses) != 2 || statuses[1].SessionMissing != true {
		t.Fatalf("ListProjectSessionStatuses statuses=%#v err=%v", statuses, err)
	}
	if _, err := ListProjectSessionStatuses(ctx, nil, fakeSessionStatusStore{}, domain.ProjectSessionRelationFilter{}); err == nil {
		t.Fatalf("expected nil run store error")
	}
	if _, err := ListProjectSessionStatuses(ctx, statusStore, nil, domain.ProjectSessionRelationFilter{}); err == nil {
		t.Fatalf("expected nil session store error")
	}
}

func TestIntegrationRunsPreparationWorkspaceAndStatusWorkflows(t *testing.T) {
	TestRunsPreparationWorkspaceAndStatusWorkflows(t)
}

func TestE2ERunsPreparationWorkspaceAndStatusWorkflows(t *testing.T) {
	TestRunsPreparationWorkspaceAndStatusWorkflows(t)
}

func TestRunsControllerRunProjectAgentSuccessWorkflow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:      root,
		SessionRoot:   filepath.Join(root, "sessions"),
		RuntimeDriver: "boxlite",
		DefaultImage:  "guest:latest",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	configDB := &fakeControllerStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 1},
		projectAgent: domain.ProjectAgentRecord{
			ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Driver: "boxlite", Image: "guest:latest",
		},
		managed: ManagedAgentDefinition{
			ID: "agent-1", Enabled: true, Driver: "boxlite", GuestImage: "guest:latest", ManagedProjectID: "project-1", ManagedAgentName: "worker",
		},
		revision: domain.ProjectRevisionRecord{
			ProjectID: "project-1",
			Revision:  1,
			SpecJSON:  `{"agents":[{"name":"worker"}],"variables":[{"name":"PROJECT_VAR","value":"project"}]}`,
		},
		agent:  domain.AgentDefinition{ID: "agent-1", Provider: "codex", Model: "gpt", EnvItems: []domain.SessionEnvVar{{Name: "AGENT_VAR", Value: "agent"}}},
		global: []domain.SessionEnvVar{{Name: "GLOBAL_VAR", Value: "global"}},
		runs:   map[string]domain.ProjectRunRecord{},
	}
	driver := &fakeControllerDriver{store: store}
	executor := &fakeControllerExecutor{}
	bus := &fakeControllerPublisher{}
	dashboard := &fakeControllerDashboard{}
	controller := NewController(ControllerDependencies{
		Config:   config,
		Store:    store,
		ConfigDB: configDB,
		Driver:   driver,
		Executor: executor,
		Runtime: func(*domain.Session) (Runtime, error) {
			return &fakeControllerRuntime{}, nil
		},
		Images:    fakeControllerImages{},
		Bus:       bus,
		Dashboard: dashboard,
	})
	var started bool
	var startedLogsPath string
	var chunks []domain.ExecChunk
	run, execErr, err := controller.RunProjectAgent(ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Prompt:          "do work",
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: "request-1",
		CleanupPolicy:   agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_STOP_ON_COMPLETION,
	}, &StreamSink{
		SendStarted: func(run domain.ProjectRunRecord, _ time.Time) error {
			started = true
			startedLogsPath = configDB.runs[run.RunID].LogsPath
			return nil
		},
		SendChunk: func(_ string, chunk domain.ExecChunk, _ time.Time) error {
			chunks = append(chunks, chunk)
			return nil
		},
	})
	if err != nil || execErr != nil {
		t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
	}
	if run.Status != domain.ProjectRunStatusSucceeded || run.SessionID == "" || run.Output != "done" {
		t.Fatalf("run = %#v", run)
	}
	if !started || len(chunks) != 1 || !driver.started || !driver.stopped || executor.request.Message != "do work" {
		t.Fatalf("started=%v chunks=%#v driver=%#v request=%#v", started, chunks, driver, executor.request)
	}
	if startedLogsPath == "" || filepath.Base(startedLogsPath) != "output.txt" {
		t.Fatalf("started logs path = %q, want cell output path", startedLogsPath)
	}
	if data, err := os.ReadFile(run.LogsPath); err != nil || string(data) != "chunk" {
		t.Fatalf("agent run logs_path content = %q err=%v", string(data), err)
	}
	proxyState, err := store.GetProxyState(run.SessionID)
	if err != nil {
		t.Fatalf("GetProxyState returned error: %v", err)
	}
	if proxyState.Enabled {
		t.Fatalf("proxy state = %+v, want default jupyter disabled", proxyState)
	}
	if len(bus.events) == 0 || len(dashboard.reasons) == 0 {
		t.Fatalf("bus=%#v dashboard=%#v", bus.events, dashboard.reasons)
	}
}

func TestRunsControllerRunProjectAgentResolvesJupyterConfig(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        "boxlite",
		DefaultImage:         "guest:latest",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/jupyter",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	configDB := &fakeControllerStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 1},
		projectAgent: domain.ProjectAgentRecord{
			ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Driver: "boxlite", Image: "guest:latest",
		},
		managed: ManagedAgentDefinition{
			ID: "agent-1", Enabled: true, Driver: "boxlite", GuestImage: "guest:latest", ManagedProjectID: "project-1", ManagedAgentName: "worker",
		},
		revision: domain.ProjectRevisionRecord{
			ProjectID: "project-1",
			Revision:  1,
			SpecJSON:  `{"agents":[{"name":"worker","jupyter":{"enabled":true,"guest_port":9999}}]}`,
		},
		agent: domain.AgentDefinition{ID: "agent-1", Provider: "codex", Model: "gpt"},
		runs:  map[string]domain.ProjectRunRecord{},
	}
	controller := NewController(ControllerDependencies{
		Config:   config,
		Store:    store,
		ConfigDB: configDB,
		Driver:   &fakeControllerDriver{store: store},
		Executor: &fakeControllerExecutor{},
		Images:   fakeControllerImages{},
	})
	run, execErr, err := controller.RunProjectAgent(ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Prompt:          "do work",
		Source:          domain.ProjectRunSourceScheduler,
		ClientRequestID: "request-jupyter",
		Jupyter:         &agentcomposev2.RunJupyterSpec{Expose: true},
	}, nil)
	if err != nil || execErr != nil {
		t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
	}
	proxyState, err := store.GetProxyState(run.SessionID)
	if err != nil {
		t.Fatalf("GetProxyState returned error: %v", err)
	}
	if !proxyState.Enabled || !proxyState.Exposed || proxyState.GuestPort != 9999 || proxyState.HostPort == 0 || proxyState.Token == "" {
		t.Fatalf("proxy state = %+v, want YAML guest port with CLI expose", proxyState)
	}
}

func TestIntegrationRunsControllerRunProjectAgentSuccessWorkflow(t *testing.T) {
	TestRunsControllerRunProjectAgentSuccessWorkflow(t)
}

func TestE2ERunsControllerRunProjectAgentSuccessWorkflow(t *testing.T) {
	TestRunsControllerRunProjectAgentSuccessWorkflow(t)
}

func TestRunsControllerRunProjectAgentCommandWorkflow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:      root,
		SessionRoot:   filepath.Join(root, "sessions"),
		RuntimeDriver: "boxlite",
		DefaultImage:  "guest:latest",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	configDB := &fakeControllerStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 1},
		projectAgent: domain.ProjectAgentRecord{
			ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Driver: "boxlite", Image: "guest:latest",
		},
		managed: ManagedAgentDefinition{
			ID: "agent-1", Enabled: true, Driver: "boxlite", GuestImage: "guest:latest", ManagedProjectID: "project-1", ManagedAgentName: "worker",
		},
		revision: domain.ProjectRevisionRecord{ProjectID: "project-1", Revision: 1, SpecJSON: `{"agents":[{"name":"worker"}]}`},
		agent:    domain.AgentDefinition{ID: "agent-1", Provider: "codex"},
		runs:     map[string]domain.ProjectRunRecord{},
	}
	runtime := &fakeControllerRuntime{}
	controller := NewController(ControllerDependencies{
		Config:   config,
		Store:    store,
		ConfigDB: configDB,
		Driver:   &fakeControllerDriver{store: store},
		Runtime: func(*domain.Session) (Runtime, error) {
			return runtime, nil
		},
		Images: fakeControllerImages{},
	})
	var started bool
	var chunks []domain.ExecChunk
	run, execErr, err := controller.RunProjectAgent(ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Command:         "echo command",
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: "command-request",
	}, &StreamSink{
		SendStarted: func(domain.ProjectRunRecord, time.Time) error {
			started = true
			return nil
		},
		SendChunk: func(_ string, chunk domain.ExecChunk, _ time.Time) error {
			chunks = append(chunks, chunk)
			return nil
		},
	})
	if err != nil || execErr != nil {
		t.Fatalf("RunProjectAgent command err=%v execErr=%v run=%#v", err, execErr, run)
	}
	if run.Status != domain.ProjectRunStatusSucceeded || run.Output != "command output\n" || run.ArtifactsDir == "" || run.LogsPath == "" {
		t.Fatalf("command run = %#v", run)
	}
	transcriptData, err := os.ReadFile(filepath.Join(run.ArtifactsDir, "transcript.txt"))
	if err != nil || string(transcriptData) != "command output\n" || strings.Contains(string(transcriptData), execution.CommandResultPrefix) {
		t.Fatalf("command transcript artifact = %q err=%v", string(transcriptData), err)
	}
	requestData, err := os.ReadFile(filepath.Join(run.ArtifactsDir, "command-request.json"))
	if err != nil || !strings.Contains(string(requestData), `"mode": "shell"`) || !strings.Contains(string(requestData), `"script": "echo command"`) {
		t.Fatalf("command request artifact = %q err=%v", string(requestData), err)
	}
	if data, err := os.ReadFile(filepath.Join(run.ArtifactsDir, "output.txt")); err != nil || string(data) != "command output\n" {
		t.Fatalf("command output artifact = %q err=%v", string(data), err)
	}
	if !started || len(chunks) != 1 || chunks[0].Text != "command output\n" || strings.Contains(chunks[0].Text, execution.CommandResultPrefix) || runtime.spec.Command != "sh" || !strings.Contains(strings.Join(runtime.spec.Args, " "), "agent-compose-runtime exec") {
		t.Fatalf("started=%v chunks=%#v spec=%#v", started, chunks, runtime.spec)
	}
	second, secondExecErr, secondErr := controller.RunProjectAgent(ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Command:         "pwd",
		SessionID:       run.SessionID,
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: "command-request-2",
		CleanupPolicy:   agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING,
	}, nil)
	if secondErr != nil || secondExecErr != nil {
		t.Fatalf("second command run err=%v execErr=%v run=%#v", secondErr, secondExecErr, second)
	}
	if second.RunID == run.RunID || second.ArtifactsDir == run.ArtifactsDir || second.LogsPath == run.LogsPath {
		t.Fatalf("command runs should have independent ids/artifacts/logs: first=%#v second=%#v", run, second)
	}
	if !strings.Contains(second.ArtifactsDir, second.RunID) || !strings.Contains(second.LogsPath, second.RunID) {
		t.Fatalf("second command paths do not include run id: artifacts=%q logs=%q run=%q", second.ArtifactsDir, second.LogsPath, second.RunID)
	}
	secondRequestData, err := os.ReadFile(filepath.Join(second.ArtifactsDir, "command-request.json"))
	if err != nil || !strings.Contains(string(secondRequestData), `"script": "pwd"`) {
		t.Fatalf("second command request artifact = %q err=%v", string(secondRequestData), err)
	}
	if _, err := os.Stat(filepath.Join(run.ArtifactsDir, "command-request.json")); err != nil {
		t.Fatalf("first command artifact should remain after second run: %v", err)
	}
	if _, _, err := controller.RunProjectAgent(ctx, RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Command: "echo command", Prompt: "prompt",
	}, nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("command and prompt error = %v, want ErrInvalidRequest", err)
	}
}

func TestRunsControllerRunProjectAgentCommandNonZeroExitPreservesOutput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:      root,
		SessionRoot:   filepath.Join(root, "sessions"),
		RuntimeDriver: "boxlite",
		DefaultImage:  "guest:latest",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	configDB := &fakeControllerStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 1},
		projectAgent: domain.ProjectAgentRecord{
			ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Driver: "boxlite", Image: "guest:latest",
		},
		managed: ManagedAgentDefinition{
			ID: "agent-1", Enabled: true, Driver: "boxlite", GuestImage: "guest:latest", ManagedProjectID: "project-1", ManagedAgentName: "worker",
		},
		revision: domain.ProjectRevisionRecord{ProjectID: "project-1", Revision: 1, SpecJSON: `{"agents":[{"name":"worker"}]}`},
		agent:    domain.AgentDefinition{ID: "agent-1", Provider: "codex"},
		runs:     map[string]domain.ProjectRunRecord{},
	}
	runtime := &fakeControllerRuntime{result: domain.RuntimeCommandResult{
		Stdout:   "partial stdout\n",
		Stderr:   "failure stderr\n",
		Output:   "partial stdout\nfailure stderr\n",
		ExitCode: 7,
		Success:  false,
	}}
	controller := NewController(ControllerDependencies{
		Config:   config,
		Store:    store,
		ConfigDB: configDB,
		Driver:   &fakeControllerDriver{store: store},
		Runtime: func(*domain.Session) (Runtime, error) {
			return runtime, nil
		},
		Images: fakeControllerImages{},
	})
	var chunks []domain.ExecChunk
	run, execErr, err := controller.RunProjectAgent(ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Command:         "exit 7",
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: "command-failure",
	}, &StreamSink{
		SendChunk: func(_ string, chunk domain.ExecChunk, _ time.Time) error {
			chunks = append(chunks, chunk)
			return nil
		},
	})
	if err != nil || execErr != nil {
		t.Fatalf("RunProjectAgent failure err=%v execErr=%v run=%#v", err, execErr, run)
	}
	if run.Status != domain.ProjectRunStatusFailed || run.ExitCode != 7 || run.Output != "partial stdout\nfailure stderr\n" {
		t.Fatalf("failed command run = %#v", run)
	}
	if len(chunks) != 2 || domain.NormalizeStdioStream(chunks[0].Stream) != domain.StdioStdout || domain.NormalizeStdioStream(chunks[1].Stream) != domain.StdioStderr {
		t.Fatalf("stream chunks = %#v", chunks)
	}
	joinedChunks := chunks[0].Text + chunks[1].Text
	if !strings.Contains(joinedChunks, "partial stdout\n") || !strings.Contains(joinedChunks, "failure stderr\n") || strings.Contains(joinedChunks, execution.CommandResultPrefix) {
		t.Fatalf("stream chunks leaked or lost output: %#v", chunks)
	}
	transcriptData, err := os.ReadFile(filepath.Join(run.ArtifactsDir, "transcript.txt"))
	if err != nil || !strings.Contains(string(transcriptData), "partial stdout\n") || !strings.Contains(string(transcriptData), "failure stderr\n") || strings.Contains(string(transcriptData), execution.CommandResultPrefix) {
		t.Fatalf("failed command transcript = %q err=%v", string(transcriptData), err)
	}
}

func TestRunsControllerExecuteProjectRunCommandEdgeBranches(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:       root,
		SessionRoot:    filepath.Join(root, "sessions"),
		RuntimeDriver:  "boxlite",
		DefaultImage:   "guest:latest",
		GuestStateRoot: "/guest/state",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSession(ctx, "command session", "", "boxlite", "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	run := domain.ProjectRunRecord{RunID: "run-edge", ProjectID: "project-1", AgentName: "worker"}
	req := RunAgentRequest{Env: []*agentcomposev2.EnvVarSpec{{Name: "REQUEST_ENV", Value: "yes"}}}

	transition, err := (&Controller{config: config}).executeProjectRunCommand(ctx, run, session, req, "echo edge", nil)
	if err == nil || transition.ExitCode != 1 || !strings.Contains(transition.Error, "dependencies are required") {
		t.Fatalf("nil deps transition=%#v err=%v", transition, err)
	}

	controller := &Controller{config: config, store: store, runtime: func(*domain.Session) (Runtime, error) {
		return &fakeControllerRuntime{}, nil
	}}
	transition, err = controller.executeProjectRunCommand(ctx, run, session, req, "echo edge", &StreamSink{
		SendStarted: func(domain.ProjectRunRecord, time.Time) error {
			return errors.New("start send failed")
		},
	})
	if err == nil || transition.ExitCode != 1 || !strings.Contains(transition.Error, "start send failed") {
		t.Fatalf("send started transition=%#v err=%v", transition, err)
	}

	missingVMSession := *session
	missingVMSession.Summary.ID = "missing-vm"
	missingVMSession.Summary.WorkspacePath = filepath.Join(root, "missing-vm", "workspace")
	transition, err = controller.executeProjectRunCommand(ctx, run, &missingVMSession, req, "echo edge", nil)
	if err == nil || transition.ExitCode != 1 || !strings.Contains(transition.Error, "no such file") {
		t.Fatalf("missing vm transition=%#v err=%v", transition, err)
	}
	if err := store.SaveVMState(session.Summary.ID, domain.VMState{Driver: "boxlite", BoxID: "box-1"}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}

	controller.runtime = func(*domain.Session) (Runtime, error) {
		return nil, errors.New("runtime unavailable")
	}
	transition, err = controller.executeProjectRunCommand(ctx, run, session, req, "echo edge", nil)
	if err == nil || transition.ExitCode != 1 || !strings.Contains(transition.Error, "runtime unavailable") {
		t.Fatalf("runtime provider transition=%#v err=%v", transition, err)
	}

	run.RunID = "run-mkdir"
	blockingPath := projectRunCommandArtifactsDir(run, session)
	if err := os.MkdirAll(filepath.Dir(blockingPath), 0o755); err != nil {
		t.Fatalf("mkdir blocking parent: %v", err)
	}
	if err := os.WriteFile(blockingPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	controller.runtime = func(*domain.Session) (Runtime, error) {
		return &fakeControllerRuntime{}, nil
	}
	transition, err = controller.executeProjectRunCommand(ctx, run, session, req, "echo edge", nil)
	if err == nil || transition.ExitCode != 1 || !strings.Contains(transition.Error, "not a directory") {
		t.Fatalf("mkdir transition=%#v err=%v", transition, err)
	}

	run.RunID = "run-send"
	transition, err = controller.executeProjectRunCommand(ctx, run, session, req, "echo edge", &StreamSink{
		SendChunk: func(string, domain.ExecChunk, time.Time) error {
			return errors.New("chunk send failed")
		},
	})
	if err == nil || transition.ExitCode != 1 || !strings.Contains(transition.Error, "chunk send failed") {
		t.Fatalf("send chunk transition=%#v err=%v", transition, err)
	}

	run.RunID = "run-exec-err"
	controller.runtime = func(*domain.Session) (Runtime, error) {
		return &fakeControllerRuntime{execErr: errors.New("exec failed")}, nil
	}
	transition, err = controller.executeProjectRunCommand(ctx, run, session, req, "echo edge", nil)
	if err == nil || transition.ExitCode == 0 || !strings.Contains(transition.Error, "exec failed") {
		t.Fatalf("exec error transition=%#v err=%v", transition, err)
	}

	run.RunID = "run-parse"
	rawResult := domain.ExecResult{Stdout: "plain output", Output: "plain output", ExitCode: 0, Success: true}
	controller.runtime = func(*domain.Session) (Runtime, error) {
		return &fakeControllerRuntime{rawResult: &rawResult}, nil
	}
	transition, err = controller.executeProjectRunCommand(ctx, run, session, req, "echo edge", nil)
	if err == nil || transition.ExitCode != 1 || !strings.Contains(transition.Error, "no result payload") {
		t.Fatalf("parse transition=%#v err=%v", transition, err)
	}
}

func TestIntegrationRunsControllerRunProjectAgentCommandWorkflow(t *testing.T) {
	TestRunsControllerRunProjectAgentCommandWorkflow(t)
}

func TestE2ERunsControllerRunProjectAgentCommandWorkflow(t *testing.T) {
	TestRunsControllerRunProjectAgentCommandWorkflow(t)
}

type controllerRunFixture struct {
	ctx        context.Context
	config     *appconfig.Config
	store      *sessionstore.Store
	configDB   *fakeControllerStore
	driver     *fakeControllerDriver
	executor   *fakeControllerExecutor
	dashboard  *fakeControllerDashboard
	controller *Controller
}

func newControllerRunFixture(t *testing.T) *controllerRunFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:      root,
		SessionRoot:   filepath.Join(root, "sessions"),
		RuntimeDriver: "boxlite",
		DefaultImage:  "guest:latest",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	configDB := &fakeControllerStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 1},
		projectAgent: domain.ProjectAgentRecord{
			ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Driver: "boxlite", Image: "guest:latest",
		},
		managed: ManagedAgentDefinition{
			ID: "agent-1", Enabled: true, Driver: "boxlite", GuestImage: "guest:latest", ManagedProjectID: "project-1", ManagedAgentName: "worker",
		},
		revision: domain.ProjectRevisionRecord{ProjectID: "project-1", Revision: 1, SpecJSON: `{"agents":[{"name":"worker"}]}`},
		agent:    domain.AgentDefinition{ID: "agent-1", Provider: "codex"},
		runs:     map[string]domain.ProjectRunRecord{},
	}
	driver := &fakeControllerDriver{store: store}
	executor := &fakeControllerExecutor{}
	dashboard := &fakeControllerDashboard{}
	controller := NewController(ControllerDependencies{
		Config:       config,
		Store:        store,
		ConfigDB:     configDB,
		Driver:       driver,
		Executor:     executor,
		Images:       fakeControllerImages{},
		LoaderEngine: &loaders.QJSLoaderEngine{},
		Dashboard:    dashboard,
	})
	return &controllerRunFixture{
		ctx:        ctx,
		config:     config,
		store:      store,
		configDB:   configDB,
		driver:     driver,
		executor:   executor,
		dashboard:  dashboard,
		controller: controller,
	}
}

func runAgentWithRemoveOnCompletion(t *testing.T, fixture *controllerRunFixture, extra func(*RunAgentRequest)) (domain.ProjectRunRecord, error, error) {
	t.Helper()
	req := RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Prompt:          "do work",
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: uuidForTest(t.Name()),
		CleanupPolicy:   agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_REMOVE_ON_COMPLETION,
	}
	if extra != nil {
		extra(&req)
	}
	return fixture.controller.RunProjectAgent(fixture.ctx, req, nil)
}

func uuidForTest(name string) string {
	return strings.NewReplacer("/", "-", " ", "-").Replace(name)
}

func TestRunsControllerRunProjectAgentRemoveOnCompletionCleanup(t *testing.T) {
	t.Run("success removes created session", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, nil)
		if err != nil || execErr != nil {
			t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
		}
		if run.Status != domain.ProjectRunStatusSucceeded || run.SessionID == "" || run.CleanupError != "" {
			t.Fatalf("run = %#v", run)
		}
		if _, statErr := os.Stat(fixture.store.SessionDir(run.SessionID)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("created session dir still exists or stat error mismatch: %v", statErr)
		}
		if !fixture.driver.stopped || !containsString(fixture.dashboard.reasons, "session_removed") {
			t.Fatalf("driver=%#v dashboard=%#v", fixture.driver, fixture.dashboard.reasons)
		}
	})

	t.Run("agent failure removes created session and preserves original error", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		fixture.executor.execErr = errors.New("agent failed")
		fixture.executor.cell = domain.NotebookCell{ID: "cell-1", Type: execution.CellTypeAgent, Output: "failed", Success: false, ExitCode: 7, Stderr: "agent failed"}
		run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, nil)
		if err != nil || execErr == nil || !strings.Contains(execErr.Error(), "agent failed") {
			t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
		}
		if run.Status != domain.ProjectRunStatusFailed || run.CleanupError != "" {
			t.Fatalf("run = %#v", run)
		}
		if _, statErr := os.Stat(fixture.store.SessionDir(run.SessionID)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("created session dir still exists or stat error mismatch: %v", statErr)
		}
	})

	t.Run("context cancel marks canceled and removes created session", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		fixture.executor.execErr = context.Canceled
		fixture.executor.cell = domain.NotebookCell{ID: "cell-1", Type: execution.CellTypeAgent, Output: "canceled", Success: false, ExitCode: 1, Stderr: "canceled"}
		run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, nil)
		if err != nil || !errors.Is(execErr, context.Canceled) {
			t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
		}
		if run.Status != domain.ProjectRunStatusCanceled || run.CleanupError != "" {
			t.Fatalf("run = %#v", run)
		}
		if _, statErr := os.Stat(fixture.store.SessionDir(run.SessionID)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("created session dir still exists or stat error mismatch: %v", statErr)
		}
	})

	t.Run("existing session is stopped but not removed", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		session, err := fixture.store.CreateSession(fixture.ctx, "existing", "", "boxlite", "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
		if err != nil {
			t.Fatalf("CreateSession returned error: %v", err)
		}
		run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, func(req *RunAgentRequest) {
			req.SessionID = session.Summary.ID
		})
		if err != nil || execErr != nil {
			t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
		}
		loaded, err := fixture.store.GetSession(fixture.ctx, session.Summary.ID)
		if err != nil {
			t.Fatalf("existing session was removed: %v", err)
		}
		if run.SessionID != session.Summary.ID || loaded.Summary.VMStatus != domain.VMStatusStopped {
			t.Fatalf("run=%#v loaded session=%#v", run, loaded.Summary)
		}
	})

	t.Run("driver cannot be combined with existing session", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, func(req *RunAgentRequest) {
			req.SessionID = "existing"
			req.Driver = "docker"
		})
		if !errors.Is(err, ErrInvalidRequest) || execErr != nil {
			t.Fatalf("RunProjectAgent run=%#v err=%v execErr=%v", run, err, execErr)
		}
	})
}

func TestRunsControllerRunProjectAgentCleanupErrorRecording(t *testing.T) {
	t.Run("successful run reports cleanup error", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		fixture.driver.stopErr = errors.New("stop failed")
		run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, nil)
		if err != nil || execErr != nil {
			t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
		}
		if run.Status != domain.ProjectRunStatusSucceeded || !strings.Contains(run.CleanupError, "stop failed") {
			t.Fatalf("run = %#v", run)
		}
		if _, statErr := os.Stat(fixture.store.SessionDir(run.SessionID)); statErr != nil {
			t.Fatalf("session dir should remain when cleanup fails: %v", statErr)
		}
	})

	t.Run("failed run keeps original error and records cleanup error", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		fixture.driver.stopErr = errors.New("stop failed")
		fixture.executor.execErr = errors.New("agent failed")
		fixture.executor.cell = domain.NotebookCell{ID: "cell-1", Type: execution.CellTypeAgent, Output: "failed", Success: false, ExitCode: 7, Stderr: "agent failed"}
		run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, nil)
		if err != nil || execErr == nil || !strings.Contains(execErr.Error(), "agent failed") {
			t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
		}
		if run.Status != domain.ProjectRunStatusFailed || !strings.Contains(run.Error, "agent failed") || !strings.Contains(run.CleanupError, "stop failed") {
			t.Fatalf("run = %#v", run)
		}
	})

	t.Run("session start failure cleans created session", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		fixture.driver.startErr = errors.New("start failed")
		run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, nil)
		if err != nil || execErr == nil || !strings.Contains(execErr.Error(), "start failed") {
			t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
		}
		if run.Status != domain.ProjectRunStatusFailed || run.SessionID == "" || run.CleanupError != "" {
			t.Fatalf("run = %#v", run)
		}
		if _, statErr := os.Stat(fixture.store.SessionDir(run.SessionID)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("created session dir still exists or stat error mismatch: %v", statErr)
		}
	})
}

func TestRunsControllerRunProjectAgentManualTriggerResolution(t *testing.T) {
	fixture := newControllerRunFixture(t)
	trigger := domain.LoaderTrigger{
		LoaderID:   "loader-1",
		ID:         "trigger-1",
		Kind:       domain.LoaderTriggerKindInterval,
		IntervalMs: 1000,
		Enabled:    false,
		SpecJSON:   `{"kind":"interval","intervalMs":1000}`,
	}
	fixture.configDB.schedulers = []domain.ProjectSchedulerRecord{{
		ProjectID:       "project-1",
		SchedulerID:     "scheduler-1",
		AgentName:       "worker",
		ManagedLoaderID: "loader-1",
		Enabled:         false,
		TriggerCount:    1,
	}}
	fixture.configDB.loaders = map[string]domain.Loader{
		"loader-1": {
			Summary: domain.LoaderSummary{
				ID:                 "loader-1",
				Enabled:            false,
				Runtime:            domain.LoaderRuntimeScheduler,
				ManagedProjectID:   "project-1",
				ManagedAgentName:   "worker",
				ManagedSchedulerID: "scheduler-1",
			},
			Script:   `scheduler.interval("trigger-1", async function() { return scheduler.agent("resolved prompt", { sessionEnv: [{ name: "TRIGGER_ENV", value: "yes" }] }); }, 1000);`,
			Triggers: []domain.LoaderTrigger{trigger},
		},
	}
	run, execErr, err := fixture.controller.RunProjectAgent(fixture.ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Source:          domain.ProjectRunSourceManual,
		TriggerID:       "trigger-1",
		ClientRequestID: "manual-trigger",
	}, nil)
	if err != nil || execErr != nil {
		t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
	}
	if run.Status != domain.ProjectRunStatusSucceeded || run.Prompt != "resolved prompt" || run.TriggerID != "trigger-1" || run.SchedulerID != "scheduler-1" {
		t.Fatalf("run = %#v", run)
	}
	session, err := fixture.store.GetSession(fixture.ctx, run.SessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if fixture.executor.request.Message != "resolved prompt" || envItemValue(session.EnvItems, "TRIGGER_ENV") != "yes" {
		t.Fatalf("executor request = %#v", fixture.executor.request)
	}
	if len(run.Warnings) != 2 ||
		!strings.Contains(run.Warnings[0], "scheduler scheduler-1 is disabled") ||
		!strings.Contains(run.Warnings[1], "trigger trigger-1 is disabled") {
		t.Fatalf("warnings = %#v", run.Warnings)
	}
}

func TestRunsControllerRunProjectAgentManualTriggerMissingDoesNotCreateRun(t *testing.T) {
	fixture := newControllerRunFixture(t)
	fixture.configDB.schedulers = []domain.ProjectSchedulerRecord{{
		ProjectID:       "project-1",
		SchedulerID:     "scheduler-1",
		AgentName:       "worker",
		ManagedLoaderID: "loader-1",
		Enabled:         true,
	}}
	fixture.configDB.loaders = map[string]domain.Loader{
		"loader-1": {
			Summary: domain.LoaderSummary{
				ID:                 "loader-1",
				Enabled:            true,
				Runtime:            domain.LoaderRuntimeScheduler,
				ManagedProjectID:   "project-1",
				ManagedAgentName:   "worker",
				ManagedSchedulerID: "scheduler-1",
			},
			Script:   `scheduler.interval("trigger-1", async function() { return scheduler.agent("resolved prompt"); }, 1000);`,
			Triggers: []domain.LoaderTrigger{{LoaderID: "loader-1", ID: "trigger-1", Kind: domain.LoaderTriggerKindInterval, IntervalMs: 1000, Enabled: true}},
		},
	}
	run, execErr, err := fixture.controller.RunProjectAgent(fixture.ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Source:          domain.ProjectRunSourceManual,
		TriggerID:       "missing",
		ClientRequestID: "missing-trigger",
	}, nil)
	if !errors.Is(err, domain.ErrNotFound) || execErr != nil {
		t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
	}
	if len(fixture.configDB.runs) != 0 {
		t.Fatalf("runs created before trigger resolution failure: %#v", fixture.configDB.runs)
	}
}

func TestManualTriggerCaptureHostUnavailableMethodsAndEnvSpecs(t *testing.T) {
	ctx := context.Background()
	host := &manualTriggerCaptureHost{}

	if err := host.Log(ctx, "ignored", map[string]any{"ok": true}); err != nil {
		t.Fatalf("Log returned error: %v", err)
	}
	if _, err := host.PublishEvent(ctx, "runtime.topic", `{}`); err == nil || !strings.Contains(err.Error(), "scheduler.event.publish") {
		t.Fatalf("PublishEvent error = %v", err)
	}
	if _, err := host.Command(ctx, domain.LoaderCommandRequest{}); err == nil || !strings.Contains(err.Error(), "scheduler.command") {
		t.Fatalf("Command error = %v", err)
	}
	if _, err := host.LLM(ctx, "prompt", domain.LoaderLLMRequest{}); err == nil || !strings.Contains(err.Error(), "scheduler.llm") {
		t.Fatalf("LLM error = %v", err)
	}
	if value, ok, err := host.StateGet(ctx, "cursor"); err != nil || ok || value != "" {
		t.Fatalf("StateGet value=%q ok=%v err=%v", value, ok, err)
	}
	if err := host.StateSet(ctx, "cursor", `{}`); err != nil {
		t.Fatalf("StateSet returned error: %v", err)
	}
	if err := host.StateDelete(ctx, "cursor"); err != nil {
		t.Fatalf("StateDelete returned error: %v", err)
	}
	if _, err := host.CallSessionRPC(ctx, "GetSession", `{}`); err == nil || !strings.Contains(err.Error(), "scheduler.session") {
		t.Fatalf("CallSessionRPC error = %v", err)
	}

	specs := envVarSpecsFromSessionEnv([]domain.SessionEnvVar{
		{Name: " A ", Value: "1", Secret: true},
		{Name: " ", Value: "ignored"},
		{Name: "B", Value: "2"},
	})
	if len(specs) != 2 || specs[0].Name != "A" || specs[0].Value != "1" || !specs[0].Secret || specs[1].Name != "B" {
		t.Fatalf("env specs = %#v", specs)
	}
}

func TestRunsControllerApplyJupyterOptionsToSession(t *testing.T) {
	fixture := newControllerRunFixture(t)
	fixture.config.JupyterGuestPort = 8888
	session, err := fixture.store.CreateSession(fixture.ctx, "jupyter session", "", "boxlite", "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	before, err := fixture.store.GetProxyState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState before returned error: %v", err)
	}
	if err := fixture.controller.applyJupyterOptionsToSession(session.Summary.ID, sessionstore.CreateSessionOptions{}); err != nil {
		t.Fatalf("apply empty options returned error: %v", err)
	}
	unchanged, err := fixture.store.GetProxyState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState unchanged returned error: %v", err)
	}
	if unchanged != before {
		t.Fatalf("empty options changed proxy state before=%#v after=%#v", before, unchanged)
	}
	if err := fixture.controller.applyJupyterOptionsToSession(session.Summary.ID, sessionstore.CreateSessionOptions{JupyterExpose: true, JupyterGuestPort: 9999}); err != nil {
		t.Fatalf("apply jupyter options returned error: %v", err)
	}
	enabled, err := fixture.store.GetProxyState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState enabled returned error: %v", err)
	}
	if !enabled.Enabled || !enabled.Exposed || enabled.GuestPort != 9999 || enabled.HostPort == 0 || strings.TrimSpace(enabled.Token) == "" || enabled.JupyterURL != enabled.ProxyPath {
		t.Fatalf("enabled proxy state = %#v", enabled)
	}
}

func TestRunsProjectRunLogAppendChunk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "runs", "run-1", "output.txt")
	if err := appendProjectRunLogChunk(path, domain.ExecChunk{Text: "stdout\n"}); err != nil {
		t.Fatalf("append stdout returned error: %v", err)
	}
	if err := appendProjectRunLogChunk(path, domain.ExecChunk{Text: "stderr\n", Stream: domain.StdioStderr}); err != nil {
		t.Fatalf("append stderr returned error: %v", err)
	}
	if err := appendProjectRunLogChunk(path, domain.ExecChunk{}); err != nil {
		t.Fatalf("append empty returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(data) != "stdout\nstderr\n" {
		t.Fatalf("log content = %q", string(data))
	}
}

func TestRunsControllerHelperEdgeWorkflows(t *testing.T) {
	PrepareStreamingHeaders(nil)
	headers := http.Header{}
	PrepareStreamingHeaders(headers)
	if headers.Get("Cache-Control") != "no-cache, no-transform" || headers.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream headers = %#v", headers)
	}

	baseJupyter := sessionstore.CreateSessionOptions{JupyterGuestPort: 8888}
	if options, err := resolveRunJupyterOptions(baseJupyter, nil); err != nil || options != baseJupyter {
		t.Fatalf("nil jupyter override options=%#v err=%v", options, err)
	}
	if _, err := resolveRunJupyterOptions(baseJupyter, &agentcomposev2.RunJupyterSpec{GuestPort: 65536}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid jupyter guest port err=%v", err)
	}
	options, err := resolveRunJupyterOptions(baseJupyter, &agentcomposev2.RunJupyterSpec{Enabled: true, GuestPort: 9000})
	if err != nil || !options.JupyterEnabled || options.JupyterExpose || options.JupyterGuestPort != 9000 {
		t.Fatalf("enabled jupyter options=%#v err=%v", options, err)
	}
	options, err = resolveRunJupyterOptions(sessionstore.CreateSessionOptions{}, &agentcomposev2.RunJupyterSpec{Expose: true})
	if err != nil || !options.JupyterEnabled || !options.JupyterExpose {
		t.Fatalf("exposed jupyter options=%#v err=%v", options, err)
	}

	if env := execEnvMap(nil); env != nil {
		t.Fatalf("nil env map = %#v", env)
	}
	if env := execEnvMap([]*agentcomposev2.EnvVarSpec{{Name: " "}, nil}); env != nil {
		t.Fatalf("empty env map = %#v", env)
	}
	env := execEnvMap([]*agentcomposev2.EnvVarSpec{
		{Name: " A ", Value: "1"},
		{Name: "B", Value: ""},
		{Name: "A", Value: "2"},
	})
	if len(env) != 2 || env["A"] != "2" || env["B"] != "" {
		t.Fatalf("env map = %#v", env)
	}

	root := t.TempDir()
	session := &domain.Session{Summary: domain.SessionSummary{ID: "session-1", WorkspacePath: filepath.Join(root, "sessions", "session-1", "workspace")}}
	run := domain.ProjectRunRecord{RunID: "run-1"}
	success := transitionFromCommandResult(run, session, "echo ok", domain.ExecResult{Output: "ok\n", ExitCode: 0, Success: true}, nil)
	if success.ExitCode != 0 || success.Error != "" || success.Output != "ok\n" || success.ArtifactsDir == "" || !strings.Contains(success.ResultJSON, `"success":true`) {
		t.Fatalf("success transition = %#v", success)
	}
	failed := transitionFromCommandResult(run, session, "exit 7", domain.ExecResult{Stderr: "stderr detail\n", Output: "output detail\n", Stdout: "stdout detail\n", ExitCode: 7, Success: false}, nil)
	if failed.ExitCode != 7 || !strings.Contains(failed.Error, "stderr detail") {
		t.Fatalf("failed transition = %#v", failed)
	}
	execFailed := transitionFromCommandResult(run, session, "boom", domain.ExecResult{ExitCode: 0, Success: false}, errors.New("exec boom"))
	if execFailed.ExitCode != 1 || !strings.Contains(execFailed.Error, "exec boom") {
		t.Fatalf("exec failed transition = %#v", execFailed)
	}
	if got := projectRunCommandArtifactsDir(run, session); !strings.Contains(got, filepath.Join("state", "runs", "run-1")) {
		t.Fatalf("artifacts dir = %q", got)
	}
	if got := projectRunAgentCellOutputPath(nil, "cell-1"); got != "" {
		t.Fatalf("nil session cell output path = %q", got)
	}
	if got := projectRunAgentCellOutputPath(session, " "); got != "" {
		t.Fatalf("blank cell output path = %q", got)
	}
	if got := projectRunAgentCellOutputPath(session, " cell-1 "); !strings.Contains(got, filepath.Join("state", "cells", "cell-1", "output.txt")) {
		t.Fatalf("cell output path = %q", got)
	}
	if err := appendProjectRunLogChunk("", domain.ExecChunk{Text: "ignored"}); err != nil {
		t.Fatalf("blank append returned error: %v", err)
	}
	fileParent := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(fileParent, []byte("file"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := appendProjectRunLogChunk(filepath.Join(fileParent, "output.txt"), domain.ExecChunk{Text: "chunk"}); err == nil {
		t.Fatalf("expected append error under file parent")
	}

	terminalStore := &fakeRunStore{runs: map[string]domain.ProjectRunRecord{
		"cancel-run": {RunID: "cancel-run", Status: domain.ProjectRunStatusPending},
		"fail-run":   {RunID: "fail-run", Status: domain.ProjectRunStatusPending},
	}}
	coordinator := NewCoordinator(terminalStore, nil)
	canceled, err := markProjectRunTerminalError(context.Background(), coordinator, TransitionRequest{RunID: "cancel-run", Error: "canceled"}, context.Canceled)
	if err != nil || canceled.Status != domain.ProjectRunStatusCanceled {
		t.Fatalf("canceled terminal run=%#v err=%v", canceled, err)
	}
	failedRun, err := markProjectRunTerminalError(context.Background(), coordinator, TransitionRequest{RunID: "fail-run", Error: "failed"}, errors.New("failed"))
	if err != nil || failedRun.Status != domain.ProjectRunStatusFailed {
		t.Fatalf("failed terminal run=%#v err=%v", failedRun, err)
	}

	session.Summary.Tags = []domain.SessionTag{
		{Name: capabilities.CapsetTagName, Value: "dev"},
		{Name: capabilities.CapsetTagName, Value: "missing"},
	}
	provider := fakeCapabilityProvider{
		guides: map[string][]byte{"dev": []byte("Dev guide")},
		errs:   map[string]error{"missing": errors.New("missing guide")},
		target: "cap-proxy.internal:9000",
	}
	guideStore := &fakeGuideSessionStore{}
	streams := sessions.NewStreamBrokerForTest()
	ch, unsubscribe := streams.Subscribe(session.Summary.ID)
	defer unsubscribe()
	writeCapabilityGuide(context.Background(), provider, guideStore, streams, session, capabilities.SessionCapsets(session))
	guidePath := capabilities.SessionGuidePath(session)
	data, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read capability guide: %v", err)
	}
	if !strings.Contains(string(data), "Dev guide") || !strings.Contains(string(data), "cap-proxy.internal:9000") {
		t.Fatalf("capability guide content = %q", data)
	}
	if len(guideStore.events) != 1 || guideStore.events[0].Type != "capability.guide.warning" || !strings.Contains(guideStore.events[0].Message, "missing") {
		t.Fatalf("guide warning events = %#v", guideStore.events)
	}
	select {
	case event := <-ch:
		if event.EventType != sessions.WatchEventTypeEventAdded || event.Event.Type != "capability.guide.warning" {
			t.Fatalf("stream event = %#v", event)
		}
	default:
		t.Fatalf("missing guide warning stream event")
	}
	recordCapabilityGuideWarning(context.Background(), nil, streams, session.Summary.ID, "ignored")
	recordCapabilityGuideWarning(context.Background(), guideStore, streams, " ", "ignored")
}

func TestIntegrationRunsControllerHelperEdgeWorkflows(t *testing.T) {
	TestRunsControllerHelperEdgeWorkflows(t)
}

func TestE2ERunsControllerHelperEdgeWorkflows(t *testing.T) {
	TestRunsControllerHelperEdgeWorkflows(t)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func envItemValue(items []domain.SessionEnvVar, name string) string {
	for _, item := range items {
		if item.Name == name {
			return item.Value
		}
	}
	return ""
}

type fakeRunStore struct {
	project      domain.ProjectRecord
	projectAgent domain.ProjectAgentRecord
	agent        ManagedAgentDefinition
	runs         map[string]domain.ProjectRunRecord
}

func (s *fakeRunStore) GetProject(context.Context, string) (domain.ProjectRecord, error) {
	return s.project, nil
}

func (s *fakeRunStore) GetProjectAgent(context.Context, string, string) (domain.ProjectAgentRecord, error) {
	return s.projectAgent, nil
}

func (s *fakeRunStore) GetManagedAgentDefinition(context.Context, string) (ManagedAgentDefinition, error) {
	return s.agent, nil
}

func (s *fakeRunStore) CreateProjectRun(_ context.Context, run domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	if _, ok := s.runs[run.RunID]; ok {
		return domain.ProjectRunRecord{}, sql.ErrNoRows
	}
	s.runs[run.RunID] = run
	return run, nil
}

func (s *fakeRunStore) GetProjectRun(_ context.Context, runID string) (domain.ProjectRunRecord, error) {
	run, ok := s.runs[runID]
	if !ok {
		return domain.ProjectRunRecord{}, sql.ErrNoRows
	}
	return run, nil
}

func (s *fakeRunStore) UpdateProjectRun(_ context.Context, run domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	s.runs[run.RunID] = run
	return run, nil
}

type fakePreparationStore struct {
	project  domain.ProjectRecord
	revision domain.ProjectRevisionRecord
	agent    domain.AgentDefinition
	global   []domain.SessionEnvVar
}

func (s *fakePreparationStore) GetProject(context.Context, string) (domain.ProjectRecord, error) {
	return s.project, nil
}

func (s *fakePreparationStore) GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error) {
	return s.revision, nil
}

func (s *fakePreparationStore) GetAgentDefinition(context.Context, string) (domain.AgentDefinition, error) {
	return s.agent, nil
}

func (s *fakePreparationStore) ListGlobalEnv(context.Context) ([]domain.SessionEnvVar, error) {
	return s.global, nil
}

type fakeProjectSessionRunStore struct {
	runs []domain.ProjectRunRecord
}

func (s fakeProjectSessionRunStore) ListProjectSessionRuns(context.Context, domain.ProjectSessionRelationFilter) ([]domain.ProjectRunRecord, error) {
	return s.runs, nil
}

type fakeSessionStatusStore struct {
	sessions map[string]*domain.Session
}

func (s fakeSessionStatusStore) GetSession(_ context.Context, id string) (*domain.Session, error) {
	session, ok := s.sessions[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	return session, nil
}

type fakeControllerStore struct {
	project      domain.ProjectRecord
	projectAgent domain.ProjectAgentRecord
	managed      ManagedAgentDefinition
	revision     domain.ProjectRevisionRecord
	agent        domain.AgentDefinition
	global       []domain.SessionEnvVar
	runs         map[string]domain.ProjectRunRecord
	schedulers   []domain.ProjectSchedulerRecord
	loaders      map[string]domain.Loader
}

func (s *fakeControllerStore) GetProject(context.Context, string) (domain.ProjectRecord, error) {
	return s.project, nil
}

func (s *fakeControllerStore) GetProjectAgent(context.Context, string, string) (domain.ProjectAgentRecord, error) {
	return s.projectAgent, nil
}

func (s *fakeControllerStore) GetManagedAgentDefinition(context.Context, string) (ManagedAgentDefinition, error) {
	return s.managed, nil
}

func (s *fakeControllerStore) CreateProjectRun(_ context.Context, run domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	if s.runs == nil {
		s.runs = map[string]domain.ProjectRunRecord{}
	}
	if _, ok := s.runs[run.RunID]; ok {
		return domain.ProjectRunRecord{}, sql.ErrNoRows
	}
	s.runs[run.RunID] = run
	return run, nil
}

func (s *fakeControllerStore) GetProjectRun(_ context.Context, runID string) (domain.ProjectRunRecord, error) {
	run, ok := s.runs[runID]
	if !ok {
		return domain.ProjectRunRecord{}, sql.ErrNoRows
	}
	return run, nil
}

func (s *fakeControllerStore) UpdateProjectRun(_ context.Context, run domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	s.runs[run.RunID] = run
	return run, nil
}

func (s *fakeControllerStore) GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error) {
	return s.revision, nil
}

func (s *fakeControllerStore) GetAgentDefinition(context.Context, string) (domain.AgentDefinition, error) {
	return s.agent, nil
}

func (s *fakeControllerStore) ListGlobalEnv(context.Context) ([]domain.SessionEnvVar, error) {
	return s.global, nil
}

func (s *fakeControllerStore) GetWorkspaceConfig(context.Context, string) (domain.WorkspaceConfig, error) {
	return domain.WorkspaceConfig{}, domain.ErrNotFound
}

func (s *fakeControllerStore) ListProjectSchedulers(_ context.Context, projectID string) ([]domain.ProjectSchedulerRecord, error) {
	var items []domain.ProjectSchedulerRecord
	for _, scheduler := range s.schedulers {
		if scheduler.ProjectID == projectID {
			items = append(items, scheduler)
		}
	}
	return items, nil
}

func (s *fakeControllerStore) ListManagedLoaders(_ context.Context, projectID string) ([]domain.Loader, error) {
	var items []domain.Loader
	for _, loader := range s.loaders {
		if loader.Summary.ManagedProjectID == projectID {
			items = append(items, loader)
		}
	}
	if len(items) > 0 {
		return items, nil
	}
	for _, scheduler := range s.schedulers {
		if scheduler.ProjectID != projectID {
			continue
		}
		items = append(items, domain.Loader{
			Summary: domain.LoaderSummary{
				ID:                 scheduler.ManagedLoaderID,
				Enabled:            scheduler.Enabled,
				ManagedProjectID:   scheduler.ProjectID,
				ManagedRevision:    scheduler.Revision,
				ManagedAgentName:   scheduler.AgentName,
				ManagedSchedulerID: scheduler.SchedulerID,
			},
			Triggers: make([]domain.LoaderTrigger, scheduler.TriggerCount),
		})
	}
	return items, nil
}

func (s *fakeControllerStore) GetSchedulerExecution(_ context.Context, loaderID string) (domain.Loader, error) {
	if s.loaders == nil {
		return domain.Loader{}, domain.ErrNotFound
	}
	loader, ok := s.loaders[loaderID]
	if !ok {
		return domain.Loader{}, domain.ErrNotFound
	}
	return loader, nil
}

type fakeControllerDriver struct {
	started  bool
	stopped  bool
	startErr error
	stopErr  error
	store    *sessionstore.Store
}

func (d *fakeControllerDriver) StartSessionVM(_ context.Context, session *domain.Session) error {
	d.started = true
	if d.startErr != nil {
		return d.startErr
	}
	if d.store != nil {
		return d.store.SaveVMState(session.Summary.ID, domain.VMState{Driver: session.Summary.Driver, BoxID: "box-1"})
	}
	return nil
}

func (d *fakeControllerDriver) StopSessionVM(context.Context, *domain.Session) error {
	d.stopped = true
	if d.stopErr != nil {
		return d.stopErr
	}
	return nil
}

type fakeControllerExecutor struct {
	request execution.ExecuteAgentRequest
	cell    domain.NotebookCell
	execErr error
}

func (e *fakeControllerExecutor) ExecuteAgentRequest(_ context.Context, _ *domain.Session, req execution.ExecuteAgentRequest) (domain.NotebookCell, domain.SessionEvent, domain.SessionEvent, error) {
	e.request = req
	if req.Stream.OnStart != nil {
		if err := req.Stream.OnStart(domain.NotebookCell{ID: "cell-1"}); err != nil {
			return domain.NotebookCell{}, domain.SessionEvent{}, domain.SessionEvent{}, err
		}
	}
	if req.Stream.OnChunk != nil {
		if err := req.Stream.OnChunk("cell-1", domain.ExecChunk{Text: "chunk"}); err != nil {
			return domain.NotebookCell{}, domain.SessionEvent{}, domain.SessionEvent{}, err
		}
	}
	cell := e.cell
	if strings.TrimSpace(cell.ID) == "" {
		cell = domain.NotebookCell{ID: "cell-1", Type: execution.CellTypeAgent, Output: "done", Success: true, ExitCode: 0}
	}
	return cell,
		domain.SessionEvent{ID: "user", Type: "user", Message: req.Message},
		domain.SessionEvent{ID: "assistant", Type: "assistant", Message: "done"},
		e.execErr
}

type fakeControllerRuntime struct {
	spec      domain.ExecSpec
	result    domain.RuntimeCommandResult
	rawResult *domain.ExecResult
	execErr   error
}

func (r *fakeControllerRuntime) ExecStream(_ context.Context, _ *domain.Session, _ domain.VMState, spec domain.ExecSpec, writer domain.ExecStreamWriter) (domain.ExecResult, error) {
	r.spec = spec
	if r.rawResult != nil {
		return *r.rawResult, r.execErr
	}
	result := r.result
	if result.Stdout == "" && result.Stderr == "" && result.Output == "" && result.ExitCode == 0 && !result.Success {
		result = domain.RuntimeCommandResult{Stdout: "command output\n", Output: "command output\n", ExitCode: 0, Success: true}
	}
	if result.Stdout != "" && writer != nil {
		writer(domain.ExecChunk{Text: result.Stdout})
	}
	if result.Stderr != "" && writer != nil {
		writer(domain.ExecChunk{Text: result.Stderr, Stream: domain.StdioStderr})
	}
	payload := fakeRuntimeCommandPayload(result)
	if writer != nil {
		writer(domain.ExecChunk{Text: payload})
	}
	return domain.ExecResult{Stdout: result.Stdout + payload, Stderr: result.Stderr, Output: result.Output + payload, ExitCode: result.ExitCode, Success: result.Success}, r.execErr
}

func fakeRuntimeCommandPayload(result domain.RuntimeCommandResult) string {
	data, _ := json.Marshal(result)
	return execution.CommandResultPrefix + string(data) + "\n"
}

type fakeControllerImages struct{}

func (fakeControllerImages) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{}, nil
}

func (fakeControllerImages) PullImage(context.Context, images.PullRequest) (images.PullResult, error) {
	return images.PullResult{}, nil
}

func (fakeControllerImages) InspectImage(context.Context, images.InspectRequest) (images.InspectResult, error) {
	return images.InspectResult{}, nil
}

func (fakeControllerImages) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{}, nil
}

type fakeControllerPublisher struct {
	events []domain.LoaderTopicEvent
}

func (p *fakeControllerPublisher) Publish(event domain.LoaderTopicEvent) bool {
	p.events = append(p.events, event)
	return true
}

type fakeControllerDashboard struct {
	reasons []string
}

func (d *fakeControllerDashboard) Notify(reason string) {
	d.reasons = append(d.reasons, reason)
}

type fakeCapabilityProvider struct {
	guides map[string][]byte
	errs   map[string]error
	target string
}

func (p fakeCapabilityProvider) Status(context.Context) capability.Status {
	return capability.Status{Configured: true, OK: true, Status: "ok"}
}

func (p fakeCapabilityProvider) ListCapsets(context.Context) ([]capability.Capset, error) {
	return []capability.Capset{}, nil
}

func (p fakeCapabilityProvider) Catalog(context.Context, string) (capability.Catalog, error) {
	return capability.Catalog{}, nil
}

func (p fakeCapabilityProvider) CapabilityGuide(_ context.Context, capsetID string) ([]byte, error) {
	if err := p.errs[capsetID]; err != nil {
		return nil, err
	}
	if guide := p.guides[capsetID]; guide != nil {
		return guide, nil
	}
	return nil, errors.New("not found")
}

func (p fakeCapabilityProvider) ProxyTarget() string {
	return p.target
}

type fakeGuideSessionStore struct {
	events []domain.SessionEvent
}

func (s *fakeGuideSessionStore) CreateSessionWithOptions(context.Context, string, string, string, string, string, string, *sessionstore.SessionWorkspace, []sessionstore.SessionEnvVar, []sessionstore.SessionTag, sessionstore.CreateSessionOptions) (*sessionstore.Session, error) {
	return nil, errors.New("not implemented")
}

func (s *fakeGuideSessionStore) GetSession(context.Context, string) (*sessionstore.Session, error) {
	return nil, errors.New("not implemented")
}

func (s *fakeGuideSessionStore) UpdateSession(context.Context, *sessionstore.Session) error {
	return errors.New("not implemented")
}

func (s *fakeGuideSessionStore) RemoveSession(context.Context, string) error {
	return errors.New("not implemented")
}

func (s *fakeGuideSessionStore) AddEvent(_ context.Context, _ string, event sessionstore.SessionEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *fakeGuideSessionStore) GetVMState(string) (sessionstore.VMState, error) {
	return sessionstore.VMState{}, errors.New("not implemented")
}

func (s *fakeGuideSessionStore) GetProxyState(string) (sessionstore.ProxyState, error) {
	return sessionstore.ProxyState{}, errors.New("not implemented")
}

func (s *fakeGuideSessionStore) SaveProxyState(string, sessionstore.ProxyState) error {
	return errors.New("not implemented")
}

func (s *fakeGuideSessionStore) AllocateHostPortForJupyter() (int, error) {
	return 0, errors.New("not implemented")
}
