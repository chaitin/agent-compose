package agents

import (
	"context"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/model"
	"agent-compose/pkg/storage"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

type fakeAgentSessionHandler struct {
	store      *Store
	createReqs []*agentcomposev1.CreateSessionRequest
	stopCalls  []string
}

func (h *fakeAgentSessionHandler) CreateSession(ctx context.Context, req *connect.Request[agentcomposev1.CreateSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	h.createReqs = append(h.createReqs, req.Msg)
	envItems := make([]model.SessionEnvVar, 0, len(req.Msg.GetEnvItems()))
	for _, item := range req.Msg.GetEnvItems() {
		envItems = append(envItems, model.SessionEnvVar{Name: item.GetName(), Value: item.GetValue(), Secret: item.GetSecret()})
	}
	tags := make([]model.SessionTag, 0, len(req.Msg.GetTags()))
	for _, item := range req.Msg.GetTags() {
		tags = append(tags, model.SessionTag{Name: item.GetName(), Value: item.GetValue()})
	}
	session, err := h.store.CreateSession(ctx, req.Msg.GetTitle(), "", req.Msg.GetDriver(), req.Msg.GetGuestImage(), req.Msg.GetWorkspaceId(), "", nil, envItems, tags)
	if err != nil {
		return nil, err
	}
	session.Summary.VMStatus = VMStatusRunning
	if err := h.store.UpdateSession(ctx, session); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev1.SessionResponse{Session: &agentcomposev1.SessionDetail{
		Summary: &agentcomposev1.SessionSummary{
			SessionId: session.Summary.ID,
			Title:     session.Summary.Title,
			VmStatus:  VMStatusRunning,
			Tags:      req.Msg.GetTags(),
		},
		EnvItems: req.Msg.GetEnvItems(),
	}}), nil
}

func (h *fakeAgentSessionHandler) StopSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	h.stopCalls = append(h.stopCalls, req.Msg.GetSessionId())
	session, err := h.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	session.Summary.VMStatus = VMStatusStopped
	if err := h.store.UpdateSession(ctx, session); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev1.SessionResponse{Session: &agentcomposev1.SessionDetail{
		Summary: &agentcomposev1.SessionSummary{
			SessionId: session.Summary.ID,
			Title:     session.Summary.Title,
			VmStatus:  VMStatusStopped,
		},
	}}), nil
}

func newAgentServiceTestHarness(t *testing.T) (*Service, *ConfigStore, *Store, *fakeAgentSessionHandler) {
	t.Helper()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:      filepath.Join(root, "data"),
		DbAddr:        filepath.Join(root, "data", "data.db"),
		SessionRoot:   filepath.Join(root, "sessions"),
		RuntimeDriver: driverpkg.RuntimeDriverBoxlite,
		DefaultImage:  "guest:latest",
	}
	configDB, err := storage.NewConfigStoreFromConfig(config)
	if err != nil {
		t.Fatalf("NewConfigStoreFromConfig returned error: %v", err)
	}
	t.Cleanup(func() { _ = configDB.DB().Close() })
	store, err := storage.NewStoreFromConfig(config)
	if err != nil {
		t.Fatalf("NewStoreFromConfig returned error: %v", err)
	}
	sessions := &fakeAgentSessionHandler{store: store}
	return NewService(config, store, configDB, sessions, nil), configDB, store, sessions
}

func TestAgentDefinitionValidationAndProtoMapping(t *testing.T) {
	ctx := context.Background()
	service, configDB, _, _ := newAgentServiceTestHarness(t)
	workspace, err := configDB.CreateWorkspaceConfig(ctx, WorkspaceConfig{
		Name:       "Files",
		Type:       "file",
		ConfigJSON: "{}",
		Comment:    "uploaded docs",
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
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
	agent, err := configDB.CreateAgentDefinition(ctx, AgentDefinition{
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
	ctx := context.Background()
	service, _, _, sessions := newAgentServiceTestHarness(t)
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
	if summary.GetTitle() != "Runner 工作会话" || len(sessions.createReqs) != 1 {
		t.Fatalf("session summary = %+v createReqs=%v", summary, sessions.createReqs)
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

func TestAgentDefinitionCreateSessionUsesDefinitionCapsets(t *testing.T) {
	ctx := context.Background()
	service, _, _, sessions := newAgentServiceTestHarness(t)
	created, err := service.CreateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.CreateAgentDefinitionRequest{
		Name:      "Capability Runner",
		Enabled:   true,
		Provider:  "codex",
		CapsetIds: []string{"dev"},
	}))
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	if _, err := service.CreateAgentSession(ctx, connect.NewRequest(&agentcomposev1.CreateAgentSessionRequest{
		AgentId: created.Msg.GetAgent().GetAgentId(),
		Title:   "uses definition capsets",
	})); err != nil {
		t.Fatalf("CreateAgentSession returned error: %v", err)
	}
	if len(sessions.createReqs) != 1 {
		t.Fatalf("create requests = %d, want 1", len(sessions.createReqs))
	}
	if capsets := sessions.createReqs[0].GetCapsetIds(); len(capsets) != 1 || capsets[0] != "dev" {
		t.Fatalf("session capsets = %+v, want [dev]", capsets)
	}
}

func TestDeleteAgentDefinitionStopsSessionsAndKeepsDeletedInList(t *testing.T) {
	ctx := context.Background()
	service, _, store, sessions := newAgentServiceTestHarness(t)
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
	if len(sessions.stopCalls) != 1 || sessions.stopCalls[0] != sessionID {
		t.Fatalf("stop calls = %#v, want [%s]", sessions.stopCalls, sessionID)
	}
	session, err := store.GetSession(ctx, sessionID)
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
