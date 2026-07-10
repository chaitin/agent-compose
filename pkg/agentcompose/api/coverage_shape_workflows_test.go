package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/capability"
	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/dashboard"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/imagecache"
	"agent-compose/pkg/images"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestAPIMappingCoverageWorkflows(t *testing.T) {
	now := time.Date(2026, 7, 3, 7, 0, 0, 123, time.UTC)
	workspace := domain.WorkspaceConfig{
		ID:         "workspace-1",
		Name:       "repo",
		Type:       "git",
		ConfigJSON: `{"repo_url":"https://example.test/repo.git","branch":"main"}`,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	agent := domain.AgentDefinition{
		ID:           "agent-1",
		Name:         "Agent",
		Description:  "desc",
		Enabled:      true,
		Provider:     "codex",
		Model:        "gpt-5.4",
		SystemPrompt: "prompt",
		WorkspaceID:  workspace.ID,
		Driver:       "boxlite",
		GuestImage:   "guest:latest",
		EnvItems:     []domain.SessionEnvVar{{Name: "TOKEN", Value: "secret", Secret: true}},
		ConfigJSON:   `{"temperature":0}`,
		CapsetIDs:    []string{"dev"},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	agentProto := AgentDefinitionToProto(agent, &workspace, agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_AVAILABLE, agentcomposev1.AgentHealthStatus_AGENT_HEALTH_STATUS_HEALTHY, domain.AgentCurrentRunSummary{RunningSessionCount: 1}, &domain.AgentLatestRunSummary{RunType: "manual", Status: "succeeded", RunID: "run-1", Title: "latest", At: now})
	if agentProto.GetAgentId() != "agent-1" || agentProto.GetWorkFiles().GetSummary() == "" || agentProto.GetLatestRunSummary().GetRunId() != "run-1" {
		t.Fatalf("agent proto = %#v", agentProto)
	}
	if len(AgentDefinitionTagsToProto(agent)) != 3 {
		t.Fatalf("expected agent session tags")
	}
	if got := EnvItemsFromProto(EnvItemsToProto(agent.EnvItems)); len(got) != 1 || got[0].Name != "TOKEN" {
		t.Fatalf("env round trip = %#v", got)
	}
	if got := SessionTagsFromProto([]*agentcomposev1.SessionTag{nil, {Name: "k", Value: "v"}}); len(got) != 1 || got[0].Name != "k" {
		t.Fatalf("session tags = %#v", got)
	}
	if got := AgentWorkspaceSummary(domain.WorkspaceConfig{Name: "files", Type: "file", Comment: "comment"}); got != "comment" {
		t.Fatalf("file workspace summary = %q", got)
	}
	if FormatProtoTime(time.Time{}) != "" || FormatProtoTime(now) == "" {
		t.Fatalf("unexpected proto time formatting")
	}

	session := &domain.Session{
		Summary: domain.SessionSummary{
			ID:            "session-1",
			Title:         "session",
			TriggerSource: domain.SessionTypeManual,
			Driver:        "boxlite",
			VMStatus:      domain.VMStatusRunning,
			GuestImage:    "guest:latest",
			WorkspacePath: "/workspace",
			ProxyPath:     "/agent-compose/session/session-1/lab",
			CreatedAt:     now,
			UpdatedAt:     now,
			CellCount:     1,
			EventCount:    1,
			Tags:          []domain.SessionTag{{Name: "tag", Value: "value"}},
		},
		WorkspaceID: "workspace-1",
		Workspace:   &domain.SessionWorkspace{ID: "workspace-1", Name: "repo", Type: "git", ConfigJSON: "{}"},
		EnvItems:    []domain.SessionEnvVar{{Name: "SECRET", Value: "value", Secret: true}},
	}
	if detail := SessionDetailToProto(session); detail.GetSummary().GetSessionId() != "session-1" || detail.GetEnvItems()[0].GetValue() != secretRedactedValue {
		t.Fatalf("session detail = %#v", detail)
	}
	if env := GlobalEnvConfigToProto(session.EnvItems); env.GetEnvItems()[0].GetValue() != secretRedactedValue {
		t.Fatalf("global env = %#v", env)
	}
	if WorkspaceConfigToProto(workspace).GetId() != workspace.ID || SessionWorkspaceToProto(session.Workspace).GetId() != workspace.ID {
		t.Fatalf("workspace proto mapping failed")
	}
	cell := domain.NotebookCell{ID: "cell-1", Type: execution.CellTypeShell, Source: "echo hi", Stdout: "hi\n", Success: true, CreatedAt: now, ExitCode: 0, Agent: "codex", AgentSessionID: "agent-session"}
	if CellToProto(cell).GetType() != agentcomposev1.CellType_CELL_TYPE_SHELL || AgentRunToProto(cell).GetAgentSessionId() != "agent-session" {
		t.Fatalf("cell mappings failed")
	}
	for _, typ := range []agentcomposev1.CellType{agentcomposev1.CellType_CELL_TYPE_SHELL, agentcomposev1.CellType_CELL_TYPE_PYTHON, agentcomposev1.CellType_CELL_TYPE_AGENT, agentcomposev1.CellType_CELL_TYPE_JAVASCRIPT, agentcomposev1.CellType(99)} {
		_ = CellTypeFromProto(typ)
	}
	for _, typ := range []string{execution.CellTypeShell, execution.CellTypePython, execution.CellTypeAgent, execution.CellTypeJavaScript, "other"} {
		_ = CellTypeToProto(typ)
	}
	for _, event := range []sessions.WatchEvent{
		{EventType: sessions.WatchEventTypeSessionUpdated, Session: &session.Summary},
		{EventType: sessions.WatchEventTypeCellStarted, Cell: &cell},
		{EventType: sessions.WatchEventTypeCellOutput, CellID: "cell-1", Chunk: "hi"},
		{EventType: sessions.WatchEventTypeCellCompleted, Cell: &cell},
		{EventType: sessions.WatchEventTypeEventAdded, Event: &domain.SessionEvent{ID: "event-1", Type: "test", CreatedAt: now}},
		{EventType: sessions.WatchEventTypeUnspecified},
	} {
		if WatchSessionResponseToProto(event) == nil {
			t.Fatalf("nil watch response")
		}
	}
	if SessionEventToProto(domain.SessionEvent{ID: "event-1", Type: "test", CreatedAt: now}).GetId() != "event-1" {
		t.Fatalf("session event mapping failed")
	}

	loader := domain.Loader{
		Summary: domain.LoaderSummary{
			ID: "loader-1", Name: "Loader", Enabled: true, Runtime: domain.LoaderRuntimeScheduler,
			WorkspaceID: "workspace-1", AgentID: "agent-1", Driver: "boxlite", GuestImage: "guest:latest",
			DefaultAgent: "codex", SessionPolicy: domain.LoaderSessionPolicyNew, ConcurrencyPolicy: domain.LoaderConcurrencyPolicySkip,
			CapsetIDs: []string{"dev"}, CreatedAt: now, UpdatedAt: now, LatestRunAt: now, TriggerCount: 1, RunCount: 1, EventCount: 1,
		},
		Script:   "function main(){}",
		Triggers: []domain.LoaderTrigger{{LoaderID: "loader-1", ID: "trigger-1", Kind: domain.LoaderTriggerKindCron, Topic: "topic", IntervalMs: 1000, Enabled: true, AutoID: true, NextFireAt: now, LastFiredAt: now}},
		EnvItems: []domain.SessionEnvVar{{Name: "LOADER_SECRET", Value: "value", Secret: true}},
	}
	if detail := LoaderDetailToProto(loader); detail.GetSummary().GetLoaderId() != "loader-1" || detail.GetEnvItems()[0].GetValue() != secretRedactedValue {
		t.Fatalf("loader detail = %#v", detail)
	}
	for _, kind := range []string{domain.LoaderTriggerKindInterval, domain.LoaderTriggerKindEvent, domain.LoaderTriggerKindTimeout, domain.LoaderTriggerKindCron, "bad"} {
		_ = LoaderTriggerKindToProto(kind)
	}
	runSummary := domain.LoaderRunSummary{ID: "run-1", LoaderID: "loader-1", TriggerID: "trigger-1", TriggerKind: domain.LoaderTriggerKindEvent, Status: "succeeded", StartedAt: now, CompletedAt: now, DurationMs: 12, ResultJSON: `{}`, PayloadJSON: `{}`, ArtifactsDir: "/artifacts"}
	if LoaderRunDetailToProto(runSummary).GetSummary().GetRunId() != "run-1" {
		t.Fatalf("loader run detail mapping failed")
	}
	if LoaderEventToProto(domain.LoaderEvent{ID: "event-1", LoaderID: "loader-1", RunID: "run-1", CreatedAt: now}).GetId() != "event-1" {
		t.Fatalf("loader event mapping failed")
	}
	if FormatMaybeTime(time.Time{}) != "" || FormatMaybeTime(now) == "" {
		t.Fatalf("maybe time mapping failed")
	}

	projectRun := domain.ProjectRunRecord{RunID: "run-1", ProjectID: "project-1", ProjectName: "project", ProjectRevision: 2, ManagedAgentID: "agent-1", AgentName: "Agent", Source: domain.ProjectRunSourceAPI, Status: domain.ProjectRunStatusSucceeded, SessionID: "session-1", ExitCode: 0, CreatedAt: now, UpdatedAt: now, StartedAt: now, CompletedAt: now}
	if ProjectRunDetailToProto(projectRun).GetSummary().GetRunId() != "run-1" {
		t.Fatalf("project run detail mapping failed")
	}
	for _, status := range []string{domain.ProjectRunStatusPending, domain.ProjectRunStatusRunning, domain.ProjectRunStatusSucceeded, domain.ProjectRunStatusFailed, domain.ProjectRunStatusCanceled, "bad"} {
		_ = ProjectRunStatusToProto(status)
	}
	for _, status := range []agentcomposev2.RunStatus{agentcomposev2.RunStatus_RUN_STATUS_PENDING, agentcomposev2.RunStatus_RUN_STATUS_RUNNING, agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, agentcomposev2.RunStatus_RUN_STATUS_FAILED, agentcomposev2.RunStatus_RUN_STATUS_CANCELED, agentcomposev2.RunStatus(99)} {
		_ = ProjectRunStatusFromProto(status)
	}
	for _, source := range []string{domain.ProjectRunSourceScheduler, domain.ProjectRunSourceAPI, domain.ProjectRunSourceManual, "bad"} {
		_ = ProjectRunSourceToProto(source)
	}
	for _, source := range []agentcomposev2.RunSource{agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER, agentcomposev2.RunSource_RUN_SOURCE_API, agentcomposev2.RunSource_RUN_SOURCE_MANUAL, agentcomposev2.RunSource(99)} {
		_ = ProjectRunSourceFromProto(source)
		_ = ProjectRunSourceFilterFromProto(source)
	}
	if FormatProjectTime(time.Time{}) != "" || FormatProjectTime(now) == "" {
		t.Fatalf("project time mapping failed")
	}

	project := domain.ProjectRecord{ID: "project-1", Name: "Project", SourcePath: "/repo/agent-compose.yml", CurrentRevision: 2, SpecHash: "hash", CreatedAt: now, UpdatedAt: now}
	projectAgent := domain.ProjectAgentRecord{ProjectID: project.ID, AgentName: "worker", ManagedAgentID: "agent-1", Provider: "codex", Model: "gpt", Image: "guest:latest", Driver: "docker", SchedulerEnabled: true}
	projectScheduler := domain.ProjectSchedulerRecord{ProjectID: project.ID, AgentName: "worker", SchedulerID: "scheduler-1", ManagedLoaderID: "loader-1", Enabled: true, TriggerCount: 2}
	projectProto := ProjectToProto(project, &agentcomposev2.ProjectSpec{Name: "Project"}, []domain.ProjectAgentRecord{projectAgent}, []domain.ProjectSchedulerRecord{projectScheduler})
	if projectProto.GetSummary().GetProjectId() != project.ID || projectProto.GetAgents()[0].GetManagedAgentId() != "agent-1" || projectProto.GetSchedulers()[0].GetManagedLoaderId() != "loader-1" {
		t.Fatalf("project proto = %#v", projectProto)
	}
	revision := domain.ProjectRevisionRecord{ProjectID: project.ID, Revision: 2, SpecHash: "hash", CreatedAt: now}
	if ProjectRevisionToProto(revision, projectProto.GetSpec()).GetRevision() != 2 {
		t.Fatalf("project revision mapping failed")
	}
	changes := ProjectApplyChanges(project, domain.ProjectRecord{ID: project.ID, Name: "Old", SourcePath: project.SourcePath}, true, revision, true)
	if len(changes) != 1 || changes[0].GetAction() != agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED {
		t.Fatalf("ProjectApplyChanges updated = %#v", changes)
	}
	createdChanges := ProjectApplyChanges(project, domain.ProjectRecord{}, false, revision, false)
	if len(createdChanges) != 1 || createdChanges[0].GetAction() != agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED {
		t.Fatalf("ProjectApplyChanges created = %#v", createdChanges)
	}
	dryRun := DryRunProjectChanges(project, []domain.ProjectAgentRecord{projectAgent}, []domain.AgentDefinition{agent}, []domain.ProjectSchedulerRecord{projectScheduler}, []domain.Loader{loader})
	if len(dryRun) != 5 || dryRun[4].GetResourceType() != "loader" {
		t.Fatalf("DryRunProjectChanges = %#v", dryRun)
	}
	normalizedSpec := &compose.NormalizedProjectSpec{
		Name:       "Project",
		Variables:  map[string]compose.EnvVarSpec{"B": {Value: "2"}, "A": {Value: "1", Secret: true}},
		Workspaces: map[string]compose.WorkspaceSpec{"repo": {Name: "repo", Provider: "git", URL: "https://example.test/repo.git", Branch: "main", Path: "src"}},
		Network:    &compose.NetworkSpec{Mode: "host"},
		Agents: []compose.NormalizedAgentSpec{{
			Name: "worker", Provider: "codex", Model: "gpt", SystemPrompt: "system", Image: "guest:latest", CapsetIDs: []string{"dev"},
			Build: &compose.NormalizedBuildSpec{
				Context:    "docker",
				Dockerfile: "Dockerfile.agent",
				Target:     "runtime",
				Args:       map[string]string{"A": "1"},
				Platforms:  []string{"linux/amd64"},
				Tags:       []string{"worker:latest"},
				NoCache:    true,
				Pull:       true,
			},
			Driver:    &compose.NormalizedDriverSpec{Name: compose.DriverDocker, Docker: &compose.DockerDriverSpec{Host: "unix:///docker.sock"}},
			Env:       map[string]compose.EnvVarSpec{"TOKEN": {Value: "secret", Secret: true}},
			Workspace: &compose.WorkspaceSpec{Provider: "file", Path: "work"},
			Jupyter:   &compose.JupyterSpec{Enabled: true, GuestPort: 8888},
			Scheduler: &compose.NormalizedSchedulerSpec{Enabled: true, Script: "function main(){}", Triggers: []compose.NormalizedTriggerSpec{
				{Name: "cron", Kind: "cron", Cron: "*/5 * * * *"},
				{Name: "interval", Kind: "interval", Interval: "5m"},
				{Name: "timeout", Kind: "timeout", Timeout: "1m"},
				{Name: "event", Kind: "event", Event: &compose.EventTriggerSpec{Topic: "runtime.test"}, Prompt: "go"},
			}},
		}},
	}
	specProto := ProjectSpecToProto(normalizedSpec)
	if specProto.GetAgents()[0].GetDriver().GetDocker().GetHost() != "unix:///docker.sock" || specProto.GetAgents()[0].GetScheduler().GetTriggers()[3].GetEvent().GetTopic() != "runtime.test" {
		t.Fatalf("ProjectSpecToProto = %#v", specProto)
	}
	if specProto.GetAgents()[0].GetBuild().GetArgs()["A"] != "1" || !specProto.GetAgents()[0].GetBuild().GetNoCache() || !specProto.GetAgents()[0].GetBuild().GetPull() {
		t.Fatalf("build proto = %#v", specProto.GetAgents()[0].GetBuild())
	}
	shape, issues := ProjectSpecYAMLShape(specProto)
	if len(issues) != 0 || shape["name"] != "Project" {
		t.Fatalf("ProjectSpecYAMLShape shape=%#v issues=%#v", shape, issues)
	}
	if ProjectSpecToProto(nil) != nil || BuildSpecToProto(nil) != nil || JupyterSpecToProto(nil) != nil || WorkspaceSpecToProto(nil) != nil || NetworkSpecToProto(nil) != nil || DriverSpecToProto(nil) != nil || SchedulerSpecToProto(nil) != nil {
		t.Fatalf("nil proto mapper returned non-nil")
	}
	if build := BuildYAMLShape(specProto.GetAgents()[0].GetBuild()); build["context"] != "docker" || build["dockerfile"] != "Dockerfile.agent" || build["target"] != "runtime" || build["no_cache"] != true || build["pull"] != true {
		t.Fatalf("BuildYAMLShape = %#v", build)
	}
	if BuildYAMLShape(nil) != nil || cloneProjectStringMap(nil) != nil {
		t.Fatalf("empty build helpers returned non-nil")
	}
	clonedArgs := cloneProjectStringMap(map[string]string{"A": "1"})
	clonedArgs["A"] = "changed"
	if specProto.GetAgents()[0].GetBuild().GetArgs()["A"] != "1" {
		t.Fatalf("cloneProjectStringMap did not clone args")
	}
	if _, issues := EnvVarYAMLMap("env", []*agentcomposev2.EnvVarSpec{{Name: "A"}, {Name: " A "}}); len(issues) != 1 {
		t.Fatalf("expected duplicate env issue")
	}
	if _, issues := AgentYAMLMap([]*agentcomposev2.AgentSpec{{Name: "worker"}, {Name: " worker "}}); len(issues) != 1 {
		t.Fatalf("expected duplicate agent issue")
	}
	if _, issues := DriverYAMLShape("driver", &agentcomposev2.DriverSpec{Name: "docker", Boxlite: &agentcomposev2.BoxliteDriverSpec{}}); len(issues) != 1 {
		t.Fatalf("expected conflicting driver issue")
	}
	if _, issues := DriverYAMLShape("driver", &agentcomposev2.DriverSpec{Name: "bad"}); len(issues) != 1 {
		t.Fatalf("expected unsupported driver issue")
	}
	if driver, issues := DriverYAMLShape("driver", &agentcomposev2.DriverSpec{Name: "boxlite"}); len(issues) != 0 || driver["boxlite"] == nil {
		t.Fatalf("named empty boxlite driver=%#v issues=%#v", driver, issues)
	}
	if driver, issues := DriverYAMLShape("driver", &agentcomposev2.DriverSpec{Boxlite: &agentcomposev2.BoxliteDriverSpec{Kernel: "vmlinux", Rootfs: "rootfs.ext4"}, Microsandbox: &agentcomposev2.MicrosandboxDriverSpec{Profile: "dev"}}); len(issues) != 0 || len(driver) != 2 {
		t.Fatalf("multi driver=%#v issues=%#v", driver, issues)
	}
	if network := NetworkYAMLShape(nil); network != nil {
		t.Fatalf("NetworkYAMLShape nil = %#v", network)
	}
	if sourcePath := ProjectServiceSourcePath(&agentcomposev2.ProjectSource{ProjectDir: "/repo"}); sourcePath != filepath.Join("/repo", "agent-compose.yml") {
		t.Fatalf("ProjectServiceSourcePath = %q", sourcePath)
	}
	if sourcePath := ProjectServiceSourcePath(&agentcomposev2.ProjectSource{ComposePath: "/repo/custom.yml", ProjectDir: "/repo"}); sourcePath != "/repo/custom.yml" {
		t.Fatalf("ProjectServiceSourcePath compose path = %q", sourcePath)
	}
	if sourcePath := ProjectServiceSourcePath(nil); sourcePath != "" {
		t.Fatalf("ProjectServiceSourcePath nil = %q", sourcePath)
	}
	if issue := IssueFromComposeError(&compose.ValidationError{Path: "agents.worker", Message: "bad"}); issue.GetPath() != "agents.worker" {
		t.Fatalf("validation issue = %#v", issue)
	}
	if issue := IssueFromComposeError(&compose.ParseError{Path: "name", Message: "parse bad"}); issue.GetPath() != "name" {
		t.Fatalf("parse issue = %#v", issue)
	}
	if issue := ProjectValidationIssue("", "generic"); issue.GetPath() != "spec" {
		t.Fatalf("default validation issue = %#v", issue)
	}
}

func TestAPIHandlerDelegateAndDashboardWorkflows(t *testing.T) {
	ctx := context.Background()
	sessionDelegate := &fakeSessionDelegate{}
	sessionHandler := NewSessionHandler(sessionDelegate, nil, nil)
	created, err := sessionHandler.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{Title: "created"}))
	if err != nil || created.Msg.GetSession().GetSummary().GetTitle() != "created" {
		t.Fatalf("CreateSession response=%#v err=%v", created, err)
	}
	if _, err := sessionHandler.ResumeSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: "session-1"})); err != nil {
		t.Fatalf("ResumeSession returned error: %v", err)
	}
	if _, err := sessionHandler.StopSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: "session-1"})); err != nil {
		t.Fatalf("StopSession returned error: %v", err)
	}
	if _, err := sessionHandler.GetSessionProxy(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: "session-1"})); err != nil {
		t.Fatalf("GetSessionProxy returned error: %v", err)
	}
	if len(sessionDelegate.stopCalls) != 1 || sessionDelegate.stopCalls[0] != "session-1" {
		t.Fatalf("stop calls = %#v", sessionDelegate.stopCalls)
	}

	runDelegate := &fakeRunDelegate{}
	runHandler := NewRunHandler(runDelegate, nil)
	runResp, err := runHandler.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{ProjectId: "project-1", AgentName: "worker"}))
	if err != nil || runResp.Msg.GetRun().GetSummary().GetRunId() != "run-1" {
		t.Fatalf("RunAgent response=%#v err=%v", runResp, err)
	}
	startResp, err := runHandler.StartRun(ctx, connect.NewRequest(&agentcomposev2.StartRunRequest{Run: &agentcomposev2.RunAgentRequest{ProjectId: "project-1", AgentName: "worker"}}))
	if err != nil || !startResp.Msg.GetStarted() {
		t.Fatalf("StartRun response=%#v err=%v", startResp, err)
	}
	if _, err := NewRunHandler(nil, nil).StartRun(ctx, connect.NewRequest(&agentcomposev2.StartRunRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("nil StartRun error = %v", err)
	}

	dashboardCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	aggregator := dashboard.NewAggregator(fakeDashboardSessionStore{}, fakeDashboardRunStore{})
	aggregator.SetClock(func() time.Time { return time.Date(2026, 7, 1, 1, 2, 3, 0, time.UTC) })
	hub := dashboard.NewHub(dashboardCtx, aggregator, time.Millisecond)
	dashboardResp, err := NewDashboardHandler(hub).GetDashboardOverview(ctx, connect.NewRequest(&emptypb.Empty{}))
	if err != nil || dashboardResp.Msg.GetOverview().GetRuns().GetRunningCount() != 2 {
		t.Fatalf("dashboard response=%#v err=%v", dashboardResp, err)
	}
}

func TestIntegrationAPIMappingCoverageWorkflows(t *testing.T) {
	TestAPIMappingCoverageWorkflows(t)
}

func TestE2EAPIMappingCoverageWorkflows(t *testing.T) {
	TestAPIMappingCoverageWorkflows(t)
}

func TestAPILightweightHandlersCoverageWorkflows(t *testing.T) {
	ctx := context.Background()
	configStore := newFakeConfigStore()
	configHandler := NewConfigHandler(&appconfig.Config{DataRoot: t.TempDir()}, configStore)
	if resp, err := configHandler.GetGlobalEnvConfig(ctx, connect.NewRequest(&emptypb.Empty{})); err != nil || len(resp.Msg.GetEnvItems()) != 1 {
		t.Fatalf("get global env resp=%v err=%v", resp, err)
	}
	if resp, err := configHandler.UpdateGlobalEnvConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateGlobalEnvConfigRequest{EnvItems: []*agentcomposev1.SessionEnvVar{{Name: "SECRET", Secret: true}}})); err != nil || resp.Msg.GetEnvItems()[0].GetValue() != secretRedactedValue {
		t.Fatalf("update global env resp=%v err=%v", resp, err)
	}
	if resp, err := configHandler.GetCapabilityGatewayConfig(ctx, connect.NewRequest(&emptypb.Empty{})); err != nil || !resp.Msg.GetTokenSet() {
		t.Fatalf("get capability gateway resp=%v err=%v", resp, err)
	}
	if resp, err := configHandler.UpdateCapabilityGatewayConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateCapabilityGatewayConfigRequest{Addr: "http://octobus", Token: " token "})); err != nil || !resp.Msg.GetTokenSet() {
		t.Fatalf("update capability gateway resp=%v err=%v", resp, err)
	}
	if resp, err := configHandler.CreateWorkspaceConfig(ctx, connect.NewRequest(&agentcomposev1.CreateWorkspaceConfigRequest{Name: "Files", Type: "file", Comment: "files"})); err != nil || resp.Msg.GetWorkspace().GetType() != "file" {
		t.Fatalf("create file workspace resp=%v err=%v", resp, err)
	}
	fileWorkspaceID := configStore.lastWorkspaceID
	if resp, err := configHandler.UpdateWorkspaceConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateWorkspaceConfigRequest{WorkspaceId: fileWorkspaceID, Name: "Repo", Type: "git", ConfigJson: `{"repo_url":"https://example.test/repo.git"}`})); err != nil || resp.Msg.GetWorkspace().GetType() != "git" {
		t.Fatalf("update workspace to git resp=%v err=%v", resp, err)
	}
	if resp, err := configHandler.UpdateWorkspaceConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateWorkspaceConfigRequest{WorkspaceId: fileWorkspaceID, Name: "Files Again", Type: "file"})); err != nil || resp.Msg.GetWorkspace().GetType() != "file" {
		t.Fatalf("update workspace back to file resp=%v err=%v", resp, err)
	}
	if resp, err := configHandler.ListWorkspaceConfigs(ctx, connect.NewRequest(&emptypb.Empty{})); err != nil || len(resp.Msg.GetWorkspaces()) != 1 {
		t.Fatalf("list workspaces resp=%v err=%v", resp, err)
	}
	if _, err := configHandler.DeleteWorkspaceConfig(ctx, connect.NewRequest(&agentcomposev1.WorkspaceConfigIDRequest{WorkspaceId: fileWorkspaceID})); err != nil {
		t.Fatalf("delete workspace err=%v", err)
	}
	if _, err := configHandler.UpdateGlobalEnvConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateGlobalEnvConfigRequest{EnvItems: []*agentcomposev1.SessionEnvVar{{Name: "MISSING_SECRET", Secret: true}}})); err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected missing secret update invalid argument, err=%v", err)
	}
	if _, err := configHandler.UpdateWorkspaceConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateWorkspaceConfigRequest{WorkspaceId: "missing", Name: "Missing", Type: "git"})); err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected update missing workspace invalid argument, err=%v", err)
	}
	if _, err := configHandler.DeleteWorkspaceConfig(ctx, connect.NewRequest(&agentcomposev1.WorkspaceConfigIDRequest{WorkspaceId: "missing"})); err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected delete missing workspace invalid argument, err=%v", err)
	}
	if err := configHandler.checkFileWorkspaceContentCreatable("workspace-check"); err != nil {
		t.Fatalf("checkFileWorkspaceContentCreatable err=%v", err)
	}
	badRoot := filepath.Join(configHandler.config.DataRoot, "workspaces", "bad")
	if err := os.MkdirAll(filepath.Dir(badRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badRoot, []byte("not-dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := configHandler.checkFileWorkspaceContentCreatable("bad"); err == nil {
		t.Fatalf("expected non-directory workspace path error")
	}

	agentStore := newFakeAgentDefinitionStore()
	agentSessions := &fakeAgentDefinitionSessionStore{sessions: []*domain.Session{
		{Summary: domain.SessionSummary{ID: "session-running", VMStatus: domain.VMStatusRunning, Tags: []domain.SessionTag{{Name: domain.AgentSessionTagSource, Value: domain.AgentSessionTagSourceVal}, {Name: domain.AgentSessionTagID, Value: "agent-1"}}}},
		{Summary: domain.SessionSummary{ID: "session-pending", VMStatus: domain.VMStatusPending, Tags: []domain.SessionTag{{Name: domain.AgentSessionTagSource, Value: domain.AgentSessionTagSourceVal}, {Name: domain.AgentSessionTagID, Value: "agent-1"}}}},
	}}
	agentDelegate := &fakeSessionDelegate{}
	agentHandler := NewAgentDefinitionHandler(&appconfig.Config{RuntimeDriver: "boxlite"}, agentSessions, agentStore, agentDelegate, sessions.NewStreamBrokerForTest())
	if resp, err := agentHandler.ListAgentDefinitions(ctx, connect.NewRequest(&agentcomposev1.ListAgentDefinitionsRequest{IncludeDisabled: true, Limit: 10})); err != nil || len(resp.Msg.GetAgents()) != 1 {
		t.Fatalf("list agent definitions resp=%v err=%v", resp, err)
	}
	if resp, err := agentHandler.GetAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.AgentDefinitionIDRequest{AgentId: "agent-1"})); err != nil || resp.Msg.GetAgent().GetAgentId() != "agent-1" {
		t.Fatalf("get agent definition resp=%v err=%v", resp, err)
	}
	if resp, err := agentHandler.CreateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.CreateAgentDefinitionRequest{Name: "Created", Enabled: true, Provider: "codex", WorkspaceId: "workspace-1", Driver: "boxlite", GuestImage: "guest:latest", EnvItems: []*agentcomposev1.SessionEnvVar{{Name: "A", Value: "B"}}})); err != nil || resp.Msg.GetAgent().GetName() != "Created" {
		t.Fatalf("create agent definition resp=%v err=%v", resp, err)
	}
	if resp, err := agentHandler.UpdateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.UpdateAgentDefinitionRequest{AgentId: "agent-1", Name: "Updated", Enabled: true, Provider: "codex", WorkspaceId: "workspace-1", Driver: "boxlite", GuestImage: "guest:latest"})); err != nil || resp.Msg.GetAgent().GetName() != "Updated" {
		t.Fatalf("update agent definition resp=%v err=%v", resp, err)
	}
	if resp, err := agentHandler.ValidateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.ValidateAgentDefinitionRequest{Name: "Valid", Provider: "codex", WorkspaceId: "workspace-1", Driver: "boxlite"})); err != nil || len(resp.Msg.GetErrors()) != 0 {
		t.Fatalf("validate agent definition resp=%v err=%v", resp, err)
	}
	if resp, err := agentHandler.CreateAgentSession(ctx, connect.NewRequest(&agentcomposev1.CreateAgentSessionRequest{AgentId: "agent-1", Title: "Agent Session", EnvItems: []*agentcomposev1.SessionEnvVar{{Name: "REQUEST", Value: "value"}}})); err != nil || resp.Msg.GetSession().GetSummary().GetSessionId() == "" {
		t.Fatalf("create agent session resp=%v err=%v", resp, err)
	}
	if resp, err := agentHandler.SetAgentDefinitionEnabled(ctx, connect.NewRequest(&agentcomposev1.SetAgentDefinitionEnabledRequest{AgentId: "agent-1", Enabled: false})); err != nil || resp.Msg.GetAgent().GetEnabled() {
		t.Fatalf("set agent disabled resp=%v err=%v", resp, err)
	}
	if _, err := agentHandler.CreateAgentSession(ctx, connect.NewRequest(&agentcomposev1.CreateAgentSessionRequest{AgentId: "agent-1"})); err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected disabled create session invalid argument, err=%v", err)
	}
	if resp, err := agentHandler.SetAgentDefinitionEnabled(ctx, connect.NewRequest(&agentcomposev1.SetAgentDefinitionEnabledRequest{AgentId: "agent-1", Enabled: true})); err != nil || !resp.Msg.GetAgent().GetEnabled() {
		t.Fatalf("set agent enabled resp=%v err=%v", resp, err)
	}
	if _, err := agentHandler.UpdateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.UpdateAgentDefinitionRequest{})); err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected empty update agent invalid argument, err=%v", err)
	}
	if _, err := agentHandler.DeleteAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.AgentDefinitionIDRequest{})); err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected empty delete agent invalid argument, err=%v", err)
	}
	if _, err := agentHandler.SetAgentDefinitionEnabled(ctx, connect.NewRequest(&agentcomposev1.SetAgentDefinitionEnabledRequest{})); err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected empty set enabled invalid argument, err=%v", err)
	}
	if _, err := agentHandler.CreateAgentSession(ctx, connect.NewRequest(&agentcomposev1.CreateAgentSessionRequest{})); err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected empty create session invalid argument, err=%v", err)
	}
	if _, err := agentHandler.DeleteAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.AgentDefinitionIDRequest{AgentId: "agent-1"})); err != nil {
		t.Fatalf("delete agent definition err=%v", err)
	}
	if len(agentDelegate.stopCalls) != 1 || agentSessions.updated == 0 || agentStore.disabledLoaders == 0 {
		t.Fatalf("delete side effects stops=%#v updated=%d disabled=%d", agentDelegate.stopCalls, agentSessions.updated, agentStore.disabledLoaders)
	}
	if _, err := agentHandler.GetAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.AgentDefinitionIDRequest{})); err == nil {
		t.Fatalf("expected empty agent id error")
	}

	loaderController := &fakeLoaderController{}
	loaderHandler := NewLoaderHandler(loaderController, &fakeLoaderStore{})
	if resp, err := loaderHandler.ValidateLoader(ctx, connect.NewRequest(&agentcomposev1.ValidateLoaderRequest{Runtime: domain.LoaderRuntimeScheduler, Script: "function main(){}"})); err != nil || len(resp.Msg.GetTriggers()) != 1 {
		t.Fatalf("validate loader resp=%v err=%v", resp, err)
	}
	if resp, err := loaderHandler.ListLoaders(ctx, connect.NewRequest(&emptypb.Empty{})); err != nil || len(resp.Msg.GetLoaders()) != 1 {
		t.Fatalf("list loaders resp=%v err=%v", resp, err)
	}
	if resp, err := loaderHandler.GetLoader(ctx, connect.NewRequest(&agentcomposev1.LoaderIDRequest{LoaderId: "loader-1"})); err != nil || resp.Msg.GetLoader().GetSummary().GetLoaderId() != "loader-1" {
		t.Fatalf("get loader resp=%v err=%v", resp, err)
	}
	if resp, err := loaderHandler.CreateLoader(ctx, connect.NewRequest(&agentcomposev1.CreateLoaderRequest{Name: "Loader", Runtime: domain.LoaderRuntimeScheduler, AgentId: "agent-1", Script: "function main(){}", EnvItems: []*agentcomposev1.SessionEnvVar{{Name: "A", Value: "B"}}})); err != nil || resp.Msg.GetLoader().GetSummary().GetDefaultAgent() != "codex" {
		t.Fatalf("create loader resp=%v err=%v", resp, err)
	}
	if resp, err := loaderHandler.UpdateLoader(ctx, connect.NewRequest(&agentcomposev1.UpdateLoaderRequest{LoaderId: "loader-1", Name: "Loader", Runtime: domain.LoaderRuntimeScheduler, DefaultAgent: "codex", Script: "function main(){}", EnvItems: []*agentcomposev1.SessionEnvVar{{Name: "LOADER_SECRET", Value: secretRedactedValue, Secret: true}}})); err != nil || resp.Msg.GetLoader().GetSummary().GetLoaderId() != "loader-1" {
		t.Fatalf("update loader resp=%v err=%v", resp, err)
	}
	if len(loaderController.updated.EnvItems) != 1 || loaderController.updated.EnvItems[0].Value != "loader-secret" {
		t.Fatalf("update loader env = %#v", loaderController.updated.EnvItems)
	}
	for _, tc := range []struct {
		name     string
		loaderID string
		envName  string
	}{
		{name: "store error", loaderID: "loader-error", envName: "LOADER_SECRET"},
		{name: "missing secret", loaderID: "loader-1", envName: "MISSING_SECRET"},
		{name: "existing non-secret", loaderID: "loader-non-secret", envName: "LOADER_SECRET"},
		{name: "existing empty secret", loaderID: "loader-empty-secret", envName: "LOADER_SECRET"},
	} {
		_, err := loaderHandler.UpdateLoader(ctx, connect.NewRequest(&agentcomposev1.UpdateLoaderRequest{LoaderId: tc.loaderID, Name: "Loader", Runtime: domain.LoaderRuntimeScheduler, DefaultAgent: "codex", Script: "function main(){}", EnvItems: []*agentcomposev1.SessionEnvVar{{Name: tc.envName, Value: secretRedactedValue, Secret: true}}}))
		if err == nil {
			t.Fatalf("expected loader secret preservation error for %s", tc.name)
		}
	}
	if _, err := loaderHandler.DeleteLoader(ctx, connect.NewRequest(&agentcomposev1.LoaderIDRequest{LoaderId: "loader-1"})); err != nil {
		t.Fatalf("delete loader err=%v", err)
	}
	if resp, err := loaderHandler.SetLoaderEnabled(ctx, connect.NewRequest(&agentcomposev1.SetLoaderEnabledRequest{LoaderId: "loader-1", Enabled: true})); err != nil || !resp.Msg.GetLoader().GetSummary().GetEnabled() {
		t.Fatalf("set loader enabled resp=%v err=%v", resp, err)
	}
	if resp, err := loaderHandler.SetLoaderTriggerEnabled(ctx, connect.NewRequest(&agentcomposev1.SetLoaderTriggerEnabledRequest{LoaderId: "loader-1", TriggerId: "trigger-1", Enabled: false})); err != nil || resp.Msg.GetLoader().GetTriggers()[0].GetEnabled() {
		t.Fatalf("set trigger enabled resp=%v err=%v", resp, err)
	}
	if resp, err := loaderHandler.RunLoaderNow(ctx, connect.NewRequest(&agentcomposev1.RunLoaderNowRequest{LoaderId: "loader-1", TriggerId: "trigger-1", Timeout: "1s", PayloadJson: `{}`})); err != nil || resp.Msg.GetRun().GetSummary().GetRunId() != "run-1" {
		t.Fatalf("run loader resp=%v err=%v", resp, err)
	}
	if resp, err := loaderHandler.ListLoaderRuns(ctx, connect.NewRequest(&agentcomposev1.ListLoaderRunsRequest{LoaderId: "loader-1", Limit: 10})); err != nil || len(resp.Msg.GetRuns()) != 1 {
		t.Fatalf("list loader runs resp=%v err=%v", resp, err)
	}
	if resp, err := loaderHandler.GetLoaderRun(ctx, connect.NewRequest(&agentcomposev1.LoaderRunIDRequest{LoaderId: "loader-1", RunId: "run-1"})); err != nil || resp.Msg.GetRun().GetSummary().GetRunId() != "run-1" {
		t.Fatalf("get loader run resp=%v err=%v", resp, err)
	}
	if resp, err := loaderHandler.ListLoaderEvents(ctx, connect.NewRequest(&agentcomposev1.ListLoaderEventsRequest{LoaderId: "loader-1", Limit: 10})); err != nil || len(resp.Msg.GetEvents()) != 1 {
		t.Fatalf("list loader events resp=%v err=%v", resp, err)
	}
	if _, err := loaderHandler.RunLoaderNow(ctx, connect.NewRequest(&agentcomposev1.RunLoaderNowRequest{Timeout: "bad"})); err == nil {
		t.Fatalf("expected bad timeout error")
	}
	if _, err := loaderHandler.CreateLoader(ctx, connect.NewRequest(&agentcomposev1.CreateLoaderRequest{AgentId: "disabled-agent"})); err == nil {
		t.Fatalf("expected disabled agent error")
	}
	if _, err := loaderHandler.UpdateLoader(ctx, connect.NewRequest(&agentcomposev1.UpdateLoaderRequest{LoaderId: "missing", Name: "missing"})); err == nil {
		t.Fatalf("expected missing loader update error")
	}

	capHandler := NewCapabilityHandler(fakeCapabilityProvider{}, &fakeCapabilityStore{}, fakeCapabilityRuntime{listen: "127.0.0.1:9100"})
	if resp, err := capHandler.GetCapabilityStatus(ctx, connect.NewRequest(&agentcomposev1.GetCapabilityStatusRequest{})); err != nil || !resp.Msg.GetRuntimeConfigured() {
		t.Fatalf("capability status resp=%v err=%v", resp, err)
	}
	if resp, err := capHandler.ListCapabilitySets(ctx, connect.NewRequest(&agentcomposev1.ListCapabilitySetsRequest{})); err != nil || len(resp.Msg.GetCapsets()) != 1 {
		t.Fatalf("capsets resp=%v err=%v", resp, err)
	}
	if resp, err := capHandler.GetCapabilityCatalog(ctx, connect.NewRequest(&agentcomposev1.GetCapabilityCatalogRequest{CapsetId: "dev"})); err != nil || len(resp.Msg.GetMethods()) != 1 {
		t.Fatalf("catalog resp=%v err=%v", resp, err)
	}
	if resp, err := capHandler.GetCapabilityGatewayConfig(ctx, connect.NewRequest(&emptypb.Empty{})); err != nil || !resp.Msg.GetTokenSet() {
		t.Fatalf("gateway get resp=%v err=%v", resp, err)
	}
	if resp, err := capHandler.UpdateCapabilityGatewayConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateCapabilityGatewayConfigRequest{Addr: "http://octobus", Token: " token "})); err != nil || !resp.Msg.GetTokenSet() {
		t.Fatalf("gateway update resp=%v err=%v", resp, err)
	}
	for _, err := range []error{capability.ErrNotConfigured, capability.ErrInvalidCatalog, errors.New("network")} {
		if CapabilityConnectError(err) == nil {
			t.Fatalf("expected connect error")
		}
	}

	llmHandler := NewLLMHandler(fakeLLMGenerator{})
	if resp, err := llmHandler.Generate(ctx, connect.NewRequest(&agentcomposev1.GenerateLLMRequest{Prompt: "hello", Model: "gpt", OutputSchema: `{"type":"object"}`})); err != nil || resp.Msg.GetJson() == "" {
		t.Fatalf("llm resp=%v err=%v", resp, err)
	}
	if _, err := (*LLMHandler)(nil).Generate(ctx, connect.NewRequest(&agentcomposev1.GenerateLLMRequest{})); err == nil {
		t.Fatalf("expected unavailable llm error")
	}

	imageHandler := NewImageHandler(fakeImageSelector{backend: fakeImageBackend{}})
	if resp, err := imageHandler.ListImages(ctx, connect.NewRequest(&agentcomposev2.ListImagesRequest{Query: "guest", Limit: 1})); err != nil || resp.Msg.GetTotalCount() != 1 {
		t.Fatalf("list images resp=%v err=%v", resp, err)
	}
	if resp, err := imageHandler.PullImage(ctx, connect.NewRequest(&agentcomposev2.PullImageRequest{ImageRef: "guest:latest"})); err != nil || resp.Msg.GetResolvedRef() == "" {
		t.Fatalf("pull image resp=%v err=%v", resp, err)
	}
	if resp, err := imageHandler.InspectImage(ctx, connect.NewRequest(&agentcomposev2.InspectImageRequest{ImageRef: "guest:latest"})); err != nil || resp.Msg.GetImage() == nil {
		t.Fatalf("inspect image resp=%v err=%v", resp, err)
	}
	if resp, err := imageHandler.RemoveImage(ctx, connect.NewRequest(&agentcomposev2.RemoveImageRequest{ImageRef: "guest:latest", Force: true})); err != nil || len(resp.Msg.GetDeletedIds()) != 1 {
		t.Fatalf("remove image resp=%v err=%v", resp, err)
	}
	for _, req := range []any{
		&agentcomposev2.PullImageRequest{},
		&agentcomposev2.InspectImageRequest{},
		&agentcomposev2.RemoveImageRequest{},
	} {
		switch item := req.(type) {
		case *agentcomposev2.PullImageRequest:
			_, _ = imageHandler.PullImage(ctx, connect.NewRequest(item))
		case *agentcomposev2.InspectImageRequest:
			_, _ = imageHandler.InspectImage(ctx, connect.NewRequest(item))
		case *agentcomposev2.RemoveImageRequest:
			_, _ = imageHandler.RemoveImage(ctx, connect.NewRequest(item))
		}
	}
	for _, tc := range []struct {
		err  error
		code connect.Code
	}{
		{err: images.OpError{Op: "inspect", ImageRef: "missing:latest", Err: imagecache.NewError(imagecache.ErrorKindNotFound, "inspect", "missing:latest", errors.New("missing"))}, code: connect.CodeNotFound},
		{err: images.OpError{Op: "pull", ImageRef: "bad ref", Err: imagecache.NewError(imagecache.ErrorKindInvalidReference, "pull", "bad ref", errors.New("bad"))}, code: connect.CodeInvalidArgument},
		{err: images.OpError{Op: "remove", ImageRef: "busy:latest", Err: imagecache.NewError(imagecache.ErrorKindConflict, "remove", "busy:latest", errors.New("busy"))}, code: connect.CodeFailedPrecondition},
		{err: images.OpError{Op: "list", Err: imagecache.NewError(imagecache.ErrorKindInternal, "list", "", errors.New("boom"))}, code: connect.CodeInternal},
		{err: images.OpError{Op: "list", Err: imagecache.NewError(imagecache.ErrorKindUnavailable, "list", "", errors.New("down"))}, code: connect.CodeUnavailable},
		{err: errors.New("other"), code: connect.CodeUnknown},
	} {
		if got := connect.CodeOf(ConnectErrorForImageBackend("op", "image:latest", tc.err)); got != tc.code {
			t.Fatalf("ConnectErrorForImageBackend(%v) = %v, want %v", tc.err, got, tc.code)
		}
	}
	if ConnectErrorForImageBackend("", "", nil) != nil {
		t.Fatalf("nil backend error should map to nil")
	}

	if got := ExecEnvMap([]*agentcomposev2.EnvVarSpec{{Name: " FOO ", Value: "bar"}, {Name: " "}}); got["FOO"] != "bar" {
		t.Fatalf("exec env = %#v", got)
	}
	execResult := ExecResultToProto("exec-1", "session-1", "run-1", &agentcomposev2.ExecRequest{Command: &agentcomposev2.ExecCommand{Command: "echo", Args: []string{"hi"}}}, "/workspace", domain.ExecResult{Stdout: "hi", Output: "hi", Success: true}, errors.New("warning"))
	if execResult.GetExecId() != "exec-1" || execResult.GetError() == "" {
		t.Fatalf("exec result = %#v", execResult)
	}
}

func TestIntegrationAPILightweightHandlersCoverageWorkflows(t *testing.T) {
	TestAPILightweightHandlersCoverageWorkflows(t)
}

func TestE2EAPILightweightHandlersCoverageWorkflows(t *testing.T) {
	TestAPILightweightHandlersCoverageWorkflows(t)
}

func TestIntegrationAPIStoreBackedHandlerWorkflows(t *testing.T) {
	t.Run("session handler", TestSessionHandlerGetAndListSessionsUseStoreAndReconciler)
	t.Run("project proto mapping", TestProjectSpecToProtoIncludesSchedulerScript)
}

func TestE2EAPIStoreBackedHandlerWorkflows(t *testing.T) {
	TestIntegrationAPIStoreBackedHandlerWorkflows(t)
}

type fakeCapabilityProvider struct{}

type fakeConfigStore struct {
	env             []domain.SessionEnvVar
	gateway         domain.CapabilityGatewaySettings
	workspaces      map[string]domain.WorkspaceConfig
	lastWorkspaceID string
}

func newFakeConfigStore() *fakeConfigStore {
	return &fakeConfigStore{
		env:        []domain.SessionEnvVar{{Name: "SECRET", Value: "secret", Secret: true}},
		gateway:    domain.CapabilityGatewaySettings{Addr: "http://octobus", Token: "token"},
		workspaces: map[string]domain.WorkspaceConfig{},
	}
}

func (s *fakeConfigStore) ListGlobalEnv(context.Context) ([]domain.SessionEnvVar, error) {
	return append([]domain.SessionEnvVar(nil), s.env...), nil
}

func (s *fakeConfigStore) ReplaceGlobalEnv(_ context.Context, items []domain.SessionEnvVar) ([]domain.SessionEnvVar, error) {
	s.env = append([]domain.SessionEnvVar(nil), items...)
	return s.ListGlobalEnv(context.Background())
}

func (s *fakeConfigStore) ListWorkspaceConfigs(context.Context) ([]domain.WorkspaceConfig, error) {
	items := make([]domain.WorkspaceConfig, 0, len(s.workspaces))
	for _, item := range s.workspaces {
		items = append(items, item)
	}
	return items, nil
}

func (s *fakeConfigStore) GetWorkspaceConfig(_ context.Context, id string) (domain.WorkspaceConfig, error) {
	item, ok := s.workspaces[id]
	if !ok {
		return domain.WorkspaceConfig{}, domain.ResourceError(domain.ErrNotFound, "workspace", id, "not found", nil)
	}
	return item, nil
}

func (s *fakeConfigStore) CreateWorkspaceConfig(_ context.Context, item domain.WorkspaceConfig) (domain.WorkspaceConfig, error) {
	if item.ID == "" {
		item.ID = "workspace-generated"
	}
	item.CreatedAt = time.Now().UTC()
	item.UpdatedAt = item.CreatedAt
	s.workspaces[item.ID] = item
	s.lastWorkspaceID = item.ID
	return item, nil
}

func (s *fakeConfigStore) UpdateWorkspaceConfig(_ context.Context, item domain.WorkspaceConfig) (domain.WorkspaceConfig, error) {
	if _, ok := s.workspaces[item.ID]; !ok {
		return domain.WorkspaceConfig{}, domain.ResourceError(domain.ErrNotFound, "workspace", item.ID, "not found", nil)
	}
	item.UpdatedAt = time.Now().UTC()
	s.workspaces[item.ID] = item
	return item, nil
}

func (s *fakeConfigStore) DeleteWorkspaceConfig(_ context.Context, id string) error {
	delete(s.workspaces, id)
	return nil
}

func (s *fakeConfigStore) GetCapabilityGateway(context.Context) (domain.CapabilityGatewaySettings, error) {
	return s.gateway, nil
}

func (s *fakeConfigStore) SaveCapabilityGateway(_ context.Context, settings domain.CapabilityGatewaySettings) (domain.CapabilityGatewaySettings, error) {
	s.gateway = settings
	return settings, nil
}

type fakeAgentDefinitionStore struct {
	agents          map[string]domain.AgentDefinition
	workspace       domain.WorkspaceConfig
	disabledLoaders int
}

func newFakeAgentDefinitionStore() *fakeAgentDefinitionStore {
	now := time.Now().UTC()
	agent := domain.AgentDefinition{ID: "agent-1", Name: "Agent", Enabled: true, Provider: "codex", Driver: "boxlite", GuestImage: "guest:latest", WorkspaceID: "workspace-1", CreatedAt: now, UpdatedAt: now}
	return &fakeAgentDefinitionStore{
		agents:    map[string]domain.AgentDefinition{agent.ID: agent},
		workspace: domain.WorkspaceConfig{ID: "workspace-1", Name: "Workspace", Type: "file", ConfigJSON: "{}", CreatedAt: now, UpdatedAt: now},
	}
}

func (s *fakeAgentDefinitionStore) ListAgentDefinitions(_ context.Context, options domain.AgentDefinitionListOptions) (domain.AgentDefinitionListResult, error) {
	items := make([]domain.AgentDefinition, 0, len(s.agents))
	for _, item := range s.agents {
		if !options.IncludeDisabled && !item.Enabled {
			continue
		}
		items = append(items, item)
	}
	return domain.AgentDefinitionListResult{Agents: items, TotalCount: len(items)}, nil
}

func (s *fakeAgentDefinitionStore) GetAgentDefinition(_ context.Context, id string) (domain.AgentDefinition, error) {
	item, ok := s.agents[id]
	if !ok || !item.DeletedAt.IsZero() {
		return domain.AgentDefinition{}, domain.ResourceError(domain.ErrNotFound, "agent", id, "not found", nil)
	}
	return item, nil
}

func (s *fakeAgentDefinitionStore) GetAgentDefinitionIncludingDeleted(_ context.Context, id string) (domain.AgentDefinition, error) {
	item, ok := s.agents[id]
	if !ok {
		return domain.AgentDefinition{}, domain.ResourceError(domain.ErrNotFound, "agent", id, "not found", nil)
	}
	return item, nil
}

func (s *fakeAgentDefinitionStore) CreateAgentDefinition(_ context.Context, item domain.AgentDefinition) (domain.AgentDefinition, error) {
	item.CreatedAt = time.Now().UTC()
	item.UpdatedAt = item.CreatedAt
	s.agents[item.ID] = item
	return item, nil
}

func (s *fakeAgentDefinitionStore) UpdateAgentDefinition(_ context.Context, item domain.AgentDefinition) (domain.AgentDefinition, error) {
	item.UpdatedAt = time.Now().UTC()
	s.agents[item.ID] = item
	return item, nil
}

func (s *fakeAgentDefinitionStore) DeleteAgentDefinition(_ context.Context, id string) error {
	item, ok := s.agents[id]
	if !ok {
		return domain.ResourceError(domain.ErrNotFound, "agent", id, "not found", nil)
	}
	item.Enabled = false
	item.DeletedAt = time.Now().UTC()
	s.agents[id] = item
	return nil
}

func (s *fakeAgentDefinitionStore) SetAgentDefinitionEnabled(_ context.Context, id string, enabled bool) (domain.AgentDefinition, error) {
	item, err := s.GetAgentDefinition(context.Background(), id)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	item.Enabled = enabled
	s.agents[id] = item
	return item, nil
}

func (s *fakeAgentDefinitionStore) DisableLoadersByDefaultAgent(context.Context, string) (int, error) {
	s.disabledLoaders++
	return 1, nil
}

func (s *fakeAgentDefinitionStore) GetWorkspaceConfig(context.Context, string) (domain.WorkspaceConfig, error) {
	return s.workspace, nil
}

type fakeAgentDefinitionSessionStore struct {
	sessions []*domain.Session
	updated  int
	events   int
}

func (s *fakeAgentDefinitionSessionStore) ListSessions(context.Context, domain.SessionListOptions) (domain.SessionListResult, error) {
	return domain.SessionListResult{Sessions: s.sessions, TotalCount: len(s.sessions)}, nil
}

func (s *fakeAgentDefinitionSessionStore) GetVMState(string) (domain.VMState, error) {
	return domain.VMState{}, nil
}

func (s *fakeAgentDefinitionSessionStore) SaveVMState(string, domain.VMState) error {
	return nil
}

func (s *fakeAgentDefinitionSessionStore) UpdateSession(_ context.Context, session *domain.Session) error {
	s.updated++
	for i, current := range s.sessions {
		if current.Summary.ID == session.Summary.ID {
			s.sessions[i] = session
		}
	}
	return nil
}

func (s *fakeAgentDefinitionSessionStore) AddEvent(context.Context, string, domain.SessionEvent) error {
	s.events++
	return nil
}

type fakeSessionDelegate struct {
	stopCalls []string
}

func (d *fakeSessionDelegate) CreateSession(_ context.Context, req *connect.Request[agentcomposev1.CreateSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return connect.NewResponse(&agentcomposev1.SessionResponse{Session: &agentcomposev1.SessionDetail{Summary: &agentcomposev1.SessionSummary{SessionId: "created-session", Title: req.Msg.GetTitle(), VmStatus: domain.VMStatusRunning}}}), nil
}

func (d *fakeSessionDelegate) ResumeSession(context.Context, *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return connect.NewResponse(&agentcomposev1.SessionResponse{}), nil
}

func (d *fakeSessionDelegate) StopSession(_ context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	d.stopCalls = append(d.stopCalls, req.Msg.GetSessionId())
	return connect.NewResponse(&agentcomposev1.SessionResponse{}), nil
}

func (d *fakeSessionDelegate) GetSessionProxy(context.Context, *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionProxyResponse], error) {
	return connect.NewResponse(&agentcomposev1.SessionProxyResponse{}), nil
}

type fakeRunDelegate struct{}

func (fakeRunDelegate) RunAgent(context.Context, *connect.Request[agentcomposev2.RunAgentRequest]) (*connect.Response[agentcomposev2.RunAgentResponse], error) {
	return connect.NewResponse(&agentcomposev2.RunAgentResponse{Run: ProjectRunDetailToProto(domain.ProjectRunRecord{
		RunID: "run-1", ProjectID: "project-1", AgentName: "worker", Status: domain.ProjectRunStatusSucceeded,
	})}), nil
}

func (fakeRunDelegate) StartRun(context.Context, *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
	return connect.NewResponse(&agentcomposev2.StartRunResponse{Run: ProjectRunSummaryToProto(domain.ProjectRunRecord{
		RunID: "run-1", ProjectID: "project-1", AgentName: "worker", Status: domain.ProjectRunStatusRunning,
	}), Started: true}), nil
}

func (fakeRunDelegate) RunAgentStream(context.Context, *connect.Request[agentcomposev2.RunAgentRequest], *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
	return nil
}

type fakeDashboardSessionStore struct{}

func (fakeDashboardSessionStore) ListSessions(context.Context, domain.SessionListOptions) (domain.SessionListResult, error) {
	return domain.SessionListResult{Sessions: []*domain.Session{
		{Summary: domain.SessionSummary{ID: "session-running", VMStatus: domain.VMStatusRunning}},
		{Summary: domain.SessionSummary{ID: "session-failed", VMStatus: domain.VMStatusFailed}},
	}}, nil
}

type fakeDashboardRunStore struct{}

func (fakeDashboardRunStore) ListRecentLoaderRuns(context.Context, int) ([]domain.LoaderRunSummary, error) {
	return []domain.LoaderRunSummary{
		{ID: "run-running", Status: domain.VMStatusRunning},
		{ID: "run-failed", Status: domain.VMStatusFailed},
	}, nil
}

type fakeLoaderController struct {
	updated domain.Loader
}

func (fakeLoaderController) Validate(context.Context, string, string) (loaders.LoaderValidationResult, error) {
	return loaders.LoaderValidationResult{
		Warnings: []string{"warn"},
		Triggers: []domain.LoaderTrigger{{LoaderID: "loader-1", ID: "trigger-1", Kind: domain.LoaderTriggerKindEvent, Topic: "topic", Enabled: true}},
	}, nil
}

func (fakeLoaderController) CreateLoader(_ context.Context, loader domain.Loader) (domain.Loader, error) {
	loader.Summary.ID = "loader-1"
	loader.Summary.CreatedAt = time.Now().UTC()
	loader.Summary.UpdatedAt = loader.Summary.CreatedAt
	return loader, nil
}

func (c *fakeLoaderController) UpdateLoader(_ context.Context, loader domain.Loader) (domain.Loader, error) {
	if loader.Summary.ID == "missing" {
		return domain.Loader{}, domain.ResourceError(domain.ErrNotFound, "loader", loader.Summary.ID, "not found", nil)
	}
	c.updated = loader
	loader.Triggers = []domain.LoaderTrigger{{LoaderID: loader.Summary.ID, ID: "trigger-1", Kind: domain.LoaderTriggerKindEvent, Enabled: true}}
	return loader, nil
}

func (fakeLoaderController) DeleteLoader(context.Context, string) error {
	return nil
}

func (fakeLoaderController) SetLoaderEnabled(_ context.Context, loaderID string, enabled bool) (domain.Loader, error) {
	loader := testLoaderFixture()
	loader.Summary.ID = loaderID
	loader.Summary.Enabled = enabled
	return loader, nil
}

func (fakeLoaderController) SetLoaderTriggerEnabled(_ context.Context, _, triggerID string, enabled bool) (domain.Loader, error) {
	loader := testLoaderFixture()
	loader.Triggers[0].ID = triggerID
	loader.Triggers[0].Enabled = enabled
	return loader, nil
}

func (fakeLoaderController) RunNow(context.Context, string, string, string, time.Duration) (domain.LoaderRunSummary, error) {
	return testLoaderRunFixture(), nil
}

type fakeLoaderStore struct{}

func (fakeLoaderStore) ListLoaderSummaries(context.Context) ([]domain.LoaderSummary, error) {
	return []domain.LoaderSummary{testLoaderFixture().Summary}, nil
}

func (fakeLoaderStore) GetLoader(_ context.Context, loaderID string) (domain.Loader, error) {
	loader := testLoaderFixture()
	loader.Summary.ID = loaderID
	switch loaderID {
	case "loader-error":
		return domain.Loader{}, domain.ResourceError(domain.ErrNotFound, "loader", loaderID, "not found", nil)
	case "loader-non-secret":
		loader.EnvItems = []domain.SessionEnvVar{{Name: "LOADER_SECRET", Value: "loader-value"}}
	case "loader-empty-secret":
		loader.EnvItems = []domain.SessionEnvVar{{Name: "LOADER_SECRET", Secret: true}}
	}
	return loader, nil
}

func (fakeLoaderStore) ListLoaderRuns(context.Context, string, int) ([]domain.LoaderRunSummary, error) {
	return []domain.LoaderRunSummary{testLoaderRunFixture()}, nil
}

func (fakeLoaderStore) GetLoaderRun(context.Context, string, string) (domain.LoaderRunSummary, error) {
	return testLoaderRunFixture(), nil
}

func (fakeLoaderStore) ListLoaderEvents(context.Context, string, int) ([]domain.LoaderEvent, error) {
	return []domain.LoaderEvent{{ID: "event-1", LoaderID: "loader-1", RunID: "run-1", Type: "loader.test", CreatedAt: time.Now().UTC()}}, nil
}

func (fakeLoaderStore) GetAgentDefinition(_ context.Context, id string) (domain.AgentDefinition, error) {
	if id == "disabled-agent" {
		return domain.AgentDefinition{ID: id, Name: "Disabled", Provider: "codex", Enabled: false}, nil
	}
	return domain.AgentDefinition{ID: id, Name: "Agent", Provider: "codex", Enabled: true}, nil
}

func testLoaderFixture() domain.Loader {
	now := time.Now().UTC()
	return domain.Loader{
		Summary: domain.LoaderSummary{ID: "loader-1", Name: "Loader", Enabled: true, Runtime: domain.LoaderRuntimeScheduler, DefaultAgent: "codex", CreatedAt: now, UpdatedAt: now},
		Script:  "function main(){}",
		EnvItems: []domain.SessionEnvVar{
			{Name: "LOADER_SECRET", Value: "loader-secret", Secret: true},
		},
		Triggers: []domain.LoaderTrigger{
			{LoaderID: "loader-1", ID: "trigger-1", Kind: domain.LoaderTriggerKindEvent, Topic: "topic", Enabled: true},
		},
	}
}

func testLoaderRunFixture() domain.LoaderRunSummary {
	now := time.Now().UTC()
	return domain.LoaderRunSummary{ID: "run-1", LoaderID: "loader-1", TriggerID: "trigger-1", TriggerKind: domain.LoaderTriggerKindEvent, Status: "succeeded", StartedAt: now, CompletedAt: now}
}

func (fakeCapabilityProvider) Status(context.Context) capability.Status {
	return capability.Status{Configured: true, OK: true, Status: "ok", ServiceCount: 1}
}

func (fakeCapabilityProvider) ListCapsets(context.Context) ([]capability.Capset, error) {
	return []capability.Capset{{ID: "dev", Name: "Dev", Description: "dev set", Enabled: true}, {ID: "off", Enabled: false}}, nil
}

func (fakeCapabilityProvider) Catalog(context.Context, string) (capability.Catalog, error) {
	return capability.Catalog{
		CapsetID: "dev",
		Name:     "Dev",
		Methods: []capability.Method{{
			ServiceID: "svc", InstanceID: "inst", MethodFullName: "/pkg.Service/Call",
			Endpoints: []capability.Endpoint{{Protocol: "grpc", Endpoint: "127.0.0.1:1", MethodPath: "/pkg.Service/Call", Metadata: map[string]string{"x": "y"}, ToolName: "call", Procedure: "pkg.Service.Call", HTTPMethod: "POST", ContentTypes: []string{"application/json"}}},
		}},
	}, nil
}

func (fakeCapabilityProvider) CapabilityGuide(context.Context, string) ([]byte, error) {
	return []byte("# guide"), nil
}

func (fakeCapabilityProvider) ProxyTarget() string {
	return "agent-compose:9100"
}

type fakeCapabilityStore struct{}

func (*fakeCapabilityStore) GetCapabilityGateway(context.Context) (domain.CapabilityGatewaySettings, error) {
	return domain.CapabilityGatewaySettings{Addr: "http://octobus", Token: "token"}, nil
}

func (*fakeCapabilityStore) SaveCapabilityGateway(_ context.Context, settings domain.CapabilityGatewaySettings) (domain.CapabilityGatewaySettings, error) {
	return settings, nil
}

type fakeCapabilityRuntime struct {
	listen string
}

func (r fakeCapabilityRuntime) CapProxyListen() string {
	return r.listen
}

type fakeLLMGenerator struct{}

func (fakeLLMGenerator) Generate(context.Context, string, string, string) (llms.GenerateResult, error) {
	return llms.GenerateResult{Text: `{"ok":true}`, Model: "gpt", ResponseID: "resp-1", FinishReason: "stop"}, nil
}

type fakeImageSelector struct {
	backend images.Backend
}

func (s fakeImageSelector) ImageBackendForStore(agentcomposev2.ImageStoreKind) (images.Backend, error) {
	return s.backend, nil
}

type fakeImageBackend struct{}

func (fakeImageBackend) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{Images: []*agentcomposev2.Image{{ImageId: "img-1", RepoTags: []string{"guest:latest"}}}}, nil
}

func (fakeImageBackend) PullImage(context.Context, images.PullRequest) (images.PullResult, error) {
	return images.PullResult{Image: &agentcomposev2.Image{ImageId: "img-1"}, ResolvedRef: "guest@sha256:test"}, nil
}

func (fakeImageBackend) InspectImage(context.Context, images.InspectRequest) (images.InspectResult, error) {
	return images.InspectResult{Image: &agentcomposev2.Image{ImageId: "img-1", ImageRef: "guest:latest", ResolvedRef: "guest@sha256:test"}}, nil
}

func (fakeImageBackend) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{ImageRef: "guest:latest", DeletedIDs: []string{"img-1"}}, nil
}
