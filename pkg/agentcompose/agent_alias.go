package agentcompose

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	agentspkg "agent-compose/pkg/agents"
	sessionspkg "agent-compose/pkg/sessions"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const (
	agentSessionTagSource    = "source"
	agentSessionTagSourceVal = "agent"
	agentSessionTagID        = "agent_id"
	agentSessionTagName      = "agent_name"
)

type AgentDefinition = agentspkg.AgentDefinition
type AgentDefinitionListOptions = agentspkg.AgentDefinitionListOptions
type AgentDefinitionListResult = agentspkg.AgentDefinitionListResult
type AgentValidationResult = agentspkg.AgentValidationResult
type AgentCurrentRunSummary = agentspkg.AgentCurrentRunSummary
type AgentLatestRunSummary = agentspkg.AgentLatestRunSummary
type AgentDefinitionService = agentspkg.Service

func (s *Service) agentDefinitionService() *AgentDefinitionService {
	if s.agentHandlers != nil {
		return s.agentHandlers
	}
	var sessionHandler agentspkg.SessionHandler
	if s.sessions != nil {
		sessionHandler = s.sessions.componentBridge()
	}
	var streams *sessionspkg.SessionStreamBroker
	if s.streams != nil {
		streams = s.streams.componentBroker()
	}
	s.agentHandlers = agentspkg.NewService(s.config, s.store, s.configDB, sessionHandler, streams)
	return s.agentHandlers
}

func sessionHasAgentTag(session *Session, agentID string) bool {
	return agentspkg.SessionHasAgentTag(session, agentID)
}

func (s *Service) ListAgentDefinitions(ctx context.Context, req *connect.Request[agentcomposev1.ListAgentDefinitionsRequest]) (*connect.Response[agentcomposev1.ListAgentDefinitionsResponse], error) {
	return s.agentDefinitionService().ListAgentDefinitions(ctx, req)
}

func (s *Service) GetAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.AgentDefinitionIDRequest]) (*connect.Response[agentcomposev1.AgentDefinitionResponse], error) {
	return s.agentDefinitionService().GetAgentDefinition(ctx, req)
}

func (s *Service) CreateAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.CreateAgentDefinitionRequest]) (*connect.Response[agentcomposev1.AgentDefinitionResponse], error) {
	return s.agentDefinitionService().CreateAgentDefinition(ctx, req)
}

func (s *Service) UpdateAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.UpdateAgentDefinitionRequest]) (*connect.Response[agentcomposev1.AgentDefinitionResponse], error) {
	return s.agentDefinitionService().UpdateAgentDefinition(ctx, req)
}

func (s *Service) DeleteAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.AgentDefinitionIDRequest]) (*connect.Response[emptypb.Empty], error) {
	return s.agentDefinitionService().DeleteAgentDefinition(ctx, req)
}

func (s *Service) SetAgentDefinitionEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetAgentDefinitionEnabledRequest]) (*connect.Response[agentcomposev1.AgentDefinitionResponse], error) {
	return s.agentDefinitionService().SetAgentDefinitionEnabled(ctx, req)
}

func (s *Service) ValidateAgentDefinition(ctx context.Context, req *connect.Request[agentcomposev1.ValidateAgentDefinitionRequest]) (*connect.Response[agentcomposev1.ValidateAgentDefinitionResponse], error) {
	return s.agentDefinitionService().ValidateAgentDefinition(ctx, req)
}

func (s *Service) CreateAgentSession(ctx context.Context, req *connect.Request[agentcomposev1.CreateAgentSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return s.agentDefinitionService().CreateAgentSession(ctx, req)
}
