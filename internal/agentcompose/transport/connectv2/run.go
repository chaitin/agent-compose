package connectv2

import "agent-compose/proto/agentcompose/v2/agentcomposev2connect"

type RunHandler struct {
	agentcomposev2connect.RunServiceHandler
}

func NewRunHandler(service agentcomposev2connect.RunServiceHandler) *RunHandler {
	return &RunHandler{RunServiceHandler: service}
}
