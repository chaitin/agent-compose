package agentcompose

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ImageHandler struct {
	service *Service
}

func NewImageHandler(service *Service) *ImageHandler {
	return &ImageHandler{service: service}
}

func (h *ImageHandler) ListImages(ctx context.Context, req *connect.Request[agentcomposev2.ListImagesRequest]) (*connect.Response[agentcomposev2.ListImagesResponse], error) {
	return h.service.ListImages(ctx, req)
}

func (h *ImageHandler) PullImage(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
	return h.service.PullImage(ctx, req)
}

func (h *ImageHandler) InspectImage(ctx context.Context, req *connect.Request[agentcomposev2.InspectImageRequest]) (*connect.Response[agentcomposev2.InspectImageResponse], error) {
	return h.service.InspectImage(ctx, req)
}

func (h *ImageHandler) RemoveImage(ctx context.Context, req *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error) {
	return h.service.RemoveImage(ctx, req)
}

type CapabilityHandler struct {
	service *Service
}

func NewCapabilityHandler(service *Service) *CapabilityHandler {
	return &CapabilityHandler{service: service}
}

func (h *CapabilityHandler) GetCapabilityStatus(ctx context.Context, req *connect.Request[agentcomposev1.GetCapabilityStatusRequest]) (*connect.Response[agentcomposev1.CapabilityStatusResponse], error) {
	return h.service.GetCapabilityStatus(ctx, req)
}

func (h *CapabilityHandler) ListCapabilitySets(ctx context.Context, req *connect.Request[agentcomposev1.ListCapabilitySetsRequest]) (*connect.Response[agentcomposev1.ListCapabilitySetsResponse], error) {
	return h.service.ListCapabilitySets(ctx, req)
}

func (h *CapabilityHandler) GetCapabilityCatalog(ctx context.Context, req *connect.Request[agentcomposev1.GetCapabilityCatalogRequest]) (*connect.Response[agentcomposev1.GetCapabilityCatalogResponse], error) {
	return h.service.GetCapabilityCatalog(ctx, req)
}

type DashboardHandler struct {
	service *Service
}

func NewDashboardHandler(service *Service) *DashboardHandler {
	return &DashboardHandler{service: service}
}

func (h *DashboardHandler) GetDashboardOverview(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.DashboardOverviewResponse], error) {
	return h.service.GetDashboardOverview(ctx, req)
}

func (h *DashboardHandler) WatchDashboardOverview(ctx context.Context, req *connect.Request[emptypb.Empty], stream *connect.ServerStream[agentcomposev1.DashboardOverviewEvent]) error {
	return h.service.WatchDashboardOverview(ctx, req, stream)
}
