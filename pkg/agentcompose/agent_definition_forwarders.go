package agentcompose

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/agents"
	"agent-compose/pkg/sessions"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func (s *Service) agentDefinitionService() *agents.Service {
	s.forwarderMu.Lock()
	defer s.forwarderMu.Unlock()
	if s.agentHandlers != nil {
		return s.agentHandlers
	}
	var sessionHandler agents.SessionHandler
	if s.sessions != nil {
		sessionHandler = s.sessions
	}
	var streams *sessions.SessionStreamBroker
	if s.streams != nil {
		streams = s.streams
	}
	s.agentHandlers = agents.NewService(s.config, s.store, s.configDB, sessionHandler, streams)
	return s.agentHandlers
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
