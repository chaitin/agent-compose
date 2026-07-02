package connectv2

import "agent-compose/proto/agentcompose/v2/agentcomposev2connect"

type ExecHandler struct {
	agentcomposev2connect.ExecServiceHandler
}

func NewExecHandler(service agentcomposev2connect.ExecServiceHandler) *ExecHandler {
	return &ExecHandler{ExecServiceHandler: service}
}
