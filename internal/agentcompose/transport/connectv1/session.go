package connectv1

import "agent-compose/proto/agentcompose/v1/agentcomposev1connect"

type SessionHandler struct {
	agentcomposev1connect.SessionServiceHandler
}

func NewSessionHandler(service agentcomposev1connect.SessionServiceHandler) *SessionHandler {
	return &SessionHandler{SessionServiceHandler: service}
}
