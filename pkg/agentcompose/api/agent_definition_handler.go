package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const AgentSessionScanLimit = 1 << 30

type AgentDefinitionConfigStore interface {
	ListAgentDefinitions(context.Context, domain.AgentDefinitionListOptions) (domain.AgentDefinitionListResult, error)
	GetAgentDefinition(context.Context, string) (domain.AgentDefinition, error)
	GetAgentDefinitionIncludingDeleted(context.Context, string) (domain.AgentDefinition, error)
	CreateAgentDefinition(context.Context, domain.AgentDefinition) (domain.AgentDefinition, error)
	UpdateAgentDefinition(context.Context, domain.AgentDefinition) (domain.AgentDefinition, error)
	DeleteAgentDefinition(context.Context, string) error
	SetAgentDefinitionEnabled(context.Context, string, bool) (domain.AgentDefinition, error)
	DisableLoadersByDefaultAgent(context.Context, string) (int, error)
	GetWorkspaceConfig(context.Context, string) (domain.WorkspaceConfig, error)
}

type AgentDefinitionSessionStore interface {
	ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error)
	GetVMState(string) (domain.VMState, error)
	SaveVMState(string, domain.VMState) error
	UpdateSandbox(context.Context, *domain.Sandbox) error
	AddEvent(context.Context, string, domain.SandboxEvent) error
}

type AgentDefinitionHandler struct {
	config   *appconfig.Config
	store    AgentDefinitionSessionStore
	configDB AgentDefinitionConfigStore
	sessions SessionDelegate
	streams  *sessions.StreamBroker
}

func NewAgentDefinitionHandler(config *appconfig.Config, store AgentDefinitionSessionStore, configDB AgentDefinitionConfigStore, sessions SessionDelegate, streams *sessions.StreamBroker) *AgentDefinitionHandler {
	return &AgentDefinitionHandler{config: config, store: store, configDB: configDB, sessions: sessions, streams: streams}
}

func (h *AgentDefinitionHandler) ListAgentDefinitions(ctx context.Context, req *connect.Request[agentcomposev1.ListAgentDefinitionsRequest]) (*connect.Response[agentcomposev1.ListAgentDefinitionsResponse], error) {
	result, err := h.configDB.ListAgentDefinitions(ctx, domain.AgentDefinitionListOptions{
		Query:           req.Msg.GetQuery(),
		IncludeDisabled: req.Msg.GetIncludeDisabled(),
		Offset:          int(req.Msg.GetOffset()),
		Limit:           int(req.Msg.GetLimit()),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListAgentDefinitionsResponse{
		TotalCount: uint32(result.TotalCount),
		HasMore:    result.HasMore,
		NextOffset: uint32(result.NextOffset),
	}
	sessions, err := h.listAllSessions(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	for _, item := range result.Agents {
		resp.Agents = append(resp.Agents, h.agentDefinitionToProtoWith(ctx, item, sessions))
	}
	return connect.NewResponse(resp), nil
}

func (h *AgentDefinitionHandler) GetAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.AgentDefinitionIDRequest]) (*connect.Response[agentcomposev1.AgentDefinitionResponse], error) {
	id := strings.TrimSpace(req.Msg.GetAgentId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition id is required"))
	}
	item, err := h.configDB.GetAgentDefinitionIncludingDeleted(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	protoAgent, connectErr := h.agentDefinitionToProto(ctx, item)
	if connectErr != nil {
		return nil, connectErr
	}
	return connect.NewResponse(&agentcomposev1.AgentDefinitionResponse{Agent: protoAgent}), nil
}

func (h *AgentDefinitionHandler) CreateAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.CreateAgentDefinitionRequest]) (*connect.Response[agentcomposev1.AgentDefinitionResponse], error) {
	item := domain.AgentDefinition{
		ID:           uuid.NewString(),
		Name:         req.Msg.GetName(),
		Description:  req.Msg.GetDescription(),
		Enabled:      req.Msg.GetEnabled(),
		Provider:     req.Msg.GetProvider(),
		Model:        req.Msg.GetModel(),
		SystemPrompt: req.Msg.GetSystemPrompt(),
		Driver:       req.Msg.GetDriver(),
		GuestImage:   req.Msg.GetGuestImage(),
		WorkspaceID:  req.Msg.GetWorkspaceId(),
		EnvItems:     EnvItemsFromProto(req.Msg.GetEnvItems()),
		ConfigJSON:   req.Msg.GetConfigJson(),
		CapsetIDs:    req.Msg.GetCapsetIds(),
	}
	if err := h.validateAgentDefinitionInput(ctx, item, req.Msg.GetRuntimeImageId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	saved, err := h.configDB.CreateAgentDefinition(ctx, item)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	protoAgent, connectErr := h.agentDefinitionToProto(ctx, saved)
	if connectErr != nil {
		return nil, connectErr
	}
	return connect.NewResponse(&agentcomposev1.AgentDefinitionResponse{Agent: protoAgent}), nil
}

func (h *AgentDefinitionHandler) UpdateAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.UpdateAgentDefinitionRequest]) (*connect.Response[agentcomposev1.AgentDefinitionResponse], error) {
	id := strings.TrimSpace(req.Msg.GetAgentId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition id is required"))
	}
	if _, err := h.configDB.GetAgentDefinition(ctx, id); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	item := domain.AgentDefinition{
		ID:           id,
		Name:         req.Msg.GetName(),
		Description:  req.Msg.GetDescription(),
		Enabled:      req.Msg.GetEnabled(),
		Provider:     req.Msg.GetProvider(),
		Model:        req.Msg.GetModel(),
		SystemPrompt: req.Msg.GetSystemPrompt(),
		Driver:       req.Msg.GetDriver(),
		GuestImage:   req.Msg.GetGuestImage(),
		WorkspaceID:  req.Msg.GetWorkspaceId(),
		EnvItems:     EnvItemsFromProto(req.Msg.GetEnvItems()),
		ConfigJSON:   req.Msg.GetConfigJson(),
		CapsetIDs:    req.Msg.GetCapsetIds(),
	}
	if err := h.validateAgentDefinitionInput(ctx, item, req.Msg.GetRuntimeImageId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	saved, err := h.configDB.UpdateAgentDefinition(ctx, item)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	protoAgent, connectErr := h.agentDefinitionToProto(ctx, saved)
	if connectErr != nil {
		return nil, connectErr
	}
	return connect.NewResponse(&agentcomposev1.AgentDefinitionResponse{Agent: protoAgent}), nil
}

func (h *AgentDefinitionHandler) DeleteAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.AgentDefinitionIDRequest]) (*connect.Response[emptypb.Empty], error) {
	id := strings.TrimSpace(req.Msg.GetAgentId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition id is required"))
	}
	if _, err := h.configDB.GetAgentDefinition(ctx, id); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if err := h.stopAgentSessions(ctx, id); err != nil {
		return nil, err
	}
	if _, err := h.configDB.DisableLoadersByDefaultAgent(ctx, id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := h.configDB.DeleteAgentDefinition(ctx, id); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *AgentDefinitionHandler) SetAgentDefinitionEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetAgentDefinitionEnabledRequest]) (*connect.Response[agentcomposev1.AgentDefinitionResponse], error) {
	id := strings.TrimSpace(req.Msg.GetAgentId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition id is required"))
	}
	item, err := h.configDB.GetAgentDefinition(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if req.Msg.GetEnabled() {
		if err := h.validateAgentDefinitionInput(ctx, item, ""); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	saved, err := h.configDB.SetAgentDefinitionEnabled(ctx, id, req.Msg.GetEnabled())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	protoAgent, connectErr := h.agentDefinitionToProto(ctx, saved)
	if connectErr != nil {
		return nil, connectErr
	}
	return connect.NewResponse(&agentcomposev1.AgentDefinitionResponse{Agent: protoAgent}), nil
}

func (h *AgentDefinitionHandler) ValidateAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.ValidateAgentDefinitionRequest]) (*connect.Response[agentcomposev1.ValidateAgentDefinitionResponse], error) {
	item := domain.AgentDefinition{
		ID:           firstNonEmpty(strings.TrimSpace(req.Msg.GetAgentId()), "validate-agent"),
		Name:         req.Msg.GetName(),
		Provider:     req.Msg.GetProvider(),
		Model:        req.Msg.GetModel(),
		SystemPrompt: req.Msg.GetSystemPrompt(),
		Driver:       req.Msg.GetDriver(),
		GuestImage:   req.Msg.GetGuestImage(),
		WorkspaceID:  req.Msg.GetWorkspaceId(),
		EnvItems:     EnvItemsFromProto(req.Msg.GetEnvItems()),
		ConfigJSON:   req.Msg.GetConfigJson(),
		Enabled:      true,
	}
	result := h.validateAgentDefinition(ctx, item, req.Msg.GetRuntimeImageId())
	return connect.NewResponse(&agentcomposev1.ValidateAgentDefinitionResponse{
		AvailabilityStatus: result.Availability,
		HealthStatus:       result.Health,
		Warnings:           result.Warnings,
		Errors:             result.Errors,
	}), nil
}

func (h *AgentDefinitionHandler) CreateAgentSession(ctx context.Context, req *connect.Request[agentcomposev1.CreateAgentSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	id := strings.TrimSpace(req.Msg.GetAgentId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition id is required"))
	}
	agent, err := h.configDB.GetAgentDefinition(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if !agent.Enabled {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition %s is disabled", id))
	}
	validation := h.validateAgentDefinition(ctx, agent, "")
	if validation.Availability != agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_AVAILABLE {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition %s is not available: %s", id, strings.Join(validation.Errors, "; ")))
	}
	workspaceID := firstNonEmpty(strings.TrimSpace(req.Msg.GetWorkspaceId()), agent.WorkspaceID)
	if err := h.validateAgentWorkspace(ctx, workspaceID); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	driver := firstNonEmpty(strings.TrimSpace(req.Msg.GetDriver()), agent.Driver)
	if strings.TrimSpace(driver) != "" {
		if _, err := driverpkg.ResolveSandboxRuntimeDriver(driver, h.runtimeDriver()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	guestImage := firstNonEmpty(strings.TrimSpace(req.Msg.GetGuestImage()), agent.GuestImage)
	title := strings.TrimSpace(req.Msg.GetTitle())
	if title == "" {
		title = agent.Name + " 工作会话"
	}
	envItems := domain.MergeEnvItems(agent.EnvItems, EnvItemsFromProto(req.Msg.GetEnvItems()))
	createReq := &agentcomposev1.CreateSessionRequest{
		Title:       title,
		Tags:        AgentDefinitionTagsToProto(agent),
		EnvItems:    EnvItemsToProto(envItems),
		WorkspaceId: workspaceID,
		Driver:      driver,
		GuestImage:  guestImage,
		CapsetIds:   agent.CapsetIDs,
	}
	return h.sessions.CreateSession(ctx, connect.NewRequest(createReq))
}

type AgentValidationResult struct {
	Availability agentcomposev1.AgentAvailabilityStatus
	Health       agentcomposev1.AgentHealthStatus
	Warnings     []string
	Errors       []string
}

func (h *AgentDefinitionHandler) validateAgentDefinitionInput(ctx context.Context, item domain.AgentDefinition, runtimeImageID string) error {
	result := h.validateAgentDefinition(ctx, item, runtimeImageID)
	if len(result.Errors) > 0 {
		return errors.New(strings.Join(result.Errors, "; "))
	}
	return nil
}

func (h *AgentDefinitionHandler) validateAgentDefinition(ctx context.Context, item domain.AgentDefinition, runtimeImageID string) AgentValidationResult {
	workspace, workspaceErr := h.agentWorkspace(ctx, item.WorkspaceID)
	return h.validateAgentDefinitionWithWorkspace(item, runtimeImageID, workspace, workspaceErr)
}

func (h *AgentDefinitionHandler) validateAgentDefinitionWithWorkspace(item domain.AgentDefinition, runtimeImageID string, workspace *domain.WorkspaceConfig, workspaceLookupErr error) AgentValidationResult {
	result := AgentValidationResult{
		Availability: agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_AVAILABLE,
		Health:       agentcomposev1.AgentHealthStatus_AGENT_HEALTH_STATUS_HEALTHY,
	}
	if _, err := domain.NormalizeAgentDefinition(item, true); err != nil {
		result.Errors = append(result.Errors, err.Error())
	}
	if strings.TrimSpace(runtimeImageID) != "" {
		result.Errors = append(result.Errors, "runtime_image_id is not supported in this version")
	}
	if driver := strings.TrimSpace(item.Driver); driver != "" {
		if _, driverErr := driverpkg.ResolveSandboxRuntimeDriver(driver, h.runtimeDriver()); driverErr != nil {
			result.Errors = append(result.Errors, driverErr.Error())
		}
	}
	if wsErr := domain.ValidateAgentWorkspaceValue(item.WorkspaceID, workspace, workspaceLookupErr); wsErr != nil {
		result.Errors = append(result.Errors, wsErr.Error())
	}
	if len(result.Errors) > 0 {
		result.Availability = agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_VALIDATION_FAILED
		result.Health = agentcomposev1.AgentHealthStatus_AGENT_HEALTH_STATUS_AT_RISK
	} else if !item.Enabled {
		result.Availability = agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_UNAVAILABLE
		result.Health = agentcomposev1.AgentHealthStatus_AGENT_HEALTH_STATUS_AT_RISK
	}
	return result
}

func (h *AgentDefinitionHandler) validateAgentWorkspace(ctx context.Context, workspaceID string) error {
	workspace, err := h.agentWorkspace(ctx, workspaceID)
	return domain.ValidateAgentWorkspaceValue(workspaceID, workspace, err)
}

func (h *AgentDefinitionHandler) agentDefinitionToProto(ctx context.Context, item domain.AgentDefinition) (*agentcomposev1.AgentDefinition, error) {
	sessions, err := h.listAllSessions(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return h.agentDefinitionToProtoWith(ctx, item, sessions), nil
}

func (h *AgentDefinitionHandler) agentDefinitionToProtoWith(ctx context.Context, item domain.AgentDefinition, sessions []*domain.Sandbox) *agentcomposev1.AgentDefinition {
	workspace, workspaceErr := h.agentWorkspace(ctx, item.WorkspaceID)
	validation := h.validateAgentDefinitionWithWorkspace(item, "", workspace, workspaceErr)
	current, latest := domain.AgentRunSummaries(item.ID, sessions)
	if !item.DeletedAt.IsZero() {
		validation.Availability = agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_UNAVAILABLE
		validation.Health = agentcomposev1.AgentHealthStatus_AGENT_HEALTH_STATUS_AT_RISK
	} else if validation.Availability != agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_AVAILABLE {
		validation.Health = agentcomposev1.AgentHealthStatus_AGENT_HEALTH_STATUS_AT_RISK
	} else if latest != nil && latest.Status == domain.VMStatusFailed {
		validation.Health = agentcomposev1.AgentHealthStatus_AGENT_HEALTH_STATUS_AT_RISK
	}
	return AgentDefinitionToProto(item, workspace, validation.Availability, validation.Health, current, latest)
}

func (h *AgentDefinitionHandler) agentWorkspace(ctx context.Context, workspaceID string) (*domain.WorkspaceConfig, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, nil
	}
	workspace, err := h.configDB.GetWorkspaceConfig(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return &workspace, nil
}

func (h *AgentDefinitionHandler) listAllSessions(ctx context.Context) ([]*domain.Sandbox, error) {
	result, err := h.store.ListSandboxes(ctx, domain.SandboxListOptions{Limit: AgentSessionScanLimit})
	if err != nil {
		return nil, err
	}
	return result.Sandboxes, nil
}

func (h *AgentDefinitionHandler) stopAgentSessions(ctx context.Context, agentID string) error {
	sessions, err := h.listAllSessions(ctx)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	for _, session := range sessions {
		if !domain.SandboxHasAgentTag(session, agentID) {
			continue
		}
		switch session.Summary.VMStatus {
		case domain.VMStatusRunning:
			if _, err := h.sessions.StopSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: session.Summary.ID})); err != nil {
				return err
			}
		case domain.VMStatusPending:
			if err := h.markAgentSandboxStopped(ctx, session); err != nil {
				return connect.NewError(connect.CodeInternal, err)
			}
		}
	}
	return nil
}

func (h *AgentDefinitionHandler) markAgentSandboxStopped(ctx context.Context, session *domain.Sandbox) error {
	if session == nil {
		return nil
	}
	now := time.Now().UTC()
	if vmState, err := h.store.GetVMState(session.Summary.ID); err == nil {
		vmState.StoppedAt = now
		if strings.TrimSpace(vmState.LastError) == "" {
			vmState.LastError = "agent definition deleted before session startup completed"
		}
		if err := h.store.SaveVMState(session.Summary.ID, vmState); err != nil {
			return err
		}
	}
	session.Summary.VMStatus = domain.VMStatusStopped
	if err := h.store.UpdateSandbox(ctx, session); err != nil {
		return err
	}
	if h.streams != nil {
		h.streams.PublishSandboxUpdated(&session.Summary)
	}
	event := domain.SandboxEvent{
		ID:        uuid.NewString(),
		Type:      "sandbox.stopped",
		Level:     "info",
		Message:   "session stopped because agent definition was deleted",
		CreatedAt: now,
	}
	_ = h.store.AddEvent(ctx, session.Summary.ID, event)
	if h.streams != nil {
		h.streams.PublishEventAdded(session.Summary.ID, event)
	}
	return nil
}

func (h *AgentDefinitionHandler) runtimeDriver() string {
	if h == nil || h.config == nil {
		return ""
	}
	return h.config.RuntimeDriver
}
