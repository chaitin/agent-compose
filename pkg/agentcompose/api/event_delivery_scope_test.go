package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestGetEventDeliveryScopeReportsDaemonConfiguration(t *testing.T) {
	for _, test := range []struct {
		scope domain.EventDeliveryScope
		want  agentcomposev2.EventDeliveryScope
	}{
		{scope: domain.EventDeliveryScopeProject, want: agentcomposev2.EventDeliveryScope_EVENT_DELIVERY_SCOPE_PROJECT},
		{scope: domain.EventDeliveryScopeDaemon, want: agentcomposev2.EventDeliveryScope_EVENT_DELIVERY_SCOPE_DAEMON},
	} {
		handler := NewSettingsV2Handler(&appconfig.Config{DataRoot: t.TempDir(), EventDeliveryScope: test.scope}, &settingsStoreFake{})
		resp, err := handler.GetEventDeliveryScope(context.Background(), connect.NewRequest(&agentcomposev2.GetEventDeliveryScopeRequest{}))
		if err != nil {
			t.Fatalf("GetEventDeliveryScope: %v", err)
		}
		if resp.Msg.GetScope() != test.want {
			t.Fatalf("scope %q reported as %v, want %v", test.scope, resp.Msg.GetScope(), test.want)
		}
	}
}
