package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"log/slog"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/agents"
	"agent-compose/pkg/bus"
	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/capproxy"
	"agent-compose/pkg/dashboard"
	"agent-compose/pkg/events"
	"agent-compose/pkg/executor"
	"agent-compose/pkg/images"
	llmpkg "agent-compose/pkg/llm"
	"agent-compose/pkg/loaders"
	"agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/runtimes"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/settings"
	"agent-compose/pkg/storage"
	"agent-compose/pkg/workspaces"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type Service struct {
	imageHandlers      *images.Service
	dashboardHandlers  *dashboard.Service
	capabilityHandlers *capabilities.Service
	workspaceHandlers  *workspaces.Service
	settingsHandlers   *settings.Service
	config             *appconfig.Config
	store              *storage.Store
	configDB           *storage.ConfigStore
	driver             runtimes.Driver
	runtimes           runtimes.RuntimeProvider
	executor           *executor.Executor
	loaders            *loaders.LoaderManager
	images             images.ImageBackend
	ociImages          images.ImageBackend
	autoImages         images.ImageBackend
	llm                *llmpkg.LLMClient
	cap                capabilities.Integration
	bus                *bus.LoaderBus
	streams            *sessions.SessionStreamBroker
	dashboard          *dashboard.DashboardOverviewHub
	events             *events.EventDispatcher
	sessions           *sessions.SessionRPCBridge
	agentHandlers      *agents.Service
	sessionHandlers    *sessions.Service
	loaderHandlers     *loaders.Service
	projectHandlers    *projects.Service
	startedAt          time.Time
	startOnce          sync.Once
	startErr           error
	forwarderMu        sync.Mutex
	agentcomposev1connect.UnimplementedSessionServiceHandler
	agentcomposev1connect.UnimplementedKernelServiceHandler
	agentcomposev1connect.UnimplementedAgentServiceHandler
	agentcomposev1connect.UnimplementedAgentDefinitionServiceHandler
	agentcomposev1connect.UnimplementedLLMServiceHandler
	agentcomposev1connect.UnimplementedConfigServiceHandler
	agentcomposev1connect.UnimplementedLoaderServiceHandler
	agentcomposev1connect.UnimplementedDashboardServiceHandler
	agentcomposev1connect.UnimplementedCapabilityServiceHandler
	agentcomposev2connect.UnimplementedProjectServiceHandler
	agentcomposev2connect.UnimplementedRunServiceHandler
	agentcomposev2connect.UnimplementedExecServiceHandler
	agentcomposev2connect.UnimplementedImageServiceHandler
}

func NewService(di do.Injector) (*Service, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	dashHub, _ := do.Invoke[*dashboard.DashboardOverviewHub](di)
	if dashHub == nil {
		rootCtx, _ := do.Invoke[context.Context](di)
		if rootCtx == nil {
			rootCtx = context.Background()
		}
		dashHub = dashboard.NewHub(rootCtx, dashboard.NewAggregator(do.MustInvoke[*storage.Store](di), do.MustInvoke[*storage.ConfigStore](di)), 250*time.Millisecond)
	}
	capProvider := do.MustInvoke[capabilities.Integration](di)
	imageBackends, err := imageBackendsFromDI(di)
	if err != nil {
		return nil, err
	}
	projectHandlers, _ := do.Invoke[*projects.Service](di)
	service := &Service{
		imageHandlers:      images.NewService(imageBackends.docker, imageBackends.oci, imageBackends.auto),
		dashboardHandlers:  dashboard.NewService(dashHub),
		capabilityHandlers: capabilities.NewService(config, do.MustInvoke[*storage.ConfigStore](di), capProvider),
		workspaceHandlers:  workspaces.NewService(config, do.MustInvoke[*storage.ConfigStore](di)),
		settingsHandlers:   settings.NewService(do.MustInvoke[*storage.ConfigStore](di), workspaces.NewService(config, do.MustInvoke[*storage.ConfigStore](di)), capabilities.NewService(config, do.MustInvoke[*storage.ConfigStore](di), capProvider)),
		config:             config,
		store:              do.MustInvoke[*storage.Store](di),
		configDB:           do.MustInvoke[*storage.ConfigStore](di),
		driver:             do.MustInvoke[runtimes.Driver](di),
		runtimes:           do.MustInvoke[runtimes.RuntimeProvider](di),
		executor:           do.MustInvoke[*executor.Executor](di),
		loaders:            do.MustInvoke[*loaders.LoaderManager](di),
		images:             imageBackends.docker,
		ociImages:          imageBackends.oci,
		autoImages:         imageBackends.auto,
		llm:                do.MustInvoke[*llmpkg.LLMClient](di),
		cap:                capProvider,
		bus:                do.MustInvoke[*bus.LoaderBus](di),
		streams:            do.MustInvoke[*sessions.SessionStreamBroker](di),
		dashboard:          dashHub,
		events:             events.NewEventDispatcher(do.MustInvoke[context.Context](di), do.MustInvoke[*storage.ConfigStore](di), do.MustInvoke[*bus.LoaderBus](di)),
		sessions:           do.MustInvoke[*sessions.SessionRPCBridge](di),
		agentHandlers:      agents.NewService(config, do.MustInvoke[*storage.Store](di), do.MustInvoke[*storage.ConfigStore](di), do.MustInvoke[*sessions.SessionRPCBridge](di), do.MustInvoke[*sessions.SessionStreamBroker](di)),
		loaderHandlers:     loaders.NewService(do.MustInvoke[*storage.ConfigStore](di), do.MustInvoke[*loaders.LoaderManager](di), do.MustInvoke[*bus.LoaderBus](di)),
		projectHandlers:    projectHandlers,
		startedAt:          time.Now().UTC(),
	}
	if service.projectHandlers == nil {
		service.projectHandlers = newProjectServiceFromDeps(service)
	}
	service.loaders.SetProjectAgentRunner(service.projectHandlers)
	service.sessionHandlers = sessions.NewService(service.store, service.executor, service.bus, service.streams, service.sessions, service.resolveSessionAgentConfigForSessions)
	return service, nil
}

func Setup(di do.Injector) {
	Register(di)
	if err := StartBackground(di); err != nil {
		slog.Error("failed to start agent-compose background managers", "error", err)
	}
}

func Register(di do.Injector) {
	do.Provide(di, storage.NewStore)
	do.Provide(di, storage.NewConfigStore)
	do.Provide(di, runtimes.NewRuntimeProvider)
	do.Provide(di, newSessionRuntimeEnvPreparer)
	do.Provide(di, newExecutorLLMFacadeEnvPreparer)
	do.Provide(di, runtimes.NewDriver)
	do.Provide(di, executor.NewExecutor)
	do.Provide(di, llmpkg.NewLLMClient)
	do.Provide(di, capabilities.NewCapabilityProvider)
	do.Provide(di, capabilities.NewCapProxyServer)
	do.Provide(di, bus.NewLoaderBus)
	do.Provide(di, sessions.NewSessionStreamBroker)
	do.Provide(di, newExecutorStreamPublisher)
	do.Provide(di, dashboard.NewDashboardOverviewAggregator)
	do.Provide(di, dashboard.NewDashboardOverviewHub)
	do.Provide(di, newLoaderEngine)
	do.Provide(di, sessions.NewSessionRPCBridge)
	do.Provide(di, newImageBackends)
	do.Provide(di, newLoaderManager)
	do.Provide(di, newProjectService)
	do.Provide(di, NewService)

	app := do.MustInvoke[*echo.Echo](di)
	service := do.MustInvoke[*Service](di)

	path, handler := agentcomposev1connect.NewSessionServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewKernelServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewAgentServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewAgentDefinitionServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewLLMServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewConfigServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewLoaderServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewDashboardServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewCapabilityServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))

	path, handler = agentcomposev2connect.NewProjectServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev2connect.NewRunServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev2connect.NewExecServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev2connect.NewImageServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))

	registerWebhookRoutes(app, service)
	registerRuntimeLLMFacadeRoutes(app, service)
	registerProxyRoutes(app, service)
	registerWorkspaceRoutes(app, service)
}

func StartBackground(di do.Injector) error {
	service := do.MustInvoke[*Service](di)
	return service.StartBackground(do.MustInvoke[context.Context](di), do.MustInvoke[*capproxy.Server](di))
}

func (s *Service) StartBackground(ctx context.Context, capProxy *capproxy.Server) error {
	s.startOnce.Do(func() {
		reconcileCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.reconcilePersistedSessions(reconcileCtx); err != nil {
			slog.Warn("failed to reconcile persisted session state on startup", "error", err)
		}
		s.loaders.Start()
		s.events.Start()
		s.startErr = startCapabilityProxy(ctx, capProxy)
	})
	return s.startErr
}

func startCapabilityProxy(ctx context.Context, capProxy *capproxy.Server) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if capProxy.Configured() {
		go func() {
			if err := capProxy.Serve(ctx); err != nil {
				slog.Error("agent compose capability grpc proxy stopped", "error", err)
			}
		}()
	}
	return nil
}

func (s *Service) sessionsService() *sessions.Service {
	s.forwarderMu.Lock()
	defer s.forwarderMu.Unlock()
	if s.sessionHandlers != nil {
		return s.sessionHandlers
	}
	if s.streams == nil {
		s.streams, _ = sessions.NewSessionStreamBroker(nil)
	}
	if s.sessions == nil {
		s.sessions = sessions.NewSessionRPCBridgeFromDeps(s.config, s.store, s.configDB, s.driver, s.runtimes, s.bus, s.streams, s.cap, s.dashboard)
	}
	s.sessionHandlers = sessions.NewService(s.store, s.executor, s.bus, s.streams, s.sessions, s.resolveSessionAgentConfigForSessions)
	return s.sessionHandlers
}

func (s *Service) CreateSession(ctx context.Context, req *connect.Request[agentcomposev1.CreateSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return s.sessionsService().CreateSession(ctx, req)
}

func (s *Service) WatchSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest], stream *connect.ServerStream[agentcomposev1.WatchSessionResponse]) error {
	return s.sessionsService().WatchSession(ctx, req, stream)
}

func (s *Service) GetGlobalEnvConfig(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.GlobalEnvConfigResponse], error) {
	return s.settingsService().GetGlobalEnvConfig(ctx, req)
}

func (s *Service) UpdateGlobalEnvConfig(ctx context.Context, req *connect.Request[agentcomposev1.UpdateGlobalEnvConfigRequest]) (*connect.Response[agentcomposev1.GlobalEnvConfigResponse], error) {
	return s.settingsService().UpdateGlobalEnvConfig(ctx, req)
}

func (s *Service) ListWorkspaceConfigs(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.ListWorkspaceConfigsResponse], error) {
	return s.settingsService().ListWorkspaceConfigs(ctx, req)
}

func (s *Service) CreateWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.CreateWorkspaceConfigRequest]) (*connect.Response[agentcomposev1.WorkspaceConfigResponse], error) {
	return s.settingsService().CreateWorkspaceConfig(ctx, req)
}

func (s *Service) UpdateWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.UpdateWorkspaceConfigRequest]) (*connect.Response[agentcomposev1.WorkspaceConfigResponse], error) {
	return s.settingsService().UpdateWorkspaceConfig(ctx, req)
}

func (s *Service) DeleteWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.WorkspaceConfigIDRequest]) (*connect.Response[emptypb.Empty], error) {
	return s.settingsService().DeleteWorkspaceConfig(ctx, req)
}

func (s *Service) ResumeSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return s.sessionsService().ResumeSession(ctx, req)
}

func (s *Service) reconcileSessionRuntimeState(ctx context.Context, session *model.Session) (*model.Session, error) {
	_ = s.sessionsService()
	return s.sessions.ReconcileSessionRuntimeState(ctx, session)
}

func (s *Service) StopSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return s.sessionsService().StopSession(ctx, req)
}

func (s *Service) GetSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return s.sessionsService().GetSession(ctx, req)
}

func (s *Service) ListSessions(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
	return s.sessionsService().ListSessions(ctx, req)
}

func (s *Service) ensureSessionProxyReady(ctx context.Context, sessionID string) (*model.Session, model.ProxyState, error) {
	return s.sessionsService().EnsureSessionProxyReady(ctx, sessionID)
}

func (s *Service) GetSessionProxy(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionProxyResponse], error) {
	return s.sessionsService().GetSessionProxy(ctx, req)
}

func (s *Service) ExecuteCell(ctx context.Context, req *connect.Request[agentcomposev1.ExecuteCellRequest]) (*connect.Response[agentcomposev1.ExecuteCellResponse], error) {
	return s.sessionsService().ExecuteCell(ctx, req)
}

func (s *Service) ExecuteCellStream(ctx context.Context, req *connect.Request[agentcomposev1.ExecuteCellRequest], stream *connect.ServerStream[agentcomposev1.ExecuteCellStreamResponse]) error {
	return s.sessionsService().ExecuteCellStream(ctx, req, stream)
}

func (s *Service) ListCells(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.ListCellsResponse], error) {
	return s.sessionsService().ListCells(ctx, req)
}

func (s *Service) SendAgentMessage(ctx context.Context, req *connect.Request[agentcomposev1.SendAgentMessageRequest]) (*connect.Response[agentcomposev1.SendAgentMessageResponse], error) {
	return s.sessionsService().SendAgentMessage(ctx, req)
}

func (s *Service) SendAgentMessageStream(ctx context.Context, req *connect.Request[agentcomposev1.SendAgentMessageRequest], stream *connect.ServerStream[agentcomposev1.SendAgentMessageStreamResponse]) error {
	return s.sessionsService().SendAgentMessageStream(ctx, req, stream)
}

type agentExecutionConfig struct {
	Provider          string
	AgentDefinitionID string
	Model             string
	EnvItems          []model.SessionEnvVar
}

func (s *Service) resolveSessionAgentConfig(ctx context.Context, session *model.Session, requested string) agentExecutionConfig {
	ownerConfig := agents.AgentExecutionConfigForSession(ctx, s.configDB, session, requested)
	return agentExecutionConfig{
		Provider:          ownerConfig.Provider,
		AgentDefinitionID: ownerConfig.AgentDefinitionID,
		Model:             ownerConfig.Model,
		EnvItems:          append([]model.SessionEnvVar(nil), ownerConfig.EnvItems...),
	}
}

func (s *Service) resolveSessionAgentConfigForSessions(ctx context.Context, session *model.Session, requested string) sessions.AgentExecutionConfig {
	config := s.resolveSessionAgentConfig(ctx, session, requested)
	return sessions.AgentExecutionConfig{
		Provider:          config.Provider,
		AgentDefinitionID: config.AgentDefinitionID,
		Model:             config.Model,
		EnvItems:          append([]model.SessionEnvVar(nil), config.EnvItems...),
	}
}

func (s *Service) ListSessionEvents(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.ListSessionEventsResponse], error) {
	return s.sessionsService().ListSessionEvents(ctx, req)
}

func (s *Service) Generate(ctx context.Context, req *connect.Request[agentcomposev1.GenerateLLMRequest]) (*connect.Response[agentcomposev1.GenerateLLMResponse], error) {
	if s == nil {
		return llmpkg.NewService(nil, nil, nil, nil).Generate(ctx, req)
	}
	service := llmpkg.NewService(s.config, s.store, s.configDB, s.llm)
	return service.Generate(ctx, req)
}
