package agentcompose

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type SessionHandler struct {
	service *Service
}

func NewSessionHandler(service *Service) *SessionHandler {
	return &SessionHandler{service: service}
}

func (h *SessionHandler) CreateSession(ctx context.Context, req *connect.Request[agentcomposev1.CreateSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return h.service.CreateSession(ctx, req)
}

func (h *SessionHandler) ResumeSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return h.service.ResumeSession(ctx, req)
}

func (h *SessionHandler) StopSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return h.service.StopSession(ctx, req)
}

func (h *SessionHandler) GetSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return h.service.GetSession(ctx, req)
}

func (h *SessionHandler) ListSessions(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
	return h.service.ListSessions(ctx, req)
}

func (h *SessionHandler) GetSessionProxy(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionProxyResponse], error) {
	return h.service.GetSessionProxy(ctx, req)
}

func (h *SessionHandler) WatchSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest], stream *connect.ServerStream[agentcomposev1.WatchSessionResponse]) error {
	return h.service.WatchSession(ctx, req, stream)
}

type KernelHandler struct {
	service *Service
}

func NewKernelHandler(service *Service) *KernelHandler {
	return &KernelHandler{service: service}
}

func (h *KernelHandler) ExecuteCell(ctx context.Context, req *connect.Request[agentcomposev1.ExecuteCellRequest]) (*connect.Response[agentcomposev1.ExecuteCellResponse], error) {
	return h.service.ExecuteCell(ctx, req)
}

func (h *KernelHandler) ExecuteCellStream(ctx context.Context, req *connect.Request[agentcomposev1.ExecuteCellRequest], stream *connect.ServerStream[agentcomposev1.ExecuteCellStreamResponse]) error {
	return h.service.ExecuteCellStream(ctx, req, stream)
}

func (h *KernelHandler) ListCells(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.ListCellsResponse], error) {
	return h.service.ListCells(ctx, req)
}

type AgentHandler struct {
	service *Service
}

func NewAgentHandler(service *Service) *AgentHandler {
	return &AgentHandler{service: service}
}

func (h *AgentHandler) SendAgentMessage(ctx context.Context, req *connect.Request[agentcomposev1.SendAgentMessageRequest]) (*connect.Response[agentcomposev1.SendAgentMessageResponse], error) {
	return h.service.SendAgentMessage(ctx, req)
}

func (h *AgentHandler) SendAgentMessageStream(ctx context.Context, req *connect.Request[agentcomposev1.SendAgentMessageRequest], stream *connect.ServerStream[agentcomposev1.SendAgentMessageStreamResponse]) error {
	return h.service.SendAgentMessageStream(ctx, req, stream)
}

func (h *AgentHandler) ListSessionEvents(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.ListSessionEventsResponse], error) {
	return h.service.ListSessionEvents(ctx, req)
}

type LLMHandler struct {
	service *Service
}

func NewLLMHandler(service *Service) *LLMHandler {
	return &LLMHandler{service: service}
}

func (h *LLMHandler) Generate(ctx context.Context, req *connect.Request[agentcomposev1.GenerateLLMRequest]) (*connect.Response[agentcomposev1.GenerateLLMResponse], error) {
	return h.service.Generate(ctx, req)
}

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

type LoaderHandler struct {
	service *Service
}

func NewLoaderHandler(service *Service) *LoaderHandler {
	return &LoaderHandler{service: service}
}

func (h *LoaderHandler) ListLoaders(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.ListLoadersResponse], error) {
	return h.service.ListLoaders(ctx, req)
}

func (h *LoaderHandler) CreateLoader(ctx context.Context, req *connect.Request[agentcomposev1.CreateLoaderRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return h.service.CreateLoader(ctx, req)
}

func (h *LoaderHandler) ValidateLoader(ctx context.Context, req *connect.Request[agentcomposev1.ValidateLoaderRequest]) (*connect.Response[agentcomposev1.ValidateLoaderResponse], error) {
	return h.service.ValidateLoader(ctx, req)
}

func (h *LoaderHandler) GetLoader(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return h.service.GetLoader(ctx, req)
}

func (h *LoaderHandler) UpdateLoader(ctx context.Context, req *connect.Request[agentcomposev1.UpdateLoaderRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return h.service.UpdateLoader(ctx, req)
}

func (h *LoaderHandler) DeleteLoader(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[emptypb.Empty], error) {
	return h.service.DeleteLoader(ctx, req)
}

func (h *LoaderHandler) SetLoaderEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetLoaderEnabledRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return h.service.SetLoaderEnabled(ctx, req)
}

func (h *LoaderHandler) SetLoaderTriggerEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetLoaderTriggerEnabledRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return h.service.SetLoaderTriggerEnabled(ctx, req)
}

func (h *LoaderHandler) RunLoaderNow(ctx context.Context, req *connect.Request[agentcomposev1.RunLoaderNowRequest]) (*connect.Response[agentcomposev1.LoaderRunResponse], error) {
	return h.service.RunLoaderNow(ctx, req)
}

func (h *LoaderHandler) ListLoaderRuns(ctx context.Context, req *connect.Request[agentcomposev1.ListLoaderRunsRequest]) (*connect.Response[agentcomposev1.ListLoaderRunsResponse], error) {
	return h.service.ListLoaderRuns(ctx, req)
}

func (h *LoaderHandler) GetLoaderRun(ctx context.Context, req *connect.Request[agentcomposev1.LoaderRunIDRequest]) (*connect.Response[agentcomposev1.LoaderRunResponse], error) {
	return h.service.GetLoaderRun(ctx, req)
}

func (h *LoaderHandler) ListLoaderEvents(ctx context.Context, req *connect.Request[agentcomposev1.ListLoaderEventsRequest]) (*connect.Response[agentcomposev1.ListLoaderEventsResponse], error) {
	return h.service.ListLoaderEvents(ctx, req)
}

type RunHandler struct {
	service *Service
}

func NewRunHandler(service *Service) *RunHandler {
	return &RunHandler{service: service}
}

func (h *RunHandler) RunAgent(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest]) (*connect.Response[agentcomposev2.RunAgentResponse], error) {
	return h.service.RunAgent(ctx, req)
}

func (h *RunHandler) RunAgentStream(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
	return h.service.RunAgentStream(ctx, req, stream)
}

func (h *RunHandler) GetRun(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
	return h.service.GetRun(ctx, req)
}

func (h *RunHandler) ListRuns(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
	return h.service.ListRuns(ctx, req)
}

func (h *RunHandler) StopRun(ctx context.Context, req *connect.Request[agentcomposev2.StopRunRequest]) (*connect.Response[agentcomposev2.StopRunResponse], error) {
	return h.service.StopRun(ctx, req)
}

type ExecHandler struct {
	service *Service
}

func NewExecHandler(service *Service) *ExecHandler {
	return &ExecHandler{service: service}
}

func (h *ExecHandler) Exec(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest]) (*connect.Response[agentcomposev2.ExecResponse], error) {
	return h.service.Exec(ctx, req)
}

func (h *ExecHandler) ExecStream(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
	return h.service.ExecStream(ctx, req, stream)
}
