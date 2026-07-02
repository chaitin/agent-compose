package agentcompose

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/dashboard"
	"agent-compose/pkg/settings"
	"agent-compose/pkg/workspaces"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func (s *Service) GetDashboardOverview(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.DashboardOverviewResponse], error) {
	return s.dashboardService().GetDashboardOverview(ctx, req)
}

func (s *Service) WatchDashboardOverview(ctx context.Context, req *connect.Request[emptypb.Empty], stream *connect.ServerStream[agentcomposev1.DashboardOverviewEvent]) error {
	return s.dashboardService().WatchDashboardOverview(ctx, req, stream)
}

func (s *Service) GetCapabilityStatus(ctx context.Context, req *connect.Request[agentcomposev1.GetCapabilityStatusRequest]) (*connect.Response[agentcomposev1.CapabilityStatusResponse], error) {
	return s.capabilityService().GetCapabilityStatus(ctx, req)
}

func (s *Service) ListCapabilitySets(ctx context.Context, req *connect.Request[agentcomposev1.ListCapabilitySetsRequest]) (*connect.Response[agentcomposev1.ListCapabilitySetsResponse], error) {
	return s.capabilityService().ListCapabilitySets(ctx, req)
}

func (s *Service) GetCapabilityCatalog(ctx context.Context, req *connect.Request[agentcomposev1.GetCapabilityCatalogRequest]) (*connect.Response[agentcomposev1.GetCapabilityCatalogResponse], error) {
	return s.capabilityService().GetCapabilityCatalog(ctx, req)
}

func (s *Service) GetCapabilityGatewayConfig(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.CapabilityGatewayConfig], error) {
	return s.settingsService().GetCapabilityGatewayConfig(ctx, req)
}

func (s *Service) UpdateCapabilityGatewayConfig(ctx context.Context, req *connect.Request[agentcomposev1.UpdateCapabilityGatewayConfigRequest]) (*connect.Response[agentcomposev1.CapabilityGatewayConfig], error) {
	return s.settingsService().UpdateCapabilityGatewayConfig(ctx, req)
}

func (s *Service) dashboardService() *dashboard.Service {
	if s.dashboardHandlers != nil {
		return s.dashboardHandlers
	}
	s.dashboardHandlers = dashboard.NewService(s.dashboard)
	return s.dashboardHandlers
}

func (s *Service) capabilityService() *capabilities.Service {
	if s.capabilityHandlers != nil {
		return s.capabilityHandlers
	}
	s.capabilityHandlers = capabilities.NewService(s.config, s.configDB, s.cap)
	return s.capabilityHandlers
}

func (s *Service) workspaceService() *workspaces.Service {
	if s.workspaceHandlers != nil {
		return s.workspaceHandlers
	}
	s.workspaceHandlers = workspaces.NewService(s.config, s.configDB)
	return s.workspaceHandlers
}

func (s *Service) settingsService() *settings.Service {
	if s.settingsHandlers != nil {
		return s.settingsHandlers
	}
	s.settingsHandlers = settings.NewService(s.configDB, s.workspaceService(), s.capabilityService())
	return s.settingsHandlers
}
