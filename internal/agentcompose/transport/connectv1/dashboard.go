package connectv1

import "agent-compose/proto/agentcompose/v1/agentcomposev1connect"

type DashboardHandler struct {
	agentcomposev1connect.DashboardServiceHandler
}

func NewDashboardHandler(service agentcomposev1connect.DashboardServiceHandler) *DashboardHandler {
	return &DashboardHandler{DashboardServiceHandler: service}
}
