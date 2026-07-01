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
	if s.DashboardService != nil {
		return s.DashboardService
	}
	return dashboard.NewService(s.dashboard)
}

func (s *Service) capabilityService() *capabilities.Service {
	if s.CapabilityService != nil {
		return s.CapabilityService
	}
	return capabilities.NewService(s.config, s.configDB, s.cap)
}

func (s *Service) workspaceService() *workspaces.Service {
	if s.WorkspaceService != nil {
		return s.WorkspaceService
	}
	s.WorkspaceService = workspaces.NewService(s.config, s.configDB)
	return s.WorkspaceService
}

func (s *Service) settingsService() *SettingsService {
	if s.SettingsService != nil {
		return s.SettingsService
	}
	s.SettingsService = settings.NewService(s.configDB, s.workspaceService(), s.capabilityService())
	return s.SettingsService
}
