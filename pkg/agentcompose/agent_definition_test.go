package agentcompose

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	loaderspkg "agent-compose/pkg/loaders"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func TestAgentDefinitionConfigStoreCRUDAndWorkspaceProtection(t *testing.T) {
	testAgentDefinitionConfigStoreCRUDAndWorkspaceProtection(t)
}

func testAgentDefinitionConfigStoreCRUDAndWorkspaceProtection(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := newTestConfigStore(t)
	workspace, err := store.CreateWorkspaceConfig(ctx, WorkspaceConfig{
		Name:       "Agent Files",
		Type:       "git",
		ConfigJSON: `{"repo_url":"https://example.com/repo.git","branch":"main"}`,
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}
	created, err := store.CreateAgentDefinition(ctx, AgentDefinition{
		ID:          "agent-1",
		Name:        " Agent One ",
		Enabled:     true,
		WorkspaceID: workspace.ID,
		EnvItems: []SessionEnvVar{
			{Name: " B ", Value: "2"},
			{Name: "A", Value: "1"},
			{Name: "B", Value: "3"},
			{Name: " ", Value: "skip"},
		},
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	if created.Provider != defaultAgentProvider || created.ConfigJSON != "{}" {
		t.Fatalf("defaults = provider %q config %q", created.Provider, created.ConfigJSON)
	}
	if len(created.EnvItems) != 2 || created.EnvItems[0].Name != "A" || created.EnvItems[1].Value != "3" {
		t.Fatalf("env items = %#v", created.EnvItems)
	}
	if err := store.DeleteWorkspaceConfig(ctx, workspace.ID); err == nil || !strings.Contains(err.Error(), "referenced by") {
		t.Fatalf("DeleteWorkspaceConfig error = %v, want referenced by", err)
	}
	listed, err := store.ListAgentDefinitions(ctx, AgentDefinitionListOptions{IncludeDisabled: true})
	if err != nil {
		t.Fatalf("ListAgentDefinitions returned error: %v", err)
	}
	if listed.TotalCount != 1 || len(listed.Agents) != 1 {
		t.Fatalf("listed = %#v", listed)
	}
	updated := listed.Agents[0]
	updated.Name = "Agent Renamed"
	updated.Enabled = false
	saved, err := store.UpdateAgentDefinition(ctx, updated)
	if err != nil {
		t.Fatalf("UpdateAgentDefinition returned error: %v", err)
	}
	if saved.CreatedAt.IsZero() || !saved.UpdatedAt.After(saved.CreatedAt) {
		t.Fatalf("timestamps after update = created %s updated %s", saved.CreatedAt, saved.UpdatedAt)
	}
	enabled, err := store.SetAgentDefinitionEnabled(ctx, saved.ID, true)
	if err != nil {
		t.Fatalf("SetAgentDefinitionEnabled returned error: %v", err)
	}
	if !enabled.Enabled {
		t.Fatalf("enabled flag false")
	}
	if err := store.DeleteAgentDefinition(ctx, enabled.ID); err != nil {
		t.Fatalf("DeleteAgentDefinition returned error: %v", err)
	}
	if _, err := store.GetAgentDefinition(ctx, enabled.ID); err == nil {
		t.Fatalf("expected deleted agent get to fail")
	}
	if err := store.DeleteWorkspaceConfig(ctx, workspace.ID); err != nil {
		t.Fatalf("DeleteWorkspaceConfig after agent delete returned error: %v", err)
	}
}

func TestLoaderCreateBindsAgentDefinitionProvider(t *testing.T) {
	testLoaderCreateBindsAgentDefinitionProvider(t)
}

func testLoaderCreateBindsAgentDefinitionProvider(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := newTestConfigStore(t)
	agent, err := store.CreateAgentDefinition(ctx, AgentDefinition{
		ID:          "agent-loader",
		Name:        "Loader Agent",
		Enabled:     true,
		Provider:    "gemini",
		Driver:      driverpkg.RuntimeDriverDocker,
		GuestImage:  "agent-guest:latest",
		WorkspaceID: "",
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	manager := newTestLoaderManager(t, loaderspkg.ManagerDeps{
		ConfigDB: store,
		Engine:   &QJSLoaderEngine{},
	})
	service := &Service{configDB: store, loaders: manager}
	created, err := service.CreateLoader(ctx, connect.NewRequest(&agentcomposev1.CreateLoaderRequest{
		Name:              "Bound Loader",
		Runtime:           LoaderRuntimeScheduler,
		Script:            `scheduler.interval("tick", function(){ scheduler.log("tick"); }, 60000);`,
		AgentId:           agent.ID,
		DefaultAgent:      "codex",
		SessionPolicy:     LoaderSessionPolicyNew,
		ConcurrencyPolicy: LoaderConcurrencyPolicySkip,
		Enabled:           true,
	}))
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}
	summary := created.Msg.GetLoader().GetSummary()
	if summary.GetAgentId() != agent.ID {
		t.Fatalf("loader agent id = %q, want %q", summary.GetAgentId(), agent.ID)
	}
	if summary.GetDefaultAgent() != "gemini" {
		t.Fatalf("loader default agent = %q, want gemini", summary.GetDefaultAgent())
	}
}

func TestAgentDefinitionValidationAndProtoMapping(t *testing.T) {
	testAgentDefinitionValidationAndProtoMapping(t)
}

func testAgentDefinitionValidationAndProtoMapping(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := newTestConfigStore(t)
	workspace, err := store.CreateWorkspaceConfig(ctx, WorkspaceConfig{
		Name:       "Files",
		Type:       "file",
		ConfigJSON: "{}",
		Comment:    "uploaded docs",
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}
	service := &Service{
		config:   &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverBoxlite, DefaultImage: "guest:latest"},
		configDB: store,
		store: mustTestStore(t, &appconfig.Config{
			SessionRoot:   t.TempDir(),
			RuntimeDriver: driverpkg.RuntimeDriverBoxlite,
			DefaultImage:  "guest:latest",
		}),
	}
	validated, err := service.ValidateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.ValidateAgentDefinitionRequest{
		Name:        "Agent",
		WorkspaceId: workspace.ID,
		ConfigJson:  "{}",
	}))
	if err != nil {
		t.Fatalf("ValidateAgentDefinition returned error: %v", err)
	}
	if validated.Msg.GetAvailabilityStatus() != agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_AVAILABLE {
		t.Fatalf("availability = %s", validated.Msg.GetAvailabilityStatus())
	}
	invalid, err := service.ValidateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.ValidateAgentDefinitionRequest{
		Name:           "Agent",
		RuntimeImageId: "runtime-1",
		ConfigJson:     "[]",
	}))
	if err != nil {
		t.Fatalf("ValidateAgentDefinition invalid returned connect error: %v", err)
	}
	if invalid.Msg.GetAvailabilityStatus() != agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_VALIDATION_FAILED || len(invalid.Msg.GetErrors()) == 0 {
		t.Fatalf("invalid validation = %+v", invalid.Msg)
	}
	agent, err := store.CreateAgentDefinition(ctx, AgentDefinition{
		ID:          "agent-map",
		Name:        "Mapper",
		Enabled:     true,
		WorkspaceID: workspace.ID,
		ConfigJSON:  "{}",
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	protoAgent, connectErr := service.agentDefinitionToProto(ctx, agent)
	if connectErr != nil {
		t.Fatalf("agentDefinitionToProto returned error: %v", connectErr)
	}
	if protoAgent.GetWorkFiles().GetSource() != agentcomposev1.AgentWorkFilesSource_AGENT_WORK_FILES_SOURCE_FILE_WORKSPACE {
		t.Fatalf("work file source = %s", protoAgent.GetWorkFiles().GetSource())
	}
	if protoAgent.GetCurrentRunSummary().GetText() != "空闲" || protoAgent.GetRuntimeImageId() != "" {
		t.Fatalf("proto agent = %+v", protoAgent)
	}
	agent.Enabled = false
	disabled, connectErr := service.agentDefinitionToProto(ctx, agent)
	if connectErr != nil {
		t.Fatalf("agentDefinitionToProto disabled returned error: %v", connectErr)
	}
	if disabled.GetAvailabilityStatus() != agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_UNAVAILABLE || disabled.GetHealthStatus() != agentcomposev1.AgentHealthStatus_AGENT_HEALTH_STATUS_AT_RISK {
		t.Fatalf("disabled statuses = %s/%s", disabled.GetAvailabilityStatus(), disabled.GetHealthStatus())
	}
}

func TestAgentDefinitionCreateSession(t *testing.T) {
	testAgentDefinitionCreateSession(t)
}

func TestAgentDefinitionCreateSessionUsesDefinitionCapsets(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newTestServiceAPIHarness(t)
	service.sessions.cap = newTestCapabilityProvider("", "agent-compose:9100")
	created, err := service.CreateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.CreateAgentDefinitionRequest{
		Name:      "Capability Runner",
		Enabled:   true,
		Provider:  "codex",
		CapsetIds: []string{"dev"},
	}))
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	sessionResp, err := service.CreateAgentSession(ctx, connect.NewRequest(&agentcomposev1.CreateAgentSessionRequest{
		AgentId: created.Msg.GetAgent().GetAgentId(),
		Title:   "uses definition capsets",
	}))
	if err != nil {
		t.Fatalf("CreateAgentSession returned error: %v", err)
	}
	session, err := service.store.GetSession(ctx, sessionResp.Msg.GetSession().GetSummary().GetSessionId())
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if capsets := sessionCapabilityCapsets(session); len(capsets) != 1 || capsets[0] != "dev" {
		t.Fatalf("session capsets = %+v, want [dev]", capsets)
	}
	env := map[string]string{}
	for _, item := range session.EnvItems {
		env[item.Name] = item.Value
	}
	if env[capProxyTargetEnvName] != "agent-compose:9100" || env[capabilitySessionTokenEnvName] == "" {
		t.Fatalf("session capability env = %+v", env)
	}
}

func TestAgentSessionMessageUsesDefinitionProvider(t *testing.T) {
	ctx := context.Background()
	service, runtime, _ := newTestServiceAPIHarness(t)
	created, err := service.CreateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.CreateAgentDefinitionRequest{
		Name:         "Claude Runner",
		Enabled:      true,
		Provider:     "open-code",
		Model:        "unused-model-field",
		SystemPrompt: "system body",
		EnvItems: []*agentcomposev1.SessionEnvVar{
			{Name: "OPENCODE_MODEL", Value: "anthropic/claude-sonnet-4-5"},
		},
	}))
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	sessionResp, err := service.CreateAgentSession(ctx, connect.NewRequest(&agentcomposev1.CreateAgentSessionRequest{
		AgentId: created.Msg.GetAgent().GetAgentId(),
	}))
	if err != nil {
		t.Fatalf("CreateAgentSession returned error: %v", err)
	}
	sessionID := sessionResp.Msg.GetSession().GetSummary().GetSessionId()
	_, err = service.SendAgentMessage(ctx, connect.NewRequest(&agentcomposev1.SendAgentMessageRequest{
		SessionId: sessionID,
		Agent:     "codex",
		Message:   "summarize",
	}))
	if err != nil {
		t.Fatalf("SendAgentMessage returned error: %v", err)
	}
	if len(runtime.providers) != 1 || runtime.providers[0] != "opencode" {
		t.Fatalf("runtime providers = %v, want [opencode]", runtime.providers)
	}
	if len(runtime.agentSpecs) != 1 {
		t.Fatalf("runtime agent specs = %d, want 1", len(runtime.agentSpecs))
	}
	command := strings.Join(runtime.agentSpecs[0].Args, " ")
	for _, want := range []string{"--provider 'opencode'", "--model 'anthropic/claude-sonnet-4-5'"} {
		if !strings.Contains(command, want) {
			t.Fatalf("agent command %q does not contain %q", command, want)
		}
	}
	if strings.Contains(command, "--system-prompt-file") {
		t.Fatalf("agent command %q contains deprecated --system-prompt-file flag", command)
	}
}

func TestAgentSessionMessageUsesDefinitionProviderEnvForFacade(t *testing.T) {
	ctx := context.Background()
	service, runtime, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	created, err := service.CreateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.CreateAgentDefinitionRequest{
		Name:     "Claude Env Runner",
		Enabled:  true,
		Provider: "claude",
		EnvItems: []*agentcomposev1.SessionEnvVar{
			{Name: "ANTHROPIC_API_KEY", Value: "agent-anthropic-key", Secret: true},
			{Name: "ANTHROPIC_BASE_URL", Value: "https://anthropic.example.invalid"},
			{Name: "ANTHROPIC_MODEL", Value: "claude-test"},
		},
	}))
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	sessionResp, err := service.CreateAgentSession(ctx, connect.NewRequest(&agentcomposev1.CreateAgentSessionRequest{
		AgentId: created.Msg.GetAgent().GetAgentId(),
	}))
	if err != nil {
		t.Fatalf("CreateAgentSession returned error: %v", err)
	}
	sessionID := sessionResp.Msg.GetSession().GetSummary().GetSessionId()
	createdSession, err := service.store.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if env := sessionEnvMap(createdSession.EnvItems); env["ANTHROPIC_API_KEY"] != "" {
		t.Fatalf("ANTHROPIC_API_KEY persisted in session env: %#v", createdSession.EnvItems)
	}
	if len(createdSession.ProviderEnvItems) != 0 {
		t.Fatalf("ProviderEnvItems unexpectedly set before execution: %#v", createdSession.ProviderEnvItems)
	}
	storedAgent, err := service.configDB.GetAgentDefinition(ctx, created.Msg.GetAgent().GetAgentId())
	if err != nil {
		t.Fatalf("GetAgentDefinition returned error: %v", err)
	}
	if env := sessionEnvMap(storedAgent.EnvItems); env["ANTHROPIC_API_KEY"] != "agent-anthropic-key" {
		t.Fatalf("stored agent env missing key: %#v", storedAgent.EnvItems)
	}
	agentConfig := service.resolveSessionAgentConfig(ctx, createdSession, "codex")
	if env := sessionEnvMap(agentConfig.EnvItems); env["ANTHROPIC_API_KEY"] != "agent-anthropic-key" || agentConfig.Provider != "claude" {
		t.Fatalf("resolved agent config = %#v env=%#v", agentConfig, agentConfig.EnvItems)
	}
	_, err = service.SendAgentMessage(ctx, connect.NewRequest(&agentcomposev1.SendAgentMessageRequest{
		SessionId: sessionID,
		Agent:     "codex",
		Message:   "hello",
	}))
	if err != nil {
		t.Fatalf("SendAgentMessage returned error: %v", err)
	}
	if len(createdSession.ProviderEnvItems) != 0 {
		t.Fatalf("SendAgentMessage mutated source session ProviderEnvItems: %#v", createdSession.ProviderEnvItems)
	}
	if len(runtime.agentSpecs) != 1 {
		t.Fatalf("runtime agent specs = %d, want 1", len(runtime.agentSpecs))
	}
	env := runtime.agentSpecs[0].Env
	token := env["AGENT_COMPOSE_SESSION_TOKEN"]
	if token == "" {
		t.Fatalf("agent exec env missing facade token: providers=%v env=%#v", runtime.providers, env)
	}
	if env["ANTHROPIC_API_KEY"] != token {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want facade token", env["ANTHROPIC_API_KEY"])
	}
	if env["ANTHROPIC_BASE_URL"] != "http://agent-compose.test/api/runtime/sessions/"+sessionID+"/llm/anthropic" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", env["ANTHROPIC_BASE_URL"])
	}
	if env["ANTHROPIC_MODEL"] != "claude-test" {
		t.Fatalf("ANTHROPIC_MODEL = %q, want claude-test", env["ANTHROPIC_MODEL"])
	}
}

func testAgentDefinitionCreateSession(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	service, _, driver := newTestServiceAPIHarness(t)
	created, err := service.CreateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.CreateAgentDefinitionRequest{
		Name:     "Runner",
		Enabled:  true,
		Provider: "claude",
		EnvItems: []*agentcomposev1.SessionEnvVar{
			{Name: "A", Value: "agent"},
			{Name: "B", Value: "agent"},
		},
	}))
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	sessionResp, err := service.CreateAgentSession(ctx, connect.NewRequest(&agentcomposev1.CreateAgentSessionRequest{
		AgentId: created.Msg.GetAgent().GetAgentId(),
		EnvItems: []*agentcomposev1.SessionEnvVar{
			{Name: "A", Value: "request"},
		},
	}))
	if err != nil {
		t.Fatalf("CreateAgentSession returned error: %v", err)
	}
	summary := sessionResp.Msg.GetSession().GetSummary()
	if summary.GetTitle() != "Runner 工作会话" || len(driver.startCalls) != 1 {
		t.Fatalf("session summary = %+v startCalls=%v", summary, driver.startCalls)
	}
	tags := map[string]string{}
	for _, tag := range summary.GetTags() {
		tags[tag.GetName()] = tag.GetValue()
	}
	if tags[agentSessionTagSource] != agentSessionTagSourceVal || tags[agentSessionTagID] != created.Msg.GetAgent().GetAgentId() || tags[agentSessionTagName] != "Runner" {
		t.Fatalf("agent tags = %#v", tags)
	}
	if len(sessionResp.Msg.GetSession().GetEnvItems()) != 2 || sessionResp.Msg.GetSession().GetEnvItems()[0].GetValue() != "request" {
		t.Fatalf("session env = %+v", sessionResp.Msg.GetSession().GetEnvItems())
	}
	listed, err := service.ListAgentDefinitions(ctx, connect.NewRequest(&agentcomposev1.ListAgentDefinitionsRequest{IncludeDisabled: true}))
	if err != nil {
		t.Fatalf("ListAgentDefinitions returned error: %v", err)
	}
	if listed.Msg.GetAgents()[0].GetCurrentRunSummary().GetStatus() != agentcomposev1.AgentCurrentRunStatus_AGENT_CURRENT_RUN_STATUS_HAS_RUNNING_SESSION {
		t.Fatalf("current run summary = %+v", listed.Msg.GetAgents()[0].GetCurrentRunSummary())
	}
	if listed.Msg.GetAgents()[0].GetLatestRunSummary().GetRunType() != "work_session" {
		t.Fatalf("latest run summary = %+v", listed.Msg.GetAgents()[0].GetLatestRunSummary())
	}
	got, err := service.GetAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.AgentDefinitionIDRequest{AgentId: created.Msg.GetAgent().GetAgentId()}))
	if err != nil {
		t.Fatalf("GetAgentDefinition returned error: %v", err)
	}
	if got.Msg.GetAgent().GetName() != "Runner" {
		t.Fatalf("got agent = %+v", got.Msg.GetAgent())
	}
	disabled, err := service.SetAgentDefinitionEnabled(ctx, connect.NewRequest(&agentcomposev1.SetAgentDefinitionEnabledRequest{
		AgentId: created.Msg.GetAgent().GetAgentId(),
		Enabled: false,
	}))
	if err != nil {
		t.Fatalf("SetAgentDefinitionEnabled returned error: %v", err)
	}
	if disabled.Msg.GetAgent().GetEnabled() {
		t.Fatalf("agent was not disabled: %+v", disabled.Msg.GetAgent())
	}
}

func TestDeleteAgentDefinitionStopsSessionsAndKeepsDeletedInList(t *testing.T) {
	testDeleteAgentDefinitionStopsSessionsAndKeepsDeletedInList(t)
}

func testDeleteAgentDefinitionStopsSessionsAndKeepsDeletedInList(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	service, _, driver := newTestServiceAPIHarness(t)
	created, err := service.CreateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.CreateAgentDefinitionRequest{
		Name:     "Delete Me",
		Enabled:  true,
		Provider: "codex",
	}))
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	sessionResp, err := service.CreateAgentSession(ctx, connect.NewRequest(&agentcomposev1.CreateAgentSessionRequest{
		AgentId: created.Msg.GetAgent().GetAgentId(),
	}))
	if err != nil {
		t.Fatalf("CreateAgentSession returned error: %v", err)
	}
	sessionID := sessionResp.Msg.GetSession().GetSummary().GetSessionId()
	if _, err := service.DeleteAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.AgentDefinitionIDRequest{AgentId: created.Msg.GetAgent().GetAgentId()})); err != nil {
		t.Fatalf("DeleteAgentDefinition returned error: %v", err)
	}
	if len(driver.stopCalls) != 1 || driver.stopCalls[0] != sessionID {
		t.Fatalf("stop calls = %#v, want [%s]", driver.stopCalls, sessionID)
	}
	session, err := service.store.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if session.Summary.VMStatus != VMStatusStopped {
		t.Fatalf("session status = %q, want %q", session.Summary.VMStatus, VMStatusStopped)
	}
	listed, err := service.ListAgentDefinitions(ctx, connect.NewRequest(&agentcomposev1.ListAgentDefinitionsRequest{IncludeDisabled: true}))
	if err != nil {
		t.Fatalf("ListAgentDefinitions returned error: %v", err)
	}
	if len(listed.Msg.GetAgents()) != 1 || listed.Msg.GetAgents()[0].GetDeletedAt() == "" {
		t.Fatalf("listed deleted agents = %+v", listed.Msg.GetAgents())
	}
	if listed.Msg.GetAgents()[0].GetAvailabilityStatus() != agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_UNAVAILABLE {
		t.Fatalf("deleted availability = %s", listed.Msg.GetAgents()[0].GetAvailabilityStatus())
	}
}
