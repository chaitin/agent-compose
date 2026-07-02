package connectv1

import "agent-compose/proto/agentcompose/v1/agentcomposev1connect"

type ConfigHandler struct {
	agentcomposev1connect.ConfigServiceHandler
}

func NewConfigHandler(service agentcomposev1connect.ConfigServiceHandler) *ConfigHandler {
	return &ConfigHandler{ConfigServiceHandler: service}
}
