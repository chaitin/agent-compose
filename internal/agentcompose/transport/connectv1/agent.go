package connectv1

import "agent-compose/proto/agentcompose/v1/agentcomposev1connect"

type AgentHandler struct {
	agentcomposev1connect.AgentServiceHandler
}

func NewAgentHandler(service agentcomposev1connect.AgentServiceHandler) *AgentHandler {
	return &AgentHandler{AgentServiceHandler: service}
}
