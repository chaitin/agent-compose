package connectv1

import "agent-compose/proto/agentcompose/v1/agentcomposev1connect"

type AgentDefinitionHandler struct {
	agentcomposev1connect.AgentDefinitionServiceHandler
}

func NewAgentDefinitionHandler(service agentcomposev1connect.AgentDefinitionServiceHandler) *AgentDefinitionHandler {
	return &AgentDefinitionHandler{AgentDefinitionServiceHandler: service}
}
