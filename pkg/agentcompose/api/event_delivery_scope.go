package api

import (
	"context"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// GetEventDeliveryScope reports the daemon's event delivery scope so a control
// plane can verify the isolation it relies on.
func (h *SettingsV2Handler) GetEventDeliveryScope(context.Context, *connect.Request[agentcomposev2.GetEventDeliveryScopeRequest]) (*connect.Response[agentcomposev2.GetEventDeliveryScopeResponse], error) {
	return connect.NewResponse(&agentcomposev2.GetEventDeliveryScopeResponse{Scope: eventDeliveryScopeToV2(h.eventDeliveryScope)}), nil
}

func eventDeliveryScopeToV2(scope domain.EventDeliveryScope) agentcomposev2.EventDeliveryScope {
	switch scope {
	case domain.EventDeliveryScopeProject:
		return agentcomposev2.EventDeliveryScope_EVENT_DELIVERY_SCOPE_PROJECT
	case domain.EventDeliveryScopeDaemon:
		return agentcomposev2.EventDeliveryScope_EVENT_DELIVERY_SCOPE_DAEMON
	default:
		return agentcomposev2.EventDeliveryScope_EVENT_DELIVERY_SCOPE_UNSPECIFIED
	}
}
