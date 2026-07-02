package agents

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
	"agent-compose/pkg/model"
	sessionspkg "agent-compose/pkg/sessions"
	"agent-compose/pkg/storage"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const (
	VMStatusPending = model.VMStatusPending
	VMStatusRunning = model.VMStatusRunning
	VMStatusStopped = model.VMStatusStopped
	VMStatusFailed  = model.VMStatusFailed
)

type Store = storage.Store
type ConfigStore = storage.ConfigStore
type SessionListOptions = model.SessionListOptions
type SessionEvent = model.SessionEvent

type SessionHandler interface {
	StopSession(context.Context, *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error)
	CreateSession(context.Context, *connect.Request[agentcomposev1.CreateSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error)
}

type Service struct {
	config   *appconfig.Config
	store    *Store
	configDB *ConfigStore
	sessions SessionHandler
	streams  *sessionspkg.SessionStreamBroker
}

func NewService(config *appconfig.Config, store *Store, configDB *ConfigStore, sessions SessionHandler, streams *sessionspkg.SessionStreamBroker) *Service {
	return &Service{config: config, store: store, configDB: configDB, sessions: sessions, streams: streams}
}

// agentSessionScanLimit bounds how many sessions we load when computing agent
// run summaries. It must stay large enough to never truncate the scan: run
// summaries must consider every associated session, not just a recent page.
const agentSessionScanLimit = 1 << 30

func (s *Service) ListAgentDefinitions(ctx context.Context, req *connect.Request[agentcomposev1.ListAgentDefinitionsRequest]) (*connect.Response[agentcomposev1.ListAgentDefinitionsResponse], error) {
	result, err := s.configDB.ListAgentDefinitions(ctx, AgentDefinitionListOptions{
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
	sessions, err := s.listAllSessions(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	for _, item := range result.Agents {
		resp.Agents = append(resp.Agents, s.agentDefinitionToProtoWith(ctx, item, sessions))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) GetAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.AgentDefinitionIDRequest]) (*connect.Response[agentcomposev1.AgentDefinitionResponse], error) {
	id := strings.TrimSpace(req.Msg.GetAgentId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition id is required"))
	}
	item, err := s.configDB.GetAgentDefinitionIncludingDeleted(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	protoAgent, connectErr := s.agentDefinitionToProto(ctx, item)
	if connectErr != nil {
		return nil, connectErr
	}
	return connect.NewResponse(&agentcomposev1.AgentDefinitionResponse{Agent: protoAgent}), nil
}

func (s *Service) CreateAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.CreateAgentDefinitionRequest]) (*connect.Response[agentcomposev1.AgentDefinitionResponse], error) {
	item := AgentDefinition{
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
		EnvItems:     envItemsFromProto(req.Msg.GetEnvItems()),
		ConfigJSON:   req.Msg.GetConfigJson(),
		CapsetIDs:    req.Msg.GetCapsetIds(),
	}
	if err := s.validateAgentDefinitionInput(ctx, item, req.Msg.GetRuntimeImageId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	saved, err := s.configDB.CreateAgentDefinition(ctx, item)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	protoAgent, connectErr := s.agentDefinitionToProto(ctx, saved)
	if connectErr != nil {
		return nil, connectErr
	}
	return connect.NewResponse(&agentcomposev1.AgentDefinitionResponse{Agent: protoAgent}), nil
}

func (s *Service) UpdateAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.UpdateAgentDefinitionRequest]) (*connect.Response[agentcomposev1.AgentDefinitionResponse], error) {
	id := strings.TrimSpace(req.Msg.GetAgentId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition id is required"))
	}
	if _, err := s.configDB.GetAgentDefinition(ctx, id); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	item := AgentDefinition{
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
		EnvItems:     envItemsFromProto(req.Msg.GetEnvItems()),
		ConfigJSON:   req.Msg.GetConfigJson(),
		CapsetIDs:    req.Msg.GetCapsetIds(),
	}
	if err := s.validateAgentDefinitionInput(ctx, item, req.Msg.GetRuntimeImageId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	saved, err := s.configDB.UpdateAgentDefinition(ctx, item)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	protoAgent, connectErr := s.agentDefinitionToProto(ctx, saved)
	if connectErr != nil {
		return nil, connectErr
	}
	return connect.NewResponse(&agentcomposev1.AgentDefinitionResponse{Agent: protoAgent}), nil
}

func (s *Service) DeleteAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.AgentDefinitionIDRequest]) (*connect.Response[emptypb.Empty], error) {
	id := strings.TrimSpace(req.Msg.GetAgentId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition id is required"))
	}
	if _, err := s.configDB.GetAgentDefinition(ctx, id); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if err := s.stopAgentSessions(ctx, id); err != nil {
		return nil, err
	}
	if _, err := s.configDB.DisableLoadersByDefaultAgent(ctx, id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := s.configDB.DeleteAgentDefinition(ctx, id); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (s *Service) stopAgentSessions(ctx context.Context, agentID string) error {
	sessions, err := s.listAllSessions(ctx)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	for _, session := range sessions {
		if !sessionHasAgentTag(session, agentID) {
			continue
		}
		switch session.Summary.VMStatus {
		case VMStatusRunning:
			if _, err := s.sessions.StopSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: session.Summary.ID})); err != nil {
				return err
			}
		case VMStatusPending:
			if err := s.markAgentSessionStopped(ctx, session); err != nil {
				return connect.NewError(connect.CodeInternal, err)
			}
		}
	}
	return nil
}

func (s *Service) markAgentSessionStopped(ctx context.Context, session *Session) error {
	if session == nil {
		return nil
	}
	now := time.Now().UTC()
	if vmState, err := s.store.GetVMState(session.Summary.ID); err == nil {
		vmState.StoppedAt = now
		if strings.TrimSpace(vmState.LastError) == "" {
			vmState.LastError = "agent definition deleted before session startup completed"
		}
		if err := s.store.SaveVMState(session.Summary.ID, vmState); err != nil {
			return err
		}
	}
	session.Summary.VMStatus = VMStatusStopped
	if err := s.store.UpdateSession(ctx, session); err != nil {
		return err
	}
	s.streams.PublishSessionUpdated(&session.Summary)
	event := SessionEvent{
		ID:        uuid.NewString(),
		Type:      "session.stopped",
		Level:     "info",
		Message:   "session stopped because agent definition was deleted",
		CreatedAt: now,
	}
	_ = s.store.AddEvent(ctx, session.Summary.ID, event)
	s.streams.PublishEventAdded(session.Summary.ID, event)
	return nil
}

func (s *Service) SetAgentDefinitionEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetAgentDefinitionEnabledRequest]) (*connect.Response[agentcomposev1.AgentDefinitionResponse], error) {
	id := strings.TrimSpace(req.Msg.GetAgentId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition id is required"))
	}
	item, err := s.configDB.GetAgentDefinition(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if req.Msg.GetEnabled() {
		if err := s.validateAgentDefinitionInput(ctx, item, ""); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	saved, err := s.configDB.SetAgentDefinitionEnabled(ctx, id, req.Msg.GetEnabled())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	protoAgent, connectErr := s.agentDefinitionToProto(ctx, saved)
	if connectErr != nil {
		return nil, connectErr
	}
	return connect.NewResponse(&agentcomposev1.AgentDefinitionResponse{Agent: protoAgent}), nil
}

func (s *Service) ValidateAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.ValidateAgentDefinitionRequest]) (*connect.Response[agentcomposev1.ValidateAgentDefinitionResponse], error) {
	item := AgentDefinition{
		ID:           firstNonEmpty(strings.TrimSpace(req.Msg.GetAgentId()), "validate-agent"),
		Name:         req.Msg.GetName(),
		Provider:     req.Msg.GetProvider(),
		Model:        req.Msg.GetModel(),
		SystemPrompt: req.Msg.GetSystemPrompt(),
		Driver:       req.Msg.GetDriver(),
		GuestImage:   req.Msg.GetGuestImage(),
		WorkspaceID:  req.Msg.GetWorkspaceId(),
		EnvItems:     envItemsFromProto(req.Msg.GetEnvItems()),
		ConfigJSON:   req.Msg.GetConfigJson(),
		Enabled:      true,
	}
	result := s.validateAgentDefinition(ctx, item, req.Msg.GetRuntimeImageId())
	return connect.NewResponse(&agentcomposev1.ValidateAgentDefinitionResponse{
		AvailabilityStatus: result.Availability,
		HealthStatus:       result.Health,
		Warnings:           result.Warnings,
		Errors:             result.Errors,
	}), nil
}

func (s *Service) CreateAgentSession(ctx context.Context, req *connect.Request[agentcomposev1.CreateAgentSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	id := strings.TrimSpace(req.Msg.GetAgentId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition id is required"))
	}
	agent, err := s.configDB.GetAgentDefinition(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if !agent.Enabled {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition %s is disabled", id))
	}
	validation := s.validateAgentDefinition(ctx, agent, "")
	if validation.Availability != agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_AVAILABLE {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent definition %s is not available: %s", id, strings.Join(validation.Errors, "; ")))
	}
	workspaceID := firstNonEmpty(strings.TrimSpace(req.Msg.GetWorkspaceId()), agent.WorkspaceID)
	if err := s.validateAgentWorkspace(ctx, workspaceID); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	driver := firstNonEmpty(strings.TrimSpace(req.Msg.GetDriver()), agent.Driver)
	if strings.TrimSpace(driver) != "" {
		if _, err := driverpkg.ResolveSessionRuntimeDriver(driver, s.config.RuntimeDriver); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	guestImage := firstNonEmpty(strings.TrimSpace(req.Msg.GetGuestImage()), agent.GuestImage)
	title := strings.TrimSpace(req.Msg.GetTitle())
	if title == "" {
		title = agent.Name + " 工作会话"
	}
	envItems := mergeEnvItems(agent.EnvItems, envItemsFromProto(req.Msg.GetEnvItems()))
	createReq := &agentcomposev1.CreateSessionRequest{
		Title:       title,
		Tags:        agentDefinitionTags(agent),
		EnvItems:    toProtoEnvItems(envItems),
		WorkspaceId: workspaceID,
		Driver:      driver,
		GuestImage:  guestImage,
		CapsetIds:   agent.CapsetIDs,
	}
	return s.sessions.CreateSession(ctx, connect.NewRequest(createReq))
}

func (s *Service) validateAgentDefinitionInput(ctx context.Context, item AgentDefinition, runtimeImageID string) error {
	result := s.validateAgentDefinition(ctx, item, runtimeImageID)
	if len(result.Errors) > 0 {
		return errors.New(strings.Join(result.Errors, "; "))
	}
	return nil
}

func (s *Service) validateAgentDefinition(ctx context.Context, item AgentDefinition, runtimeImageID string) AgentValidationResult {
	workspace, workspaceErr := s.agentWorkspace(ctx, item.WorkspaceID)
	return s.validateAgentDefinitionWithWorkspace(item, runtimeImageID, workspace, workspaceErr)
}

// validateAgentDefinitionWithWorkspace runs the §7/§8 checks against an already
// resolved workspace so callers that also need the workspace record do not query
// it twice. Driver and workspace are checked from the raw trimmed input so the
// error list stays complete even when normalization fails on another field.
func (s *Service) validateAgentDefinitionWithWorkspace(item AgentDefinition, runtimeImageID string, workspace *WorkspaceConfig, workspaceLookupErr error) AgentValidationResult {
	result := AgentValidationResult{
		Availability: agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_AVAILABLE,
		Health:       agentcomposev1.AgentHealthStatus_AGENT_HEALTH_STATUS_HEALTHY,
	}
	if _, err := normalizeAgentDefinition(item, true); err != nil {
		result.Errors = append(result.Errors, err.Error())
	}
	if strings.TrimSpace(runtimeImageID) != "" {
		result.Errors = append(result.Errors, "runtime_image_id is not supported in this version")
	}
	if driver := strings.TrimSpace(item.Driver); driver != "" {
		if _, driverErr := driverpkg.ResolveSessionRuntimeDriver(driver, s.config.RuntimeDriver); driverErr != nil {
			result.Errors = append(result.Errors, driverErr.Error())
		}
	}
	if wsErr := validateAgentWorkspaceValue(item.WorkspaceID, workspace, workspaceLookupErr); wsErr != nil {
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

func (s *Service) validateAgentWorkspace(ctx context.Context, workspaceID string) error {
	workspace, err := s.agentWorkspace(ctx, workspaceID)
	return validateAgentWorkspaceValue(workspaceID, workspace, err)
}

func validateAgentWorkspaceValue(workspaceID string, workspace *WorkspaceConfig, lookupErr error) error {
	if strings.TrimSpace(workspaceID) == "" {
		return nil
	}
	if lookupErr != nil {
		return lookupErr
	}
	if workspace == nil {
		return fmt.Errorf("workspace config %s not found", strings.TrimSpace(workspaceID))
	}
	switch strings.ToLower(strings.TrimSpace(workspace.Type)) {
	case "file", "git":
		return nil
	default:
		return fmt.Errorf("unsupported agent workspace type %q", workspace.Type)
	}
}

func (s *Service) agentDefinitionToProto(ctx context.Context, item AgentDefinition) (*agentcomposev1.AgentDefinition, error) {
	sessions, err := s.listAllSessions(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return s.agentDefinitionToProtoWith(ctx, item, sessions), nil
}

func (s *Service) AgentDefinitionToProto(ctx context.Context, item AgentDefinition) (*agentcomposev1.AgentDefinition, error) {
	return s.agentDefinitionToProto(ctx, item)
}

// agentDefinitionToProtoWith builds the proto view from a pre-loaded session
// slice so list responses scan sessions once instead of once per agent.
func (s *Service) agentDefinitionToProtoWith(ctx context.Context, item AgentDefinition, sessions []*Session) *agentcomposev1.AgentDefinition {
	workspace, workspaceErr := s.agentWorkspace(ctx, item.WorkspaceID)
	validation := s.validateAgentDefinitionWithWorkspace(item, "", workspace, workspaceErr)
	current, latest := agentRunSummaries(item.ID, sessions)
	if !item.DeletedAt.IsZero() {
		validation.Availability = agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_UNAVAILABLE
		validation.Health = agentcomposev1.AgentHealthStatus_AGENT_HEALTH_STATUS_AT_RISK
	} else if validation.Availability != agentcomposev1.AgentAvailabilityStatus_AGENT_AVAILABILITY_STATUS_AVAILABLE {
		validation.Health = agentcomposev1.AgentHealthStatus_AGENT_HEALTH_STATUS_AT_RISK
	} else if latest != nil && latest.Status == VMStatusFailed {
		validation.Health = agentcomposev1.AgentHealthStatus_AGENT_HEALTH_STATUS_AT_RISK
	}
	return toProtoAgentDefinition(item, workspace, validation, current, latest)
}

func (s *Service) agentWorkspace(ctx context.Context, workspaceID string) (*WorkspaceConfig, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, nil
	}
	workspace, err := s.configDB.GetWorkspaceConfig(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return &workspace, nil
}

func (s *Service) listAllSessions(ctx context.Context) ([]*Session, error) {
	result, err := s.store.ListSessions(ctx, SessionListOptions{Limit: agentSessionScanLimit})
	if err != nil {
		return nil, err
	}
	return result.Sessions, nil
}

func agentRunSummaries(agentID string, sessions []*Session) (AgentCurrentRunSummary, *AgentLatestRunSummary) {
	current := AgentCurrentRunSummary{}
	var latest *AgentLatestRunSummary
	for _, session := range sessions {
		if !sessionHasAgentTag(session, agentID) {
			continue
		}
		switch session.Summary.VMStatus {
		case VMStatusPending, VMStatusRunning:
			current.RunningSessionCount++
		}
		if latest == nil || session.Summary.UpdatedAt.After(latest.At) {
			latest = &AgentLatestRunSummary{
				RunType: "work_session",
				Status:  session.Summary.VMStatus,
				RunID:   session.Summary.ID,
				Title:   session.Summary.Title,
				At:      session.Summary.UpdatedAt,
			}
		}
	}
	return current, latest
}

func AgentRunSummaries(agentID string, sessions []*Session) (AgentCurrentRunSummary, *AgentLatestRunSummary) {
	return agentRunSummaries(agentID, sessions)
}

func envItemsFromProto(items []*agentcomposev1.SessionEnvVar) []SessionEnvVar {
	result := make([]SessionEnvVar, 0, len(items))
	for _, item := range items {
		result = append(result, SessionEnvVar{Name: item.GetName(), Value: item.GetValue(), Secret: item.GetSecret()})
	}
	return result
}

func EnvItemsFromProto(items []*agentcomposev1.SessionEnvVar) []SessionEnvVar {
	return envItemsFromProto(items)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func mergeEnvItems(globalItems, sessionItems []SessionEnvVar) []SessionEnvVar {
	return model.MergeSessionEnvItems(globalItems, sessionItems)
}
