package connectv1

import "agent-compose/proto/agentcompose/v1/agentcomposev1connect"

type CapabilityHandler struct {
	agentcomposev1connect.CapabilityServiceHandler
}

func NewCapabilityHandler(service agentcomposev1connect.CapabilityServiceHandler) *CapabilityHandler {
	return &CapabilityHandler{CapabilityServiceHandler: service}
}
