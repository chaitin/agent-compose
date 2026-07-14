package runs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
	"agent-compose/pkg/volumes"
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
	if running, err := coord.MarkRunning(ctx, run.RunID, "sandbox-1"); err != nil || running.Status != domain.ProjectRunStatusRunning || running.SandboxID != "sandbox-1" {
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

	title := SandboxTitle(run)
	tags := MergeSandboxTags([]domain.SandboxTag{{Name: "project", Value: "project-1"}}, SandboxTags(run))
	if title == "" || len(tags) < 4 || WorkspaceID(run, "git") == "" || WorkspaceName(run, "git") == "" {
		t.Fatalf("session helpers title=%q tags=%#v", title, tags)
	}
	cell := domain.NotebookCell{ID: "cell-1", Type: execution.CellTypeAgent, Agent: "codex", AgentThreadID: "agent-thread", Output: "output", Success: false, ExitCode: 0, Stderr: "stderr"}
	transition := TransitionFromAgentCell(run, &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", WorkspacePath: t.TempDir()}}, cell, nil)
	if transition.ExitCode == 0 || !strings.Contains(transition.Error, "stderr") || transition.ArtifactsDir == "" {
		t.Fatalf("transition from failed cell = %#v", transition)
	}
	transition = TransitionFromAgentCell(run, nil, cell, errors.New("boom"))
	if transition.ExitCode == 0 || !strings.Contains(transition.Error, "boom") {
		t.Fatalf("transition from exec error = %#v", transition)
	}
	if !CleanupPolicyStopsSandbox(agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_STOP_ON_COMPLETION) ||
		!CleanupPolicyStopsSandbox(agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION) ||
		CleanupPolicyStopsSandbox(agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING) ||
		!CleanupPolicyRemovesSandbox(agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION) ||
		CleanupPolicyRemovesSandbox(agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_STOP_ON_COMPLETION) {
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
	spec := `{"variables":[{"name":"PROJECT_VAR","value":"project"}],"agents":[{"name":"worker","workspace":{"provider":"local","path":"."}}]}`
	store := &fakePreparationStore{
		project:  domain.ProjectRecord{ID: "project-1", Name: "Project", SourcePath: sourceDir},
		revision: domain.ProjectRevisionRecord{ProjectID: "project-1", Revision: 1, SpecJSON: spec},
		agent:    domain.AgentDefinition{ID: "agent-1", Name: "Agent", EnvItems: []domain.SandboxEnvVar{{Name: "AGENT_VAR", Value: "agent"}}, CapsetIDs: []string{"dev"}},
		global:   []domain.SandboxEnvVar{{Name: "GLOBAL_VAR", Value: "global"}},
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
	if merged := MergeEnvItems([]domain.SandboxEnvVar{{Name: "A", Value: "1"}}, []domain.SandboxEnvVar{{Name: "A", Value: "2"}}); len(merged) != 1 || merged[0].Value != "2" {
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
	if _, err := controller.prepareProjectRunWorkspace(ctx, run, store.project, nil, &compose.WorkspaceSpec{}); err == nil || !strings.Contains(err.Error(), "provider is required") {
		t.Fatalf("missing provider err=%v", err)
	}
	if _, err := controller.prepareProjectRunWorkspace(ctx, run, store.project, nil, &compose.WorkspaceSpec{Provider: "s3"}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported provider err=%v", err)
	}
	localWorkspace, err := controller.prepareProjectRunWorkspace(ctx, run, store.project, nil, &compose.WorkspaceSpec{Provider: "local", Path: "."})
	if err != nil || localWorkspace == nil || localWorkspace.Type != "file" {
		t.Fatalf("agent local workspace = %#v/%v", localWorkspace, err)
	}
	if _, err := (&Controller{}).materializeLocalProjectRunWorkspace(run, store.project, &compose.WorkspaceSpec{Provider: "local", Path: "."}); err == nil {
		t.Fatalf("materialize without config returned nil error")
	}
	if _, err := controller.materializeLocalProjectRunWorkspace(run, store.project, &compose.WorkspaceSpec{Provider: "local", Path: "missing"}); err == nil {
		t.Fatalf("materialize missing local path returned nil error")
	}
	if snapshot := toSandboxWorkspaceSnapshot(domain.WorkspaceConfig{ID: "workspace", Name: "Workspace", Type: "file", ConfigJSON: "{}"}); snapshot.ID != "workspace" {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	statusStore := fakeProjectSandboxRunStore{runs: []domain.ProjectRunRecord{{RunID: "run-1", SandboxID: "sandbox-1"}, {RunID: "run-2", SandboxID: "sandbox-1"}, {RunID: "run-3", SandboxID: "missing"}}}
	statuses, err := ListProjectSandboxStatuses(ctx, statusStore, fakeSandboxStatusStore{sessions: map[string]*domain.Sandbox{"sandbox-1": {Summary: domain.SandboxSummary{ID: "sandbox-1"}}}}, domain.ProjectSandboxRelationFilter{})
	if err != nil || len(statuses) != 2 || statuses[1].SandboxMissing != true {
		t.Fatalf("ListProjectSandboxStatuses statuses=%#v err=%v", statuses, err)
	}
	if _, err := ListProjectSandboxStatuses(ctx, nil, fakeSandboxStatusStore{}, domain.ProjectSandboxRelationFilter{}); err == nil {
		t.Fatalf("expected nil run store error")
	}
	if _, err := ListProjectSandboxStatuses(ctx, statusStore, nil, domain.ProjectSandboxRelationFilter{}); err == nil {
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
		DataRoot:           root,
		SandboxRoot:        filepath.Join(root, "sandboxes"),
		RuntimeDriver:      driverpkg.RuntimeDriverDocker,
		DefaultImage:       "guest:latest",
		DockerDefaultImage: "guest:latest",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	configDB := &fakeControllerStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 1},
		projectAgent: domain.ProjectAgentRecord{
			ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Driver: driverpkg.RuntimeDriverDocker, Image: "guest:latest",
		},
		managed: ManagedAgentDefinition{
			ID: "agent-1", Enabled: true, Driver: driverpkg.RuntimeDriverDocker, GuestImage: "guest:latest", ManagedProjectID: "project-1", ManagedAgentName: "worker",
		},
		revision: domain.ProjectRevisionRecord{
			ProjectID: "project-1",
			Revision:  1,
			SpecJSON:  `{"agents":[{"name":"worker"}],"variables":[{"name":"PROJECT_VAR","value":"project"}]}`,
		},
		agent:  domain.AgentDefinition{ID: "agent-1", Provider: "codex", Model: "gpt", EnvItems: []domain.SandboxEnvVar{{Name: "AGENT_VAR", Value: "agent"}}},
		global: []domain.SandboxEnvVar{{Name: "GLOBAL_VAR", Value: "global"}},
		runs:   map[string]domain.ProjectRunRecord{},
	}
	driver := &fakeControllerDriver{store: store}
	executor := &fakeControllerExecutor{}
	bus := &fakeControllerPublisher{}
	dashboard := &fakeControllerDashboard{}
	controller := NewController(ControllerDependencies{
		Config:           config,
		Store:            store,
		ConfigDB:         configDB,
		WorkspaceEnsurer: &controllerWorkspaceEnsurer{},
		Driver:           driver,
		Executor:         executor,
		Runtime: func(*domain.Sandbox) (Runtime, error) {
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
		CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_STOP_ON_COMPLETION,
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
	if run.Status != domain.ProjectRunStatusSucceeded || run.SandboxID == "" || run.Output != "done" {
		t.Fatalf("run = %#v", run)
	}
	if !strings.Contains(run.ArtifactsDir, filepath.Join("state", "cells", "cell-1")) || filepath.Base(run.LogsPath) != "output.txt" {
		t.Fatalf("agent run artifact paths = artifacts:%q logs:%q", run.ArtifactsDir, run.LogsPath)
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
	proxyState, err := store.GetProxyState(run.SandboxID)
	if data, err := os.ReadFile(filepath.Join(run.ArtifactsDir, "output.txt")); err != nil || string(data) != "chunk" {
		t.Fatalf("agent run output artifact = %q err=%v", string(data), err)
	}
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
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverDocker,
		DefaultImage:         "guest:latest",
		DockerDefaultImage:   "guest:latest",
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
			ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Driver: driverpkg.RuntimeDriverDocker, Image: "guest:latest",
		},
		managed: ManagedAgentDefinition{
			ID: "agent-1", Enabled: true, Driver: driverpkg.RuntimeDriverDocker, GuestImage: "guest:latest", ManagedProjectID: "project-1", ManagedAgentName: "worker",
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
		Config:           config,
		Store:            store,
		ConfigDB:         configDB,
		WorkspaceEnsurer: &controllerWorkspaceEnsurer{},
		Driver:           &fakeControllerDriver{store: store},
		Executor:         &fakeControllerExecutor{},
		Images:           fakeControllerImages{},
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
	proxyState, err := store.GetProxyState(run.SandboxID)
	if err != nil {
		t.Fatalf("GetProxyState returned error: %v", err)
	}
	if !proxyState.Enabled || !proxyState.Exposed || proxyState.GuestPort != 9999 || proxyState.HostPort != 0 || proxyState.Token == "" {
		t.Fatalf("proxy state = %+v, want YAML guest port with CLI expose", proxyState)
	}
}

func TestRunsControllerRunProjectAgentResolvesVolumeMounts(t *testing.T) {
	fixture := newControllerRunFixture(t)
	hostPath := t.TempDir()
	fixture.configDB.agent.Volumes = []domain.VolumeMountSpec{{
		Type:   domain.VolumeMountTypeVolume,
		Source: "cache",
		Target: "/cache",
	}}
	fixture.configDB.projectVolumes = map[string]domain.VolumeRecord{
		"cache": {ID: "vol-cache", Name: "project_cache", Driver: domain.VolumeDriverLocal, Path: hostPath},
	}
	resolver := &fakeVolumeResolver{
		mounts: []domain.SandboxVolumeMount{{
			ID:       "mount-cache",
			Type:     domain.VolumeMountTypeVolume,
			Source:   "cache",
			Target:   "/cache",
			VolumeID: "vol-cache",
			Driver:   domain.VolumeDriverLocal,
			HostPath: hostPath,
		}},
		warnings: []string{"volume target /cache overlaps test path"},
	}
	fixture.controller.volumes = resolver

	run, execErr, err := fixture.controller.RunProjectAgent(fixture.ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Prompt:          "do work",
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: "volume-request",
	}, nil)
	if err != nil || execErr != nil {
		t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
	}
	if len(resolver.specs) != 1 || resolver.specs[0].Source != "cache" || resolver.options.ProjectVolumes["cache"].ID != "vol-cache" {
		t.Fatalf("resolver specs=%#v options=%#v", resolver.specs, resolver.options)
	}
	session, err := fixture.store.GetSandbox(fixture.ctx, run.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	if len(session.VolumeMounts) != 1 || session.VolumeMounts[0].HostPath != hostPath || session.VolumeMounts[0].Target != "/cache" {
		t.Fatalf("sandbox volume mounts = %#v", session.VolumeMounts)
	}
	if len(run.Warnings) != 1 || !strings.Contains(run.Warnings[0], "volume target /cache") {
		t.Fatalf("run warnings = %#v", run.Warnings)
	}
}

func TestRunsControllerRunProjectAgentRejectsRequestVolumesWithExistingSandbox(t *testing.T) {
	fixture := newControllerRunFixture(t)
	session, err := fixture.store.CreateSandbox(fixture.ctx, "existing", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	run, execErr, err := fixture.controller.RunProjectAgent(fixture.ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Command:         "echo ok",
		SandboxID:       session.Summary.ID,
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: "volume-existing-sandbox",
		Volumes: []domain.VolumeMountSpec{{
			Type:   domain.VolumeMountTypeBind,
			Source: ".",
			Target: "/cache",
		}},
	}, nil)
	if !errors.Is(err, ErrInvalidRequest) || execErr != nil {
		t.Fatalf("RunProjectAgent run=%#v err=%v execErr=%v", run, err, execErr)
	}
}

func TestIntegrationRunsControllerRunProjectAgentSuccessWorkflow(t *testing.T) {
	testRunsControllerPersistenceBoundaryWorkflows(t)
}

func TestE2ERunsControllerRunProjectAgentSuccessWorkflow(t *testing.T) {
	testRunsControllerPersistenceBoundaryWorkflows(t)
}

func testRunsControllerPersistenceBoundaryWorkflows(t *testing.T) {
	t.Helper()
	t.Run("success", TestRunsControllerRunProjectAgentSuccessWorkflow)
	t.Run("jupyter config", TestRunsControllerRunProjectAgentResolvesJupyterConfig)
	t.Run("volume mounts", TestRunsControllerRunProjectAgentResolvesVolumeMounts)
	t.Run("existing sandbox volumes", TestRunsControllerRunProjectAgentRejectsRequestVolumesWithExistingSandbox)
	t.Run("manual trigger resolution", TestRunsControllerRunProjectAgentManualTriggerResolution)
	t.Run("manual trigger payload", TestRunsControllerRunProjectAgentManualTriggerPayload)
	t.Run("manual trigger prompt", TestRunsControllerRunProjectAgentManualTriggerPromptOverride)
	t.Run("manual trigger missing", TestRunsControllerRunProjectAgentManualTriggerMissingDoesNotCreateRun)
	t.Run("sticky binding scope", TestRunsControllerStickyBindingsAreScopedByTrigger)
	t.Run("unsupported persistence boundary", TestRunsControllerRejectsUncompiledScheduledSandboxBeforePersistence)
	t.Run("apply jupyter", TestRunsControllerApplyJupyterOptionsToSandbox)
	t.Run("docker host port", TestRunsControllerApplyJupyterOptionsLeavesDockerHostPortForRuntime)
}

func TestRunsControllerRunProjectAgentCommandWorkflow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:           root,
		SandboxRoot:        filepath.Join(root, "sandboxes"),
		RuntimeDriver:      driverpkg.RuntimeDriverDocker,
		DefaultImage:       "guest:latest",
		DockerDefaultImage: "guest:latest",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	configDB := &fakeControllerStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 1},
		projectAgent: domain.ProjectAgentRecord{
			ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Driver: driverpkg.RuntimeDriverDocker, Image: "guest:latest",
		},
		managed: ManagedAgentDefinition{
			ID: "agent-1", Enabled: true, Driver: driverpkg.RuntimeDriverDocker, GuestImage: "guest:latest", ManagedProjectID: "project-1", ManagedAgentName: "worker",
		},
		revision: domain.ProjectRevisionRecord{ProjectID: "project-1", Revision: 1, SpecJSON: `{"agents":[{"name":"worker"}]}`},
		agent:    domain.AgentDefinition{ID: "agent-1", Provider: "codex"},
		runs:     map[string]domain.ProjectRunRecord{},
	}
	runtime := &fakeControllerRuntime{}
	controller := NewController(ControllerDependencies{
		Config:           config,
		Store:            store,
		ConfigDB:         configDB,
		WorkspaceEnsurer: &controllerWorkspaceEnsurer{},
		Driver:           &fakeControllerDriver{store: store},
		Runtime: func(*domain.Sandbox) (Runtime, error) {
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
	if !strings.Contains(run.ArtifactsDir, filepath.Join("state", "runs", run.RunID)) || filepath.Base(run.LogsPath) != "transcript.txt" {
		t.Fatalf("command run artifact paths = artifacts:%q logs:%q", run.ArtifactsDir, run.LogsPath)
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
		SandboxID:       run.SandboxID,
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: "command-request-2",
		CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
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
		DataRoot:           root,
		SandboxRoot:        filepath.Join(root, "sandboxes"),
		RuntimeDriver:      driverpkg.RuntimeDriverDocker,
		DefaultImage:       "guest:latest",
		DockerDefaultImage: "guest:latest",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	configDB := &fakeControllerStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 1},
		projectAgent: domain.ProjectAgentRecord{
			ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Driver: driverpkg.RuntimeDriverDocker, Image: "guest:latest",
		},
		managed: ManagedAgentDefinition{
			ID: "agent-1", Enabled: true, Driver: driverpkg.RuntimeDriverDocker, GuestImage: "guest:latest", ManagedProjectID: "project-1", ManagedAgentName: "worker",
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
		Config:           config,
		Store:            store,
		ConfigDB:         configDB,
		WorkspaceEnsurer: &controllerWorkspaceEnsurer{},
		Driver:           &fakeControllerDriver{store: store},
		Runtime: func(*domain.Sandbox) (Runtime, error) {
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

func TestRunsControllerRunProjectCommandAttachProjectsOutputAndResult(t *testing.T) {
	ctx := context.Background()
	controller, configDB, runtime := newTestRunAttachController(t, []driverpkg.RuntimeOutputFrame{
		{Type: driverpkg.RuntimeOutputStarted},
		{Type: driverpkg.RuntimeOutputStdout, Data: []byte("hello\n")},
		{Type: driverpkg.RuntimeOutputStderr, Data: []byte("warn\n")},
		{Type: driverpkg.RuntimeOutputResult, Result: &driverpkg.RuntimeResult{OperationID: "run-attach", ExitCode: 0, Success: true}},
	})
	requests := []*agentcomposev2.RunAttachRequest{{
		Frame: &agentcomposev2.RunAttachRequest_Start{Start: &agentcomposev2.RunAttachStart{
			Request: &agentcomposev2.RunAgentRequest{
				ProjectId:       "project-1",
				AgentName:       "worker",
				Command:         "echo hello",
				Source:          agentcomposev2.RunSource_RUN_SOURCE_API,
				ClientRequestId: "attach-request",
			},
			Mode:        agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND,
			AttachStdin: true,
		}},
	}}
	var responses []*agentcomposev2.RunAttachResponse
	err := controller.RunProjectCommandAttach(ctx, recvRunAttachRequests(requests), func(resp *agentcomposev2.RunAttachResponse) error {
		if started := resp.GetStarted(); started != nil {
			stored, err := configDB.GetProjectRun(ctx, started.GetRunId())
			if err != nil {
				t.Fatalf("get running command attach run: %v", err)
			}
			if stored.LogsPath == "" || stored.ArtifactsDir == "" {
				t.Fatalf("running command attach run paths were not persisted: %#v", stored)
			}
		}
		responses = append(responses, resp)
		return nil
	})
	if err != nil {
		t.Fatalf("RunProjectCommandAttach returned error: %v", err)
	}
	if len(responses) != 4 || responses[0].GetStarted() == nil || responses[1].GetOutput() == nil || responses[2].GetOutput() == nil || responses[3].GetResult() == nil {
		t.Fatalf("attach responses = %#v", responses)
	}
	if responses[1].GetOutput().GetTranscript().GetText() != "hello\n" || responses[2].GetOutput().GetStream() != agentcomposev2.StdioStream_STDIO_STREAM_STDERR {
		t.Fatalf("attach output projection = %#v / %#v", responses[1].GetOutput(), responses[2].GetOutput())
	}
	run := responses[3].GetResult().GetRun()
	if run.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || !responses[3].GetResult().GetSuccess() {
		t.Fatalf("attach result = %#v", responses[3].GetResult())
	}
	if runtime.spec.ArtifactDir != "" || runtime.spec.Command == nil || runtime.spec.Command.Command != "bash" || strings.Join(runtime.spec.Command.Args, " ") != "-lc echo hello" {
		t.Fatalf("runtime spec = %#v", runtime.spec)
	}
	stored, err := configDB.GetProjectRun(ctx, run.GetRunId())
	if err != nil || stored.Status != domain.ProjectRunStatusSucceeded || stored.Output != "hello\nwarn\n" || stored.LogsPath == "" {
		t.Fatalf("stored run = %#v err=%v", stored, err)
	}
	data, err := os.ReadFile(stored.LogsPath)
	if err != nil || string(data) != "hello\nwarn\n" {
		t.Fatalf("attach transcript = %q err=%v", string(data), err)
	}
}

func TestRunsControllerRunProjectCommandAttachValidatesStartFrame(t *testing.T) {
	controller, _, _ := newTestRunAttachController(t, nil)
	err := controller.RunProjectCommandAttach(context.Background(), recvRunAttachRequests([]*agentcomposev2.RunAttachRequest{{
		Frame: &agentcomposev2.RunAttachRequest_Stdin{Stdin: &agentcomposev2.AttachStdin{Data: []byte("x")}},
	}}), func(*agentcomposev2.RunAttachResponse) error { return nil })
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("RunProjectCommandAttach first frame error = %v, want ErrInvalidRequest", err)
	}
}

func TestRunsControllerRunProjectPromptAttachProjectsAgentFrames(t *testing.T) {
	ctx := context.Background()
	controller, configDB, runtime := newTestRunAttachController(t, []driverpkg.RuntimeOutputFrame{
		{Type: driverpkg.RuntimeOutputStarted},
		{Type: driverpkg.RuntimeOutputStdout, Data: []byte(`{"v":1,"seq":0,"type":"started","provider":"codex","sessionId":"thread-1"}` + "\n")},
		{Type: driverpkg.RuntimeOutputStdout, Data: []byte(`{"v":1,"seq":1,"type":"agent_event","event":{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"hello agent\n"}}}` + "\n")},
		{Type: driverpkg.RuntimeOutputStdout, Data: []byte(`{"v":1,"seq":2,"type":"agent_turn_completed","provider":"codex","sessionId":"thread-1","finalText":"hello agent\n"}` + "\n")},
		{Type: driverpkg.RuntimeOutputStdout, Data: []byte(`{"v":1,"seq":3,"type":"result","provider":"codex","sessionId":"thread-1","stopReason":"eof","finalText":"hello agent\n","transcript":"hello agent\n"}` + "\n")},
		{Type: driverpkg.RuntimeOutputResult, Result: &driverpkg.RuntimeResult{OperationID: "run-attach", ExitCode: 0, Success: true}},
	})
	requests := []*agentcomposev2.RunAttachRequest{{
		Frame: &agentcomposev2.RunAttachRequest_Start{Start: &agentcomposev2.RunAttachStart{
			Request: &agentcomposev2.RunAgentRequest{ProjectId: "project-1", AgentName: "worker", Prompt: "hello"},
			Mode:    agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
		}},
	}}
	var responses []*agentcomposev2.RunAttachResponse
	err := controller.RunProjectCommandAttach(ctx, recvRunAttachRequests(requests), func(resp *agentcomposev2.RunAttachResponse) error {
		if started := resp.GetStarted(); started != nil {
			stored, err := configDB.GetProjectRun(ctx, started.GetRunId())
			if err != nil {
				t.Fatalf("get running prompt attach run: %v", err)
			}
			if stored.LogsPath == "" || stored.ArtifactsDir == "" {
				t.Fatalf("running prompt attach run paths were not persisted: %#v", stored)
			}
		}
		responses = append(responses, resp)
		return nil
	})
	if err != nil {
		t.Fatalf("RunProjectCommandAttach prompt mode returned error: %v", err)
	}
	if len(responses) != 5 || responses[0].GetStarted() == nil || responses[1].GetAgentEvent() == nil || responses[2].GetAgentEvent() == nil || responses[3].GetAgentTurnCompleted() == nil || responses[4].GetResult() == nil {
		t.Fatalf("prompt attach responses = %#v", responses)
	}
	if responses[2].GetAgentEvent().GetText() != "hello agent\n" {
		t.Fatalf("agent event text = %q", responses[2].GetAgentEvent().GetText())
	}
	if runtime.spec.Command == nil || runtime.spec.Command.Command != "sh" || !strings.Contains(strings.Join(runtime.spec.Command.Args, " "), "agent-compose-runtime stream") || runtime.spec.TTY {
		t.Fatalf("runtime spec = %#v", runtime.spec)
	}
	if runtime.interaction == nil || len(runtime.interaction.sent) < 2 {
		t.Fatalf("runtime sent frames = %#v", runtime.interaction)
	}
	var startFrame map[string]any
	if err := json.Unmarshal(runtime.interaction.sent[0].Data, &startFrame); err != nil {
		t.Fatalf("start frame json: %v", err)
	}
	if startFrame["type"] != "start" || startFrame["provider"] != "codex" {
		t.Fatalf("start frame = %#v", startFrame)
	}
	var humanFrame map[string]any
	if err := json.Unmarshal(runtime.interaction.sent[1].Data, &humanFrame); err != nil {
		t.Fatalf("human frame json: %v", err)
	}
	if humanFrame["type"] != "human_message" || humanFrame["message"] != "hello" {
		t.Fatalf("human frame = %#v", humanFrame)
	}
	run := responses[4].GetResult().GetRun()
	stored, err := configDB.GetProjectRun(ctx, run.GetRunId())
	if err != nil || stored.Status != domain.ProjectRunStatusSucceeded || stored.Output != "hello agent\n" {
		t.Fatalf("stored run = %#v err=%v", stored, err)
	}
	transcript, err := os.ReadFile(stored.LogsPath)
	if err != nil {
		t.Fatalf("read prompt attach transcript: %v", err)
	}
	if string(transcript) != "hello agent\n" {
		t.Fatalf("prompt attach transcript = %q", string(transcript))
	}
}

func TestRunsControllerRunProjectPromptAttachGatesQueuedTurnsAndOrdersTranscript(t *testing.T) {
	ctx := context.Background()
	controller, configDB, runtime := newTestRunAttachController(t, nil)
	interaction := newScriptedRunAttachInteraction()
	runtime.interactionOverride = interaction

	requests := make(chan *agentcomposev2.RunAttachRequest, 4)
	requests <- &agentcomposev2.RunAttachRequest{Frame: &agentcomposev2.RunAttachRequest_Start{Start: &agentcomposev2.RunAttachStart{
		Request: &agentcomposev2.RunAgentRequest{ProjectId: "project-1", AgentName: "worker", Prompt: "human-1"},
		Mode:    agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
	}}}
	requests <- humanMessageAttachRequest("human-2")
	requests <- humanMessageAttachRequest("human-3")
	requests <- &agentcomposev2.RunAttachRequest{Frame: &agentcomposev2.RunAttachRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}}}
	close(requests)

	var responses []*agentcomposev2.RunAttachResponse
	done := make(chan error, 1)
	go func() {
		done <- controller.RunProjectCommandAttach(ctx, func() (*agentcomposev2.RunAttachRequest, error) {
			req, ok := <-requests
			if !ok {
				return nil, io.EOF
			}
			return req, nil
		}, func(resp *agentcomposev2.RunAttachResponse) error {
			responses = append(responses, resp)
			return nil
		})
	}()

	assertPromptRuntimeFrame(t, receiveRuntimeInputFrame(t, interaction.sent), "start", "")
	assertPromptRuntimeFrame(t, receiveRuntimeInputFrame(t, interaction.sent), "human_message", "human-1")
	assertNoRuntimeInputFrame(t, interaction.sent)

	interaction.frames <- driverpkg.RuntimeOutputFrame{Type: driverpkg.RuntimeOutputStarted}
	interaction.frames <- promptRuntimeStdoutFrame(`{"v":1,"seq":0,"type":"started","provider":"codex","sessionId":"thread-1"}`)
	interaction.frames <- promptRuntimeStdoutFrame(`{"v":1,"seq":1,"type":"agent_event","event":{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"agent-1\n"}}}`)
	interaction.frames <- promptRuntimeStdoutFrame(`{"v":1,"seq":2,"type":"agent_turn_completed","provider":"codex","sessionId":"thread-1","finalText":"agent-1\n"}`)
	assertPromptRuntimeFrame(t, receiveRuntimeInputFrame(t, interaction.sent), "human_message", "human-2")
	assertNoRuntimeInputFrame(t, interaction.sent)

	interaction.frames <- promptRuntimeStdoutFrame(`{"v":1,"seq":3,"type":"agent_event","event":{"type":"item.completed","item":{"id":"m2","type":"agent_message","text":"agent-2\n"}}}`)
	interaction.frames <- promptRuntimeStdoutFrame(`{"v":1,"seq":4,"type":"agent_turn_completed","provider":"codex","sessionId":"thread-1","finalText":"agent-2\n"}`)
	assertPromptRuntimeFrame(t, receiveRuntimeInputFrame(t, interaction.sent), "human_message", "human-3")
	assertPromptRuntimeFrame(t, receiveRuntimeInputFrame(t, interaction.sent), "eof", "")

	interaction.frames <- promptRuntimeStdoutFrame(`{"v":1,"seq":5,"type":"agent_event","event":{"type":"item.completed","item":{"id":"m3","type":"agent_message","text":"agent-3\n"}}}`)
	interaction.frames <- promptRuntimeStdoutFrame(`{"v":1,"seq":6,"type":"agent_turn_completed","provider":"codex","sessionId":"thread-1","finalText":"agent-3\n"}`)
	interaction.frames <- promptRuntimeStdoutFrame(`{"v":1,"seq":7,"type":"result","provider":"codex","sessionId":"thread-1","stopReason":"eof","finalText":"agent-3\n","transcript":"agent-1\nagent-2\nagent-3\n"}`)
	interaction.frames <- driverpkg.RuntimeOutputFrame{Type: driverpkg.RuntimeOutputResult, Result: &driverpkg.RuntimeResult{OperationID: "run-attach", Success: true}}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunProjectCommandAttach returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunProjectCommandAttach did not finish")
	}

	var result *agentcomposev2.AttachResult
	for _, response := range responses {
		if response.GetResult() != nil {
			result = response.GetResult()
		}
	}
	if result == nil || result.GetRun() == nil {
		t.Fatalf("prompt attach responses have no result: %#v", responses)
	}
	stored, err := configDB.GetProjectRun(ctx, result.GetRun().GetRunId())
	if err != nil {
		t.Fatalf("get stored run: %v", err)
	}
	transcript, err := os.ReadFile(stored.LogsPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	const expected = "agent-1\nhuman-2\nagent-2\nhuman-3\nagent-3\n"
	if string(transcript) != expected {
		t.Fatalf("transcript = %q, want %q", string(transcript), expected)
	}
}

func promptRuntimeStdoutFrame(line string) driverpkg.RuntimeOutputFrame {
	return driverpkg.RuntimeOutputFrame{Type: driverpkg.RuntimeOutputStdout, Data: []byte(line + "\n")}
}

func TestPromptAttachProjectorLogsResultFinalTextWithoutAgentEventText(t *testing.T) {
	logsPath := filepath.Join(t.TempDir(), "transcript.txt")
	projector := newPromptAttachProjector(domain.ProjectRunRecord{RunID: "run-final"}, &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-final"}}, logsPath, nil)
	_, transition, err := projector.Project([]byte(`{"type":"result","finalText":"final only\n","stopReason":"eof"}` + "\n"))
	if err != nil {
		t.Fatalf("project final text result: %v", err)
	}
	if transition == nil || transition.Output != "final only\n" {
		t.Fatalf("transition = %#v", transition)
	}
	transcript, err := os.ReadFile(logsPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if string(transcript) != "final only\n" {
		t.Fatalf("transcript = %q", string(transcript))
	}
}

func TestPromptAttachProjectorLogsHumanMessagesAndTurnFinalText(t *testing.T) {
	logsPath := filepath.Join(t.TempDir(), "transcript.txt")
	hub := NewRunLogHub()
	sub := hub.Subscribe("run-follow")
	defer sub.Close()
	projector := newPromptAttachProjector(domain.ProjectRunRecord{RunID: "run-follow"}, &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-follow"}}, logsPath, hub)
	if _, _, err := projector.Project([]byte(`{"type":"agent_event","event":{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"first answer\n"}}}` + "\n")); err != nil {
		t.Fatalf("project first answer: %v", err)
	}
	if err := projector.AppendHumanMessage("next question"); err != nil {
		t.Fatalf("append human message: %v", err)
	}
	if _, _, err := projector.Project([]byte(`{"type":"agent_turn_completed","finalText":"first answer\n"}` + "\n")); err != nil {
		t.Fatalf("project first turn completion: %v", err)
	}
	transcript, err := os.ReadFile(logsPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if string(transcript) != "first answer\nnext question\n" {
		t.Fatalf("transcript = %q", string(transcript))
	}
	firstEvent := receiveProjectorRunLogEvent(t, sub)
	secondEvent := receiveProjectorRunLogEvent(t, sub)
	if firstEvent.Data != "first answer\n" || secondEvent.Data != "next question\n" {
		t.Fatalf("run log events = %#v / %#v", firstEvent, secondEvent)
	}
}

func TestPromptAttachProjectorLogsTurnFinalTextWithoutAgentEventText(t *testing.T) {
	logsPath := filepath.Join(t.TempDir(), "transcript.txt")
	projector := newPromptAttachProjector(domain.ProjectRunRecord{RunID: "run-turn-final"}, &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-turn-final"}}, logsPath, nil)
	if _, _, err := projector.Project([]byte(`{"type":"agent_turn_completed","finalText":"turn only\n"}` + "\n")); err != nil {
		t.Fatalf("project turn final text: %v", err)
	}
	transcript, err := os.ReadFile(logsPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if string(transcript) != "turn only\n" {
		t.Fatalf("transcript = %q", string(transcript))
	}
}

func TestPromptAttachProjectorSeparatesHumanMessageAfterUnterminatedAgentText(t *testing.T) {
	logsPath := filepath.Join(t.TempDir(), "transcript.txt")
	projector := newPromptAttachProjector(domain.ProjectRunRecord{RunID: "run-boundary"}, &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-boundary"}}, logsPath, nil)
	if _, _, err := projector.Project([]byte(`{"type":"agent_event","event":{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"first answer"}}}` + "\n")); err != nil {
		t.Fatalf("project agent text: %v", err)
	}
	if err := projector.AppendHumanMessage("next question"); err != nil {
		t.Fatalf("append human message: %v", err)
	}
	transcript, err := os.ReadFile(logsPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if string(transcript) != "first answer\nnext question\n" {
		t.Fatalf("transcript = %q", string(transcript))
	}
}

func TestPromptAttachProjectorDoesNotDuplicateSeparatorsBetweenQueuedHumanMessages(t *testing.T) {
	logsPath := filepath.Join(t.TempDir(), "transcript.txt")
	projector := newPromptAttachProjector(domain.ProjectRunRecord{RunID: "run-human-tail"}, &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-human-tail"}}, logsPath, nil)
	if _, _, err := projector.Project([]byte(`{"type":"agent_event","event":{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"agent"}}}` + "\n")); err != nil {
		t.Fatalf("project agent text: %v", err)
	}
	if err := projector.AppendHumanMessage("human-2"); err != nil {
		t.Fatalf("append first human message: %v", err)
	}
	if err := projector.AppendHumanMessage("  human-3  "); err != nil {
		t.Fatalf("append second human message: %v", err)
	}
	transcript, err := os.ReadFile(logsPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if string(transcript) != "agent\nhuman-2\n  human-3  \n" {
		t.Fatalf("transcript = %q", string(transcript))
	}
}

func TestPromptAttachProjectorSeparatesHumanMessageFromStderrTail(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
	}{
		{name: "terminated", stderr: "warning\n"},
		{name: "unterminated", stderr: "warning"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logsPath := filepath.Join(t.TempDir(), "transcript.txt")
			projector := newPromptAttachProjector(domain.ProjectRunRecord{RunID: "run-stderr-tail"}, &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-stderr-tail"}}, logsPath, nil)
			if err := projector.AppendStderr(test.stderr); err != nil {
				t.Fatalf("append stderr: %v", err)
			}
			if err := projector.AppendHumanMessage("next"); err != nil {
				t.Fatalf("append human message: %v", err)
			}
			transcript, err := os.ReadFile(logsPath)
			if err != nil {
				t.Fatalf("read transcript: %v", err)
			}
			if string(transcript) != "warning\nnext\n" {
				t.Fatalf("transcript = %q", string(transcript))
			}
		})
	}
}

func TestPromptAttachProjectorPersistsEachFrameIdempotently(t *testing.T) {
	store := &projectorEventStore{keys: map[string]struct{}{}}
	projector := newPersistentPromptAttachProjector(context.Background(), domain.ProjectRunRecord{RunID: "run-events", AgentName: "worker"}, &domain.Sandbox{}, filepath.Join(t.TempDir(), "transcript.txt"), nil, store)
	if err := projector.AppendHumanMessageFrame("question", "client-frame-1"); err != nil {
		t.Fatalf("append human frame: %v", err)
	}
	if err := projector.AppendHumanMessageFrame("question", "client-frame-1"); err != nil {
		t.Fatalf("retry human frame: %v", err)
	}
	turn := []byte(`{"seq":42,"type":"agent_turn_completed","provider":"codex","finalText":"answer","stopReason":"end_turn"}` + "\n")
	if _, _, err := projector.Project(turn); err != nil {
		t.Fatalf("project assistant turn: %v", err)
	}
	if _, _, err := projector.Project(turn); err != nil {
		t.Fatalf("retry assistant turn: %v", err)
	}
	_, transition, err := projector.Project([]byte(`{"seq":43,"type":"result","finalText":"answer","stopReason":"end_turn"}` + "\n"))
	if err != nil || transition == nil || !transition.SkipTerminalAgentEvent {
		t.Fatalf("result transition = %#v err=%v", transition, err)
	}
	if len(store.events) != 2 {
		t.Fatalf("persisted events = %#v", store.events)
	}
	if store.events[0].Kind != domain.ProjectRunEventKindUserMessage || store.events[0].ID != attachedHumanEventID("run-events", "client-frame-1", 1, "question") {
		t.Fatalf("human event = %#v", store.events[0])
	}
	if store.events[1].Kind != domain.ProjectRunEventKindAgentMessage || store.events[1].ID != attachedAgentEventID("run-events", 42, turn) || store.events[1].Text != "answer" {
		t.Fatalf("assistant event = %#v", store.events[1])
	}
}

func TestPromptAttachProjectorDoesNotSkipTerminalAgentEventAfterOnlyHumanMessage(t *testing.T) {
	store := &projectorEventStore{keys: map[string]struct{}{}}
	projector := newPersistentPromptAttachProjector(context.Background(), domain.ProjectRunRecord{RunID: "run-result-only", AgentName: "worker"}, &domain.Sandbox{}, filepath.Join(t.TempDir(), "transcript.txt"), nil, store)
	if err := projector.AppendHumanMessageFrame("question", "client-frame-1"); err != nil {
		t.Fatalf("append human frame: %v", err)
	}
	_, transition, err := projector.Project([]byte(`{"seq":43,"type":"result","finalText":"answer","stopReason":"end_turn"}` + "\n"))
	if err != nil {
		t.Fatalf("project result: %v", err)
	}
	if transition == nil || transition.SkipTerminalAgentEvent {
		t.Fatalf("result transition = %#v", transition)
	}
	if len(store.events) != 1 || store.events[0].Kind != domain.ProjectRunEventKindUserMessage {
		t.Fatalf("persisted events = %#v", store.events)
	}
}

func TestIntegrationPromptAttachProjectorPersistsAssistantTurnBeforeSkippingTerminalEvent(t *testing.T) {
	store := &projectorEventStore{keys: map[string]struct{}{}}
	projector := newPersistentPromptAttachProjector(context.Background(), domain.ProjectRunRecord{RunID: "run-integration-events", AgentName: "worker"}, &domain.Sandbox{}, filepath.Join(t.TempDir(), "transcript.txt"), nil, store)
	if err := projector.AppendHumanMessageFrame("question", "client-frame-1"); err != nil {
		t.Fatalf("append human frame: %v", err)
	}
	_, transition, err := projector.Project([]byte(`{"seq":43,"type":"result","finalText":"answer","stopReason":"end_turn"}` + "\n"))
	if err != nil {
		t.Fatalf("project result without assistant turn: %v", err)
	}
	if transition == nil || transition.SkipTerminalAgentEvent {
		t.Fatalf("result-only transition = %#v", transition)
	}
	turn := []byte(`{"seq":44,"type":"agent_turn_completed","provider":"codex","finalText":"answer","stopReason":"end_turn"}` + "\n")
	if _, _, err := projector.Project(turn); err != nil {
		t.Fatalf("project assistant turn: %v", err)
	}
	_, transition, err = projector.Project([]byte(`{"seq":45,"type":"result","finalText":"answer","stopReason":"end_turn"}` + "\n"))
	if err != nil {
		t.Fatalf("project result after assistant turn: %v", err)
	}
	if transition == nil || !transition.SkipTerminalAgentEvent {
		t.Fatalf("assistant transition = %#v", transition)
	}
	if len(store.events) != 2 || store.events[0].Kind != domain.ProjectRunEventKindUserMessage || store.events[1].Kind != domain.ProjectRunEventKindAgentMessage {
		t.Fatalf("persisted events = %#v", store.events)
	}
}

type projectorEventStore struct {
	keys   map[string]struct{}
	events []domain.ProjectRunEventRecord
}

func (s *projectorEventStore) AppendProjectRunEvent(_ context.Context, event domain.ProjectRunEventRecord) (domain.ProjectRunEventRecord, bool, error) {
	if _, exists := s.keys[event.ID]; exists {
		return event, false, nil
	}
	s.keys[event.ID] = struct{}{}
	s.events = append(s.events, event)
	return event, true, nil
}

func (s *projectorEventStore) AppendProjectRunEvents(ctx context.Context, events []domain.ProjectRunEventRecord) ([]domain.ProjectRunEventRecord, []bool, error) {
	created := make([]bool, 0, len(events))
	for _, event := range events {
		_, wasCreated, err := s.AppendProjectRunEvent(ctx, event)
		if err != nil {
			return nil, nil, err
		}
		created = append(created, wasCreated)
	}
	return events, created, nil
}

func receiveProjectorRunLogEvent(t *testing.T, sub *RunLogSubscription) RunLogEvent {
	t.Helper()
	select {
	case event := <-sub.C():
		return event
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for run log event")
	}
	return RunLogEvent{}
}

func TestRunsControllerRunProjectPromptAttachUnsupportedProvidersDoNotOpenRuntime(t *testing.T) {
	for _, provider := range []string{"claude", "gemini", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			ctx := context.Background()
			controller, configDB, runtime := newTestRunAttachController(t, nil)
			configDB.agent.Provider = provider
			requests := []*agentcomposev2.RunAttachRequest{{
				Frame: &agentcomposev2.RunAttachRequest_Start{Start: &agentcomposev2.RunAttachStart{
					Request: &agentcomposev2.RunAgentRequest{ProjectId: "project-1", AgentName: "worker", Prompt: "hello"},
					Mode:    agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
				}},
			}}
			var responses []*agentcomposev2.RunAttachResponse
			err := controller.RunProjectCommandAttach(ctx, recvRunAttachRequests(requests), func(resp *agentcomposev2.RunAttachResponse) error {
				responses = append(responses, resp)
				return nil
			})
			if err != nil {
				t.Fatalf("RunProjectCommandAttach prompt mode returned error: %v", err)
			}
			if runtime.interaction != nil {
				t.Fatalf("runtime interaction opened for unsupported provider %s: %#v", provider, runtime.interaction)
			}
			if len(responses) != 1 || responses[0].GetResult() == nil || responses[0].GetResult().GetSuccess() {
				t.Fatalf("prompt attach unsupported provider responses = %#v", responses)
			}
			if got := responses[0].GetResult().GetError(); !strings.Contains(got, "prompt attach currently supports codex provider only") {
				t.Fatalf("prompt attach unsupported provider error = %q", got)
			}
			run := responses[0].GetResult().GetRun()
			if run.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_FAILED {
				t.Fatalf("prompt attach unsupported provider run = %#v", run)
			}
		})
	}
}

func TestRunsControllerExecuteProjectRunCommandEdgeBranches(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:           root,
		SandboxRoot:        filepath.Join(root, "sandboxes"),
		RuntimeDriver:      driverpkg.RuntimeDriverDocker,
		DefaultImage:       "guest:latest",
		DockerDefaultImage: "guest:latest",
		GuestStateRoot:     "/guest/state",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "command session", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	run := domain.ProjectRunRecord{RunID: "run-edge", ProjectID: "project-1", AgentName: "worker"}
	req := RunAgentRequest{Env: []*agentcomposev2.EnvVarSpec{{Name: "REQUEST_ENV", Value: "yes"}}}

	transition, err := (&Controller{config: config}).executeProjectRunCommand(ctx, run, session, req, "echo edge", nil)
	if err == nil || transition.ExitCode != 1 || !strings.Contains(transition.Error, "dependencies are required") {
		t.Fatalf("nil deps transition=%#v err=%v", transition, err)
	}

	controller := &Controller{config: config, store: store, runtime: func(*domain.Sandbox) (Runtime, error) {
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

	missingVMSandbox := *session
	missingVMSandbox.Summary.ID = "missing-vm"
	missingVMSandbox.Summary.WorkspacePath = filepath.Join(root, "missing-vm", "workspace")
	transition, err = controller.executeProjectRunCommand(ctx, run, &missingVMSandbox, req, "echo edge", nil)
	if err == nil || transition.ExitCode != 1 || !strings.Contains(transition.Error, "no such file") {
		t.Fatalf("missing vm transition=%#v err=%v", transition, err)
	}
	if err := store.SaveVMState(session.Summary.ID, domain.VMState{Driver: driverpkg.RuntimeDriverDocker, BoxID: "box-1"}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}

	controller.runtime = func(*domain.Sandbox) (Runtime, error) {
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
	controller.runtime = func(*domain.Sandbox) (Runtime, error) {
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
	controller.runtime = func(*domain.Sandbox) (Runtime, error) {
		return &fakeControllerRuntime{execErr: errors.New("exec failed")}, nil
	}
	transition, err = controller.executeProjectRunCommand(ctx, run, session, req, "echo edge", nil)
	if err == nil || transition.ExitCode == 0 || !strings.Contains(transition.Error, "exec failed") {
		t.Fatalf("exec error transition=%#v err=%v", transition, err)
	}

	run.RunID = "run-parse"
	rawResult := domain.ExecResult{Stdout: "plain output", Output: "plain output", ExitCode: 0, Success: true}
	controller.runtime = func(*domain.Sandbox) (Runtime, error) {
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
		DataRoot:           root,
		SandboxRoot:        filepath.Join(root, "sandboxes"),
		RuntimeDriver:      driverpkg.RuntimeDriverDocker,
		DefaultImage:       "guest:latest",
		DockerDefaultImage: "guest:latest",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	configDB := &fakeControllerStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 1},
		projectAgent: domain.ProjectAgentRecord{
			ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Driver: driverpkg.RuntimeDriverDocker, Image: "guest:latest",
		},
		managed: ManagedAgentDefinition{
			ID: "agent-1", Enabled: true, Driver: driverpkg.RuntimeDriverDocker, GuestImage: "guest:latest", ManagedProjectID: "project-1", ManagedAgentName: "worker",
		},
		revision: domain.ProjectRevisionRecord{ProjectID: "project-1", Revision: 1, SpecJSON: `{"agents":[{"name":"worker"}]}`},
		agent:    domain.AgentDefinition{ID: "agent-1", Provider: "codex"},
		runs:     map[string]domain.ProjectRunRecord{},
	}
	driver := &fakeControllerDriver{store: store}
	executor := &fakeControllerExecutor{}
	dashboard := &fakeControllerDashboard{}
	controller := NewController(ControllerDependencies{
		Config:           config,
		Store:            store,
		ConfigDB:         configDB,
		WorkspaceEnsurer: &controllerWorkspaceEnsurer{},
		Driver:           driver,
		Executor:         executor,
		Images:           fakeControllerImages{},
		LoaderEngine:     &loaders.QJSLoaderEngine{},
		Dashboard:        dashboard,
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
		CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION,
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
	t.Run("success removes created sandbox", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, nil)
		if err != nil || execErr != nil {
			t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
		}
		if run.Status != domain.ProjectRunStatusSucceeded || run.SandboxID == "" || run.CleanupError != "" {
			t.Fatalf("run = %#v", run)
		}
		if _, statErr := os.Stat(fixture.store.SandboxDir(run.SandboxID)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("created sandbox dir still exists or stat error mismatch: %v", statErr)
		}
		if !fixture.driver.stopped || !fixture.driver.removed || !containsString(fixture.dashboard.reasons, "sandbox_removed") {
			t.Fatalf("driver=%#v dashboard=%#v", fixture.driver, fixture.dashboard.reasons)
		}
	})

	t.Run("agent failure removes created sandbox and preserves original error", func(t *testing.T) {
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
		if _, statErr := os.Stat(fixture.store.SandboxDir(run.SandboxID)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("created sandbox dir still exists or stat error mismatch: %v", statErr)
		}
	})

	t.Run("context cancel marks canceled and removes created sandbox", func(t *testing.T) {
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
		if _, statErr := os.Stat(fixture.store.SandboxDir(run.SandboxID)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("created sandbox dir still exists or stat error mismatch: %v", statErr)
		}
	})

	t.Run("existing sandbox is stopped but not removed", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		session, err := fixture.store.CreateSandbox(fixture.ctx, "existing", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
		if err != nil {
			t.Fatalf("CreateSandbox returned error: %v", err)
		}
		run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, func(req *RunAgentRequest) {
			req.SandboxID = session.Summary.ID
		})
		if err != nil || execErr != nil {
			t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
		}
		loaded, err := fixture.store.GetSandbox(fixture.ctx, session.Summary.ID)
		if err != nil {
			t.Fatalf("existing sandbox was removed: %v", err)
		}
		if run.SandboxID != session.Summary.ID || loaded.Summary.VMStatus != domain.VMStatusStopped {
			t.Fatalf("run=%#v loaded session=%#v", run, loaded.Summary)
		}
	})

	t.Run("driver cannot be combined with existing sandbox", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, func(req *RunAgentRequest) {
			req.SandboxID = "existing"
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
		if _, statErr := os.Stat(fixture.store.SandboxDir(run.SandboxID)); statErr != nil {
			t.Fatalf("sandbox dir should remain when cleanup fails: %v", statErr)
		}
	})

	t.Run("runtime remove failure keeps sandbox metadata", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		fixture.driver.removeErr = errors.New("runtime remove failed")
		run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, nil)
		if err != nil || execErr != nil {
			t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
		}
		if !strings.Contains(run.CleanupError, "runtime remove failed") {
			t.Fatalf("cleanup error = %q", run.CleanupError)
		}
		if _, statErr := os.Stat(fixture.store.SandboxDir(run.SandboxID)); statErr != nil {
			t.Fatalf("sandbox dir should remain when runtime removal fails: %v", statErr)
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

	t.Run("session start failure cleans created sandbox", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		fixture.driver.startErr = errors.New("start failed")
		run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, nil)
		if err != nil || execErr == nil || !strings.Contains(execErr.Error(), "start failed") {
			t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
		}
		if run.Status != domain.ProjectRunStatusFailed || run.SandboxID == "" || run.CleanupError != "" {
			t.Fatalf("run = %#v", run)
		}
		if _, statErr := os.Stat(fixture.store.SandboxDir(run.SandboxID)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("created sandbox dir still exists or stat error mismatch: %v", statErr)
		}
	})
}

func TestIntegrationRunsControllerRemoveOnCompletionWorkflows(t *testing.T) {
	t.Run("cleanup", TestRunsControllerRunProjectAgentRemoveOnCompletionCleanup)
	t.Run("cleanup error recording", TestRunsControllerRunProjectAgentCleanupErrorRecording)
}

func TestE2ERunsControllerRemoveOnCompletionWorkflows(t *testing.T) {
	TestIntegrationRunsControllerRemoveOnCompletionWorkflows(t)
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
		Enabled:         true,
		TriggerCount:    1,
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
			Script:   `scheduler.interval("trigger-1", async function() { return scheduler.agent("resolved prompt", { sandboxEnv: [{ name: "TRIGGER_ENV", value: "yes" }] }); }, 1000);`,
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
	session, err := fixture.store.GetSandbox(fixture.ctx, run.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	if fixture.executor.request.Message != "resolved prompt" || envItemValue(session.EnvItems, "TRIGGER_ENV") != "yes" {
		t.Fatalf("executor request = %#v", fixture.executor.request)
	}
	if len(run.Warnings) != 1 || !strings.Contains(run.Warnings[0], "trigger trigger-1 is disabled") {
		t.Fatalf("warnings = %#v", run.Warnings)
	}
}

func TestRunsControllerRunProjectAgentManualTriggerPayload(t *testing.T) {
	fixture := newControllerRunFixture(t)
	trigger := domain.LoaderTrigger{
		LoaderID:   "loader-1",
		ID:         "trigger-1",
		Kind:       domain.LoaderTriggerKindInterval,
		IntervalMs: 1000,
		Enabled:    true,
		SpecJSON:   `{"kind":"interval","intervalMs":1000}`,
	}
	fixture.configDB.schedulers = []domain.ProjectSchedulerRecord{{
		ProjectID:       "project-1",
		SchedulerID:     "scheduler-1",
		AgentName:       "worker",
		ManagedLoaderID: "loader-1",
		Enabled:         true,
		TriggerCount:    1,
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
			Script:   `scheduler.interval("trigger-1", async function(payload) { return scheduler.agent("review " + payload.topic, { sandboxEnv: [{ name: "TRIGGER_TOPIC", value: payload.topic }] }); }, 1000);`,
			Triggers: []domain.LoaderTrigger{trigger},
		},
	}
	run, execErr, err := fixture.controller.RunProjectAgent(fixture.ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Source:          domain.ProjectRunSourceManual,
		TriggerID:       "trigger-1",
		PayloadJSON:     `{"topic":"nightly"}`,
		ClientRequestID: "manual-trigger-payload",
	}, nil)
	if err != nil || execErr != nil {
		t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
	}
	session, err := fixture.store.GetSandbox(fixture.ctx, run.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	if run.Prompt != "review nightly" || fixture.executor.request.Message != "review nightly" || envItemValue(session.EnvItems, "TRIGGER_TOPIC") != "nightly" {
		t.Fatalf("run=%#v executor=%#v env=%#v", run, fixture.executor.request, session.EnvItems)
	}
}

func TestRunsControllerRunProjectAgentManualTriggerPromptOverride(t *testing.T) {
	fixture := newControllerRunFixture(t)
	trigger := domain.LoaderTrigger{
		LoaderID:   "loader-1",
		ID:         "trigger-1",
		Kind:       domain.LoaderTriggerKindInterval,
		IntervalMs: 1000,
		Enabled:    true,
		SpecJSON:   `{"kind":"interval","intervalMs":1000}`,
	}
	fixture.configDB.schedulers = []domain.ProjectSchedulerRecord{{
		ProjectID:       "project-1",
		SchedulerID:     "scheduler-1",
		AgentName:       "worker",
		ManagedLoaderID: "loader-1",
		Enabled:         true,
		TriggerCount:    1,
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
			Script:   `scheduler.interval("trigger-1", async function() { return scheduler.agent("captured prompt", { sandboxEnv: [{ name: "TRIGGER_ENV", value: "yes" }] }); }, 1000);`,
			Triggers: []domain.LoaderTrigger{trigger},
		},
	}
	run, execErr, err := fixture.controller.RunProjectAgent(fixture.ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Prompt:          "override prompt",
		Source:          domain.ProjectRunSourceManual,
		TriggerID:       "trigger-1",
		ClientRequestID: "manual-trigger-prompt",
	}, nil)
	if err != nil || execErr != nil {
		t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
	}
	session, err := fixture.store.GetSandbox(fixture.ctx, run.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	if run.Prompt != "override prompt" || fixture.executor.request.Message != "override prompt" || envItemValue(session.EnvItems, "TRIGGER_ENV") != "yes" {
		t.Fatalf("run=%#v executor=%#v env=%#v", run, fixture.executor.request, session.EnvItems)
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

func TestRunsControllerStickyBindingsAreScopedByTrigger(t *testing.T) {
	fixture := newControllerRunFixture(t)
	runSticky := func(triggerID, requestID string) domain.ProjectRunRecord {
		t.Helper()
		run, execErr, err := fixture.controller.RunProjectAgent(fixture.ctx, RunAgentRequest{
			ProjectID:              "project-1",
			AgentName:              "worker",
			Prompt:                 "do sticky work",
			Source:                 domain.ProjectRunSourceScheduler,
			SchedulerID:            "scheduler-1",
			TriggerID:              triggerID,
			ClientRequestID:        requestID,
			CleanupPolicy:          agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
			StickyBindingLoaderID:  "loader-1",
			StickyBindingTriggerID: triggerID,
		}, nil)
		if err != nil || execErr != nil {
			t.Fatalf("RunProjectAgent(%s) err=%v execErr=%v run=%#v", triggerID, err, execErr, run)
		}
		return run
	}

	first := runSticky("trigger-a", "sticky-a-1")
	second := runSticky("trigger-a", "sticky-a-2")
	other := runSticky("trigger-b", "sticky-b-1")
	if first.SandboxID == "" || second.SandboxID != first.SandboxID {
		t.Fatalf("same trigger sandbox ids = %q, %q", first.SandboxID, second.SandboxID)
	}
	if other.SandboxID == first.SandboxID {
		t.Fatalf("different triggers shared sandbox %q", other.SandboxID)
	}
	if fixture.configDB.bindings["loader-1/trigger-a"].SandboxID != first.SandboxID || fixture.configDB.bindings["loader-1/trigger-b"].SandboxID != other.SandboxID {
		t.Fatalf("bindings = %#v", fixture.configDB.bindings)
	}

	fixture.configDB.bindings["loader-1/stale"] = domain.LoaderBinding{LoaderID: "loader-1", TriggerID: "stale", SandboxID: "missing-sandbox"}
	replacement := runSticky("stale", "sticky-stale-1")
	if replacement.SandboxID == "missing-sandbox" || fixture.configDB.bindings["loader-1/stale"].SandboxID != replacement.SandboxID {
		t.Fatalf("stale replacement run=%#v bindings=%#v", replacement, fixture.configDB.bindings)
	}
	if len(replacement.Warnings) == 0 || !strings.Contains(replacement.Warnings[0], "unavailable") {
		t.Fatalf("stale replacement warnings = %#v", replacement.Warnings)
	}
}

func TestRunsControllerRejectsUncompiledScheduledSandboxBeforePersistence(t *testing.T) {
	for _, runtimeDriver := range []string{driverpkg.RuntimeDriverBoxlite, driverpkg.RuntimeDriverMicrosandbox} {
		t.Run(runtimeDriver, func(t *testing.T) {
			rawErr := driverpkg.ValidateCompiledRuntimeDriver(runtimeDriver)
			if rawErr == nil {
				t.Skipf("runtime driver %s is compiled in this build", runtimeDriver)
			}
			t.Run("new sticky sandbox", func(t *testing.T) {
				fixture := newControllerRunFixture(t)
				volumeResolver := &fakeVolumeResolver{}
				fixture.controller.volumes = volumeResolver
				bindingKey := "loader-uncompiled/trigger-uncompiled"
				originalBinding := domain.LoaderBinding{LoaderID: "loader-uncompiled", TriggerID: "trigger-uncompiled", SandboxID: "missing-original-sandbox"}
				fixture.configDB.bindings = map[string]domain.LoaderBinding{bindingKey: originalBinding}
				beforeSandboxes, err := fixture.store.ListSandboxes(fixture.ctx, domain.SandboxListOptions{})
				if err != nil {
					t.Fatalf("ListSandboxes before ensure returned error: %v", err)
				}
				beforeArtifacts := snapshotRunSandboxTree(t, fixture.config.SandboxRoot)

				result, err := fixture.controller.ensureProjectRunSandbox(fixture.ctx, domain.ProjectRunRecord{
					RunID: "run-uncompiled", ProjectID: "project-1", ProjectName: "Project", AgentName: "worker", Driver: runtimeDriver, ImageRef: "guest:latest",
				}, Preparation{Volumes: []domain.VolumeMountSpec{{Type: domain.VolumeMountTypeBind, Source: t.TempDir(), Target: "/blocked"}}}, RunAgentRequest{
					StickyBindingLoaderID: "loader-uncompiled", StickyBindingTriggerID: "trigger-uncompiled",
				})
				assertRunsRuntimeNotCompiled(t, err, runtimeDriver)
				if result.Sandbox != nil || result.Created {
					t.Fatalf("unsupported ensure result = %#v, want no sandbox", result)
				}
				afterSandboxes, listErr := fixture.store.ListSandboxes(fixture.ctx, domain.SandboxListOptions{})
				if listErr != nil || len(afterSandboxes.Sandboxes) != len(beforeSandboxes.Sandboxes) {
					t.Fatalf("sandboxes changed: before=%d after=%d err=%v", len(beforeSandboxes.Sandboxes), len(afterSandboxes.Sandboxes), listErr)
				}
				if got := fixture.configDB.bindings[bindingKey]; got != originalBinding {
					t.Fatalf("binding changed: got=%#v want=%#v", got, originalBinding)
				}
				if len(volumeResolver.specs) != 0 || fixture.driver.started {
					t.Fatalf("unsupported ensure reached volume/runtime work: volumes=%#v driver=%#v", volumeResolver.specs, fixture.driver)
				}
				if afterArtifacts := snapshotRunSandboxTree(t, fixture.config.SandboxRoot); !reflect.DeepEqual(afterArtifacts, beforeArtifacts) {
					t.Fatalf("sandbox artifacts changed: before=%#v after=%#v", beforeArtifacts, afterArtifacts)
				}
			})

			t.Run("historical bound sandbox", func(t *testing.T) {
				fixture := newControllerRunFixture(t)
				volumeResolver := &fakeVolumeResolver{}
				fixture.controller.volumes = volumeResolver
				sandbox, err := fixture.store.CreateSandbox(fixture.ctx, "historical", "", runtimeDriver, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
				if err != nil {
					t.Fatalf("CreateSandbox historical returned error: %v", err)
				}
				sandbox.Summary.VMStatus = domain.VMStatusStopped
				sandbox.Summary.RuntimeRef = "original-runtime-ref"
				if err := fixture.store.UpdateSandbox(fixture.ctx, sandbox); err != nil {
					t.Fatalf("UpdateSandbox historical returned error: %v", err)
				}
				originalProxy := domain.ProxyState{Enabled: false, HostPort: 12345, GuestPort: 8888, ProxyPath: "/original"}
				if err := fixture.store.SaveProxyState(sandbox.Summary.ID, originalProxy); err != nil {
					t.Fatalf("SaveProxyState historical returned error: %v", err)
				}
				bindingKey := "loader-history/trigger-history"
				originalBinding := domain.LoaderBinding{LoaderID: "loader-history", TriggerID: "trigger-history", SandboxID: sandbox.Summary.ID}
				fixture.configDB.bindings = map[string]domain.LoaderBinding{bindingKey: originalBinding}
				beforeArtifacts := snapshotRunSandboxTree(t, fixture.config.SandboxRoot)
				beforeEvents, err := fixture.store.ListEvents(fixture.ctx, sandbox.Summary.ID)
				if err != nil {
					t.Fatalf("ListEvents before ensure returned error: %v", err)
				}

				result, err := fixture.controller.ensureProjectRunSandbox(fixture.ctx, domain.ProjectRunRecord{
					RunID: "run-history", ProjectID: "project-1", ProjectName: "Project", AgentName: "worker", Driver: runtimeDriver, ImageRef: "guest:latest",
				}, Preparation{}, RunAgentRequest{
					StickyBindingLoaderID: "loader-history", StickyBindingTriggerID: "trigger-history", Jupyter: &agentcomposev2.RunJupyterSpec{Enabled: true},
				})
				assertRunsRuntimeNotCompiled(t, err, runtimeDriver)
				if result.Sandbox == nil || result.Sandbox.Summary.ID != sandbox.Summary.ID || result.Created {
					t.Fatalf("unsupported historical result = %#v", result)
				}
				loaded, loadErr := fixture.store.GetSandbox(fixture.ctx, sandbox.Summary.ID)
				if loadErr != nil || loaded.Summary.Driver != runtimeDriver || loaded.Summary.VMStatus != domain.VMStatusStopped || loaded.Summary.RuntimeRef != "original-runtime-ref" {
					t.Fatalf("historical sandbox changed: sandbox=%#v err=%v", loaded, loadErr)
				}
				proxy, proxyErr := fixture.store.GetProxyState(sandbox.Summary.ID)
				if proxyErr != nil || !reflect.DeepEqual(proxy, originalProxy) {
					t.Fatalf("historical proxy changed: got=%#v err=%v want=%#v", proxy, proxyErr, originalProxy)
				}
				afterEvents, eventErr := fixture.store.ListEvents(fixture.ctx, sandbox.Summary.ID)
				if eventErr != nil || !reflect.DeepEqual(afterEvents, beforeEvents) {
					t.Fatalf("historical events changed: before=%#v after=%#v err=%v", beforeEvents, afterEvents, eventErr)
				}
				if got := fixture.configDB.bindings[bindingKey]; got != originalBinding {
					t.Fatalf("historical binding changed: got=%#v want=%#v", got, originalBinding)
				}
				if len(volumeResolver.specs) != 0 || fixture.driver.started {
					t.Fatalf("historical ensure reached volume/runtime work: volumes=%#v driver=%#v", volumeResolver.specs, fixture.driver)
				}
				if afterArtifacts := snapshotRunSandboxTree(t, fixture.config.SandboxRoot); !reflect.DeepEqual(afterArtifacts, beforeArtifacts) {
					t.Fatalf("historical artifacts changed: before=%#v after=%#v", beforeArtifacts, afterArtifacts)
				}
			})
		})
	}
}

func assertRunsRuntimeNotCompiled(t *testing.T, err error, runtimeDriver string) {
	t.Helper()
	if !errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) || !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("error = %v, want typed unsupported", err)
	}
	var notCompiled *driverpkg.RuntimeDriverNotCompiledError
	if !errors.As(err, &notCompiled) || notCompiled.Driver != runtimeDriver {
		t.Fatalf("typed error = %#v, want driver %q", notCompiled, runtimeDriver)
	}
}

func snapshotRunSandboxTree(t *testing.T, root string) []string {
	t.Helper()
	var snapshot []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot = append(snapshot, fmt.Sprintf("%s|%s|%d", rel, info.Mode(), info.Size()))
		return nil
	}); err != nil {
		t.Fatalf("snapshot sandbox root %s: %v", root, err)
	}
	return snapshot
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

	specs := envVarSpecsFromSandboxEnv([]domain.SandboxEnvVar{
		{Name: " A ", Value: "1", Secret: true},
		{Name: " ", Value: "ignored"},
		{Name: "B", Value: "2"},
	})
	if len(specs) != 2 || specs[0].Name != "A" || specs[0].Value != "1" || !specs[0].Secret || specs[1].Name != "B" {
		t.Fatalf("env specs = %#v", specs)
	}
}

func TestRunsControllerApplyJupyterOptionsToSandbox(t *testing.T) {
	fixture := newControllerRunFixture(t)
	fixture.config.JupyterGuestPort = 8888
	session, err := fixture.store.CreateSandbox(fixture.ctx, "jupyter session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	before, err := fixture.store.GetProxyState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState before returned error: %v", err)
	}
	if err := fixture.controller.applyJupyterOptionsToSandbox(session, sessionstore.CreateSandboxOptions{}); err != nil {
		t.Fatalf("apply empty options returned error: %v", err)
	}
	unchanged, err := fixture.store.GetProxyState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState unchanged returned error: %v", err)
	}
	if unchanged != before {
		t.Fatalf("empty options changed proxy state before=%#v after=%#v", before, unchanged)
	}
	if err := fixture.controller.applyJupyterOptionsToSandbox(session, sessionstore.CreateSandboxOptions{JupyterExpose: true, JupyterGuestPort: 9999}); err != nil {
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

func TestRunsControllerApplyJupyterOptionsLeavesDockerHostPortForRuntime(t *testing.T) {
	fixture := newControllerRunFixture(t)
	fixture.config.JupyterGuestPort = 8888
	sandbox, err := fixture.store.CreateSandbox(fixture.ctx, "docker jupyter", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	if err := fixture.controller.applyJupyterOptionsToSandbox(sandbox, sessionstore.CreateSandboxOptions{JupyterEnabled: true}); err != nil {
		t.Fatalf("applyJupyterOptionsToSandbox returned error: %v", err)
	}
	state, err := fixture.store.GetProxyState(sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState returned error: %v", err)
	}
	if !state.Enabled || state.GuestPort != 8888 || state.HostPort != 0 || state.Token == "" {
		t.Fatalf("docker proxy state = %+v, want enabled state with runtime-assigned host port", state)
	}
}

func TestRunsProjectRunLogAppendChunk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "runs", "run-1", "output.txt")
	if offset, err := appendProjectRunLogChunk(path, domain.ExecChunk{Text: "stdout\n"}); err != nil || offset != 7 {
		t.Fatalf("append stdout returned error: %v", err)
	}
	if offset, err := appendProjectRunLogChunk(path, domain.ExecChunk{Text: "stderr\n", Stream: domain.StdioStderr}); err != nil || offset != 14 {
		t.Fatalf("append stderr returned error: %v", err)
	}
	if _, err := appendProjectRunLogChunk(path, domain.ExecChunk{}); err != nil {
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

	baseJupyter := sessionstore.CreateSandboxOptions{JupyterGuestPort: 8888}
	if options, err := resolveRunJupyterOptions(baseJupyter, nil); err != nil ||
		options.JupyterEnabled != baseJupyter.JupyterEnabled ||
		options.JupyterExpose != baseJupyter.JupyterExpose ||
		options.JupyterGuestPort != baseJupyter.JupyterGuestPort ||
		len(options.VolumeMounts) != 0 {
		t.Fatalf("nil jupyter override options=%#v err=%v", options, err)
	}
	if _, err := resolveRunJupyterOptions(baseJupyter, &agentcomposev2.RunJupyterSpec{GuestPort: 65536}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid jupyter guest port err=%v", err)
	}
	options, err := resolveRunJupyterOptions(baseJupyter, &agentcomposev2.RunJupyterSpec{Enabled: true, GuestPort: 9000})
	if err != nil || !options.JupyterEnabled || options.JupyterExpose || options.JupyterGuestPort != 9000 {
		t.Fatalf("enabled jupyter options=%#v err=%v", options, err)
	}
	options, err = resolveRunJupyterOptions(sessionstore.CreateSandboxOptions{}, &agentcomposev2.RunJupyterSpec{Expose: true})
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
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-1", "workspace")}}
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
		t.Fatalf("nil sandbox cell output path = %q", got)
	}
	if got := projectRunAgentCellOutputPath(session, " "); got != "" {
		t.Fatalf("blank cell output path = %q", got)
	}
	if got := projectRunAgentCellOutputPath(session, " cell-1 "); !strings.Contains(got, filepath.Join("state", "cells", "cell-1", "output.txt")) {
		t.Fatalf("cell output path = %q", got)
	}
	if _, err := appendProjectRunLogChunk("", domain.ExecChunk{Text: "ignored"}); err != nil {
		t.Fatalf("blank append returned error: %v", err)
	}
	fileParent := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(fileParent, []byte("file"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if _, err := appendProjectRunLogChunk(filepath.Join(fileParent, "output.txt"), domain.ExecChunk{Text: "chunk"}); err == nil {
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

	session.Summary.Tags = []domain.SandboxTag{
		{Name: capabilities.CapsetTagName, Value: "dev"},
		{Name: capabilities.CapsetTagName, Value: "missing"},
	}
	provider := fakeCapabilityProvider{
		guides: map[string][]byte{"dev": []byte("Dev guide")},
		errs:   map[string]error{"missing": errors.New("missing guide")},
		target: "cap-proxy.internal:9000",
	}
	guideStore := &fakeGuideSandboxStore{}
	streams := sessions.NewStreamBrokerForTest()
	ch, unsubscribe := streams.Subscribe(session.Summary.ID)
	defer unsubscribe()
	writeCapabilityGuide(context.Background(), provider, guideStore, streams, session, capabilities.SandboxCapsets(session))
	guidePath := capabilities.SandboxGuidePath(session)
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

func envItemValue(items []domain.SandboxEnvVar, name string) string {
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

func (s *fakeRunStore) CreateProjectRunWithEvents(ctx context.Context, run domain.ProjectRunRecord, _ []domain.ProjectRunEventRecord) (domain.ProjectRunRecord, error) {
	created, err := s.CreateProjectRun(ctx, run)
	if err != nil {
		return s.GetProjectRun(ctx, run.RunID)
	}
	return created, nil
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

func (s *fakeRunStore) UpdateProjectRunWithEvents(ctx context.Context, run domain.ProjectRunRecord, _ []domain.ProjectRunEventRecord) (domain.ProjectRunRecord, error) {
	return s.UpdateProjectRun(ctx, run)
}

type fakePreparationStore struct {
	project        domain.ProjectRecord
	revision       domain.ProjectRevisionRecord
	agent          domain.AgentDefinition
	global         []domain.SandboxEnvVar
	projectVolumes map[string]domain.VolumeRecord
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

func (s *fakePreparationStore) ListGlobalEnv(context.Context) ([]domain.SandboxEnvVar, error) {
	return s.global, nil
}

func (s *fakePreparationStore) ListProjectVolumes(context.Context, string) (map[string]domain.VolumeRecord, error) {
	return s.projectVolumes, nil
}

type fakeProjectSandboxRunStore struct {
	runs []domain.ProjectRunRecord
}

func (s fakeProjectSandboxRunStore) ListProjectSandboxRuns(context.Context, domain.ProjectSandboxRelationFilter) ([]domain.ProjectRunRecord, error) {
	return s.runs, nil
}

type fakeSandboxStatusStore struct {
	sessions map[string]*domain.Sandbox
}

func (s fakeSandboxStatusStore) GetSandbox(_ context.Context, id string) (*domain.Sandbox, error) {
	session, ok := s.sessions[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	return session, nil
}

type fakeControllerStore struct {
	project        domain.ProjectRecord
	projectAgent   domain.ProjectAgentRecord
	managed        ManagedAgentDefinition
	revision       domain.ProjectRevisionRecord
	agent          domain.AgentDefinition
	global         []domain.SandboxEnvVar
	projectVolumes map[string]domain.VolumeRecord
	runs           map[string]domain.ProjectRunRecord
	schedulers     []domain.ProjectSchedulerRecord
	loaders        map[string]domain.Loader
	bindings       map[string]domain.LoaderBinding
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

func (s *fakeControllerStore) CreateProjectRunWithEvents(ctx context.Context, run domain.ProjectRunRecord, _ []domain.ProjectRunEventRecord) (domain.ProjectRunRecord, error) {
	created, err := s.CreateProjectRun(ctx, run)
	if err != nil {
		return s.GetProjectRun(ctx, run.RunID)
	}
	return created, nil
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

func (s *fakeControllerStore) UpdateProjectRunWithEvents(ctx context.Context, run domain.ProjectRunRecord, _ []domain.ProjectRunEventRecord) (domain.ProjectRunRecord, error) {
	return s.UpdateProjectRun(ctx, run)
}

func (s *fakeControllerStore) GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error) {
	return s.revision, nil
}

func (s *fakeControllerStore) GetAgentDefinition(context.Context, string) (domain.AgentDefinition, error) {
	return s.agent, nil
}

func (s *fakeControllerStore) ListGlobalEnv(context.Context) ([]domain.SandboxEnvVar, error) {
	return s.global, nil
}

func (s *fakeControllerStore) ListProjectVolumes(context.Context, string) (map[string]domain.VolumeRecord, error) {
	return s.projectVolumes, nil
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

func (s *fakeControllerStore) GetLoader(_ context.Context, loaderID string) (domain.Loader, error) {
	if s.loaders == nil {
		return domain.Loader{}, domain.ErrNotFound
	}
	loader, ok := s.loaders[loaderID]
	if !ok {
		return domain.Loader{}, domain.ErrNotFound
	}
	return loader, nil
}

func (s *fakeControllerStore) GetLoaderBinding(_ context.Context, loaderID, triggerID string) (domain.LoaderBinding, bool, error) {
	binding, ok := s.bindings[loaderID+"/"+triggerID]
	return binding, ok, nil
}

func (s *fakeControllerStore) UpsertLoaderBinding(_ context.Context, binding domain.LoaderBinding) error {
	if s.bindings == nil {
		s.bindings = map[string]domain.LoaderBinding{}
	}
	s.bindings[binding.LoaderID+"/"+binding.TriggerID] = binding
	return nil
}

type fakeControllerDriver struct {
	started   bool
	stopped   bool
	removed   bool
	startErr  error
	stopErr   error
	removeErr error
	store     *sessionstore.Store
}

func (d *fakeControllerDriver) StartSandboxVM(_ context.Context, session *domain.Sandbox) error {
	d.started = true
	if d.startErr != nil {
		return d.startErr
	}
	if d.store != nil {
		return d.store.SaveVMState(session.Summary.ID, domain.VMState{Driver: session.Summary.Driver, BoxID: "box-1"})
	}
	return nil
}

func (d *fakeControllerDriver) StopSandboxVM(context.Context, *domain.Sandbox) error {
	d.stopped = true
	if d.stopErr != nil {
		return d.stopErr
	}
	return nil
}

func (d *fakeControllerDriver) RemoveSandboxVM(context.Context, *domain.Sandbox) error {
	d.removed = true
	return d.removeErr
}

type fakeControllerExecutor struct {
	request execution.ExecuteAgentRequest
	cell    domain.NotebookCell
	execErr error
}

func (e *fakeControllerExecutor) ExecuteAgentRequest(_ context.Context, _ *domain.Sandbox, req execution.ExecuteAgentRequest) (domain.NotebookCell, domain.SandboxEvent, domain.SandboxEvent, error) {
	e.request = req
	if req.Stream.OnStart != nil {
		if err := req.Stream.OnStart(domain.NotebookCell{ID: "cell-1"}); err != nil {
			return domain.NotebookCell{}, domain.SandboxEvent{}, domain.SandboxEvent{}, err
		}
	}
	if req.Stream.OnChunk != nil {
		if err := req.Stream.OnChunk("cell-1", domain.ExecChunk{Text: "chunk"}); err != nil {
			return domain.NotebookCell{}, domain.SandboxEvent{}, domain.SandboxEvent{}, err
		}
	}
	cell := e.cell
	if strings.TrimSpace(cell.ID) == "" {
		cell = domain.NotebookCell{ID: "cell-1", Type: execution.CellTypeAgent, Output: "done", Success: true, ExitCode: 0}
	}
	return cell,
		domain.SandboxEvent{ID: "user", Type: "user", Message: req.Message},
		domain.SandboxEvent{ID: "assistant", Type: "assistant", Message: "done"},
		e.execErr
}

type fakeControllerRuntime struct {
	spec      domain.ExecSpec
	result    domain.RuntimeCommandResult
	rawResult *domain.ExecResult
	execErr   error
}

func (r *fakeControllerRuntime) ExecStream(_ context.Context, _ *domain.Sandbox, _ domain.VMState, spec domain.ExecSpec, writer domain.ExecStreamWriter) (domain.ExecResult, error) {
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

func newTestRunAttachController(t *testing.T, frames []driverpkg.RuntimeOutputFrame) (*Controller, *fakeControllerStore, *fakeRunAttachRuntime) {
	t.Helper()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:           root,
		SandboxRoot:        filepath.Join(root, "sessions"),
		RuntimeDriver:      driverpkg.RuntimeDriverDocker,
		DefaultImage:       "guest:latest",
		DockerDefaultImage: "guest:latest",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	configDB := &fakeControllerStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 1},
		projectAgent: domain.ProjectAgentRecord{
			ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Driver: driverpkg.RuntimeDriverDocker, Image: "guest:latest",
		},
		managed: ManagedAgentDefinition{
			ID: "agent-1", Enabled: true, Driver: driverpkg.RuntimeDriverDocker, GuestImage: "guest:latest", ManagedProjectID: "project-1", ManagedAgentName: "worker",
		},
		revision: domain.ProjectRevisionRecord{ProjectID: "project-1", Revision: 1, SpecJSON: `{"agents":[{"name":"worker"}]}`},
		agent:    domain.AgentDefinition{ID: "agent-1", Provider: "codex"},
		runs:     map[string]domain.ProjectRunRecord{},
	}
	runtime := &fakeRunAttachRuntime{frames: frames}
	controller := NewController(ControllerDependencies{
		Config:           config,
		Store:            store,
		ConfigDB:         configDB,
		WorkspaceEnsurer: &controllerWorkspaceEnsurer{},
		Driver:           &fakeControllerDriver{store: store},
		Runtime: func(*domain.Sandbox) (Runtime, error) {
			return runtime, nil
		},
		Images: fakeControllerImages{},
	})
	return controller, configDB, runtime
}

type fakeRunAttachRuntime struct {
	fakeControllerRuntime
	spec                driverpkg.RuntimeStartSpec
	frames              []driverpkg.RuntimeOutputFrame
	interaction         *fakeRunAttachInteraction
	interactionOverride driverpkg.RuntimeInteraction
}

func (r *fakeRunAttachRuntime) OpenInteraction(_ context.Context, _ *domain.Sandbox, _ domain.VMState, spec driverpkg.RuntimeStartSpec) (driverpkg.RuntimeInteraction, error) {
	r.spec = spec
	if r.interactionOverride != nil {
		return r.interactionOverride, nil
	}
	r.interaction = &fakeRunAttachInteraction{frames: append([]driverpkg.RuntimeOutputFrame(nil), r.frames...)}
	return r.interaction, nil
}

type scriptedRunAttachInteraction struct {
	frames    chan driverpkg.RuntimeOutputFrame
	sent      chan driverpkg.RuntimeInputFrame
	closed    chan struct{}
	closeOnce sync.Once
}

func newScriptedRunAttachInteraction() *scriptedRunAttachInteraction {
	return &scriptedRunAttachInteraction{
		frames: make(chan driverpkg.RuntimeOutputFrame, 16),
		sent:   make(chan driverpkg.RuntimeInputFrame, 8),
		closed: make(chan struct{}),
	}
}

func (i *scriptedRunAttachInteraction) Send(frame driverpkg.RuntimeInputFrame) error {
	i.sent <- frame
	return nil
}

func (i *scriptedRunAttachInteraction) CloseSend() error {
	i.closeOnce.Do(func() { close(i.closed) })
	return nil
}

func (i *scriptedRunAttachInteraction) Recv() (driverpkg.RuntimeOutputFrame, error) {
	frame, ok := <-i.frames
	if !ok {
		return driverpkg.RuntimeOutputFrame{}, io.EOF
	}
	return frame, nil
}

func (*scriptedRunAttachInteraction) Wait() (driverpkg.RuntimeResult, error) {
	return driverpkg.RuntimeResult{Success: true}, nil
}

type fakeRunAttachInteraction struct {
	frames []driverpkg.RuntimeOutputFrame
	sent   []driverpkg.RuntimeInputFrame
	closed bool
}

func (i *fakeRunAttachInteraction) Send(frame driverpkg.RuntimeInputFrame) error {
	i.sent = append(i.sent, frame)
	return nil
}

func (i *fakeRunAttachInteraction) CloseSend() error {
	i.closed = true
	return nil
}

func (i *fakeRunAttachInteraction) Recv() (driverpkg.RuntimeOutputFrame, error) {
	if len(i.frames) == 0 {
		return driverpkg.RuntimeOutputFrame{}, io.EOF
	}
	frame := i.frames[0]
	i.frames = i.frames[1:]
	return frame, nil
}

func (i *fakeRunAttachInteraction) Wait() (driverpkg.RuntimeResult, error) {
	return driverpkg.RuntimeResult{Success: true}, nil
}

func recvRunAttachRequests(requests []*agentcomposev2.RunAttachRequest) RunAttachReceiver {
	index := 0
	return func() (*agentcomposev2.RunAttachRequest, error) {
		if index >= len(requests) {
			return nil, io.EOF
		}
		req := requests[index]
		index++
		return req, nil
	}
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

type fakeVolumeResolver struct {
	specs    []domain.VolumeMountSpec
	options  volumes.ResolveOptions
	mounts   []domain.SandboxVolumeMount
	warnings []string
	err      error
}

func (r *fakeVolumeResolver) ResolveMounts(_ context.Context, specs []domain.VolumeMountSpec, options volumes.ResolveOptions) ([]domain.SandboxVolumeMount, []string, error) {
	r.specs = append([]domain.VolumeMountSpec(nil), specs...)
	r.options = options
	if r.err != nil {
		return nil, nil, r.err
	}
	return append([]domain.SandboxVolumeMount(nil), r.mounts...), append([]string(nil), r.warnings...), nil
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

type fakeGuideSandboxStore struct {
	events []domain.SandboxEvent
}

func (s *fakeGuideSandboxStore) CreateSandboxWithOptions(context.Context, string, string, string, string, string, string, *sessionstore.SandboxWorkspace, []sessionstore.SandboxEnvVar, []sessionstore.SandboxTag, sessionstore.CreateSandboxOptions) (*sessionstore.Sandbox, error) {
	return nil, errors.New("not implemented")
}

func (s *fakeGuideSandboxStore) GetSandbox(context.Context, string) (*sessionstore.Sandbox, error) {
	return nil, errors.New("not implemented")
}

func (s *fakeGuideSandboxStore) UpdateSandbox(context.Context, *sessionstore.Sandbox) error {
	return errors.New("not implemented")
}

func (s *fakeGuideSandboxStore) RemoveSandbox(context.Context, string) error {
	return errors.New("not implemented")
}

func (s *fakeGuideSandboxStore) AddEvent(_ context.Context, _ string, event sessionstore.SandboxEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *fakeGuideSandboxStore) GetVMState(string) (sessionstore.VMState, error) {
	return sessionstore.VMState{}, errors.New("not implemented")
}

func (s *fakeGuideSandboxStore) GetProxyState(string) (sessionstore.ProxyState, error) {
	return sessionstore.ProxyState{}, errors.New("not implemented")
}

func (s *fakeGuideSandboxStore) SaveProxyState(string, sessionstore.ProxyState) error {
	return errors.New("not implemented")
}

func (s *fakeGuideSandboxStore) AllocateHostPortForSandbox(string) (int, error) {
	return 0, errors.New("not implemented")
}
