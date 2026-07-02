package connectv1

import "agent-compose/proto/agentcompose/v1/agentcomposev1connect"

type LLMHandler struct {
	agentcomposev1connect.LLMServiceHandler
}

func NewLLMHandler(service agentcomposev1connect.LLMServiceHandler) *LLMHandler {
	return &LLMHandler{LLMServiceHandler: service}
}
