package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/agents"
	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/capproxy"
	"agent-compose/pkg/dashboard"
	"agent-compose/pkg/imagecache"
	"agent-compose/pkg/images"
	llmpkg "agent-compose/pkg/llm"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/settings"
	"agent-compose/pkg/workspaces"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type Service struct {
	imageHandlers      *images.Service
	dashboardHandlers  *DashboardService
	capabilityHandlers *CapabilityService
	workspaceHandlers  *WorkspaceService
	settingsHandlers   *SettingsService
	config             *appconfig.Config
	store              *Store
	configDB           *ConfigStore
	driver             Driver
	runtimes           RuntimeProvider
	executor           *Executor
	loaders            *LoaderManager
	images             ImageBackend
	ociImages          ImageBackend
	autoImages         ImageBackend
	llm                *LLMClient
	cap                CapabilityProvider
	bus                *LoaderBus
	streams            *SessionStreamBroker
	dashboard          *DashboardOverviewHub
	events             *EventDispatcher
	sessions           *SessionRPCBridge
	agentHandlers      *AgentDefinitionService
	sessionHandlers    *sessions.Service
	loaderHandlers     *LoaderService
	projectHandlers    *ProjectService
	startedAt          time.Time
	startOnce          sync.Once
	startErr           error
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
	dashHub, _ := do.Invoke[*DashboardOverviewHub](di)
	if dashHub == nil {
		rootCtx, _ := do.Invoke[context.Context](di)
		if rootCtx == nil {
			rootCtx = context.Background()
		}
		dashHub = newDashboardOverviewHub(rootCtx, newDashboardOverviewAggregator(do.MustInvoke[*Store](di), do.MustInvoke[*ConfigStore](di)), 250*time.Millisecond)
	}
	capProvider := do.MustInvoke[capabilityIntegration](di)
	imageCacheRoot := strings.TrimSpace(config.ImageCacheRoot)
	if imageCacheRoot == "" {
		imageCacheRoot = filepath.Join(config.DataRoot, "images")
		config.ImageCacheRoot = imageCacheRoot
	}
	dockerImages := images.NewDockerImageBackend()
	ociCache, err := imagecache.New(imagecache.Config{
		Root:               imageCacheRoot,
		DefaultRegistry:    config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		return nil, err
	}
	config.ImageCacheRoot = ociCache.Root()
	ociImages := images.NewOCIImageBackend(ociCache)
	autoImages := images.NewAutoImageBackend(config.ImageStoreMode, dockerImages, ociImages)
	projectHandlers, _ := do.Invoke[*ProjectService](di)
	service := &Service{
		imageHandlers:      images.NewService(dockerImages, ociImages, autoImages),
		dashboardHandlers:  dashboard.NewService(dashHub),
		capabilityHandlers: capabilities.NewService(config, do.MustInvoke[*ConfigStore](di), capProvider),
		workspaceHandlers:  workspaces.NewService(config, do.MustInvoke[*ConfigStore](di)),
		settingsHandlers:   settings.NewService(do.MustInvoke[*ConfigStore](di), workspaces.NewService(config, do.MustInvoke[*ConfigStore](di)), capabilities.NewService(config, do.MustInvoke[*ConfigStore](di), capProvider)),
		config:             config,
		store:              do.MustInvoke[*Store](di),
		configDB:           do.MustInvoke[*ConfigStore](di),
		driver:             do.MustInvoke[Driver](di),
		runtimes:           do.MustInvoke[RuntimeProvider](di),
		executor:           do.MustInvoke[*Executor](di),
		loaders:            do.MustInvoke[*LoaderManager](di),
		images:             dockerImages,
		ociImages:          ociImages,
		autoImages:         autoImages,
		llm:                do.MustInvoke[*LLMClient](di),
		cap:                capProvider,
		bus:                do.MustInvoke[*LoaderBus](di),
		streams:            do.MustInvoke[*SessionStreamBroker](di),
		dashboard:          dashHub,
		events:             NewEventDispatcher(do.MustInvoke[context.Context](di), do.MustInvoke[*ConfigStore](di), do.MustInvoke[*LoaderBus](di)),
		sessions:           do.MustInvoke[*SessionRPCBridge](di),
		agentHandlers:      agents.NewService(config, do.MustInvoke[*Store](di), do.MustInvoke[*ConfigStore](di), do.MustInvoke[*SessionRPCBridge](di).componentBridge(), do.MustInvoke[*SessionStreamBroker](di).componentBroker()),
		loaderHandlers:     NewLoaderService(do.MustInvoke[*ConfigStore](di), do.MustInvoke[*LoaderManager](di), do.MustInvoke[*LoaderBus](di)),
		projectHandlers:    projectHandlers,
		startedAt:          time.Now().UTC(),
	}
	if service.projectHandlers == nil {
		service.projectHandlers = NewProjectServiceFromDeps(service)
	}
	service.loaders.SetProjectAgentRunner(service.projectHandlers)
	service.sessionHandlers = sessions.NewService(service.store, service.executor, service.bus, service.streams.componentBroker(), service.sessions.componentBridge(), service.resolveSessionAgentConfigForSessions)
	return service, nil
}

func Setup(di do.Injector) {
	Register(di)
	if err := StartBackground(di); err != nil {
		slog.Error("failed to start agent-compose background managers", "error", err)
	}
}

func Register(di do.Injector) {
	do.Provide(di, NewStore)
	do.Provide(di, NewConfigStore)
	do.Provide(di, NewRuntimeProvider)
	do.Provide(di, NewDriver)
	do.Provide(di, NewExecutor)
	do.Provide(di, NewLLMClient)
	do.Provide(di, capabilities.NewCapabilityProvider)
	do.Provide(di, capabilities.NewCapProxyServer)
	do.Provide(di, NewLoaderBus)
	do.Provide(di, NewSessionStreamBroker)
	do.Provide(di, dashboard.NewDashboardOverviewAggregator)
	do.Provide(di, dashboard.NewDashboardOverviewHub)
	do.Provide(di, NewLoaderEngine)
	do.Provide(di, NewSessionRPCBridge)
	do.Provide(di, NewLoaderManager)
	do.Provide(di, NewProjectService)
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
	if s.sessionHandlers != nil {
		return s.sessionHandlers
	}
	if s.streams == nil {
		s.streams = &SessionStreamBroker{subscribers: map[string]map[int]chan sessionWatchEvent{}}
	}
	if s.sessions == nil {
		s.sessions = &SessionRPCBridge{
			config:    s.config,
			store:     s.store,
			configDB:  s.configDB,
			driver:    s.driver,
			runtimes:  s.runtimes,
			bus:       s.bus,
			streams:   s.streams,
			cap:       s.cap,
			dashboard: s.dashboard,
		}
	}
	s.sessionHandlers = sessions.NewService(s.store, s.executor, s.bus, s.streams.componentBroker(), s.sessions.componentBridge(), s.resolveSessionAgentConfigForSessions)
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

func (s *Service) reconcileSessionRuntimeState(ctx context.Context, session *Session) (*Session, error) {
	_ = s.sessionsService()
	return s.sessions.componentBridge().ReconcileSessionRuntimeState(ctx, session)
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

func (s *Service) ensureSessionProxyReady(ctx context.Context, sessionID string) (*Session, ProxyState, error) {
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
	EnvItems          []SessionEnvVar
}

func (s *Service) resolveSessionAgentConfig(ctx context.Context, session *Session, requested string) agentExecutionConfig {
	provider := normalizeAgentKind(requested)
	config := agentExecutionConfig{Provider: provider}
	if session == nil {
		return config
	}
	agentID := sessionTagValue(session.Summary.Tags, agentSessionTagID)
	if agentID == "" || !sessionHasAgentTag(session, agentID) || s.configDB == nil {
		return config
	}
	agent, err := s.configDB.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return config
	}
	return agentExecutionConfigFromDefinition(agent, provider)
}

func (s *Service) resolveSessionAgentConfigForSessions(ctx context.Context, session *Session, requested string) sessions.AgentExecutionConfig {
	config := s.resolveSessionAgentConfig(ctx, session, requested)
	return sessions.AgentExecutionConfig{
		Provider:          config.Provider,
		AgentDefinitionID: config.AgentDefinitionID,
		Model:             config.Model,
		EnvItems:          append([]SessionEnvVar(nil), config.EnvItems...),
	}
}

func agentExecutionConfigFromDefinition(agent AgentDefinition, fallbackProvider string) agentExecutionConfig {
	provider := normalizeAgentKind(agent.Provider)
	if provider == "" {
		provider = normalizeAgentKind(fallbackProvider)
	}
	model := strings.TrimSpace(agent.Model)
	if provider == "opencode" {
		model = strings.TrimSpace(sessionEnvMap(agent.EnvItems)["OPENCODE_MODEL"])
	}
	return agentExecutionConfig{
		Provider:          provider,
		AgentDefinitionID: strings.TrimSpace(agent.ID),
		Model:             model,
		EnvItems:          append([]SessionEnvVar(nil), agent.EnvItems...),
	}
}

func sessionTagValue(tags []SessionTag, name string) string {
	for _, tag := range tags {
		if strings.TrimSpace(tag.Name) == name {
			return strings.TrimSpace(tag.Value)
		}
	}
	return ""
}

func (s *Service) ListSessionEvents(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.ListSessionEventsResponse], error) {
	return s.sessionsService().ListSessionEvents(ctx, req)
}

func (s *Service) Generate(ctx context.Context, req *connect.Request[agentcomposev1.GenerateLLMRequest]) (*connect.Response[agentcomposev1.GenerateLLMResponse], error) {
	var service *llmpkg.Service
	if s != nil {
		var client *llmpkg.LLMClient
		if s.llm != nil {
			client = s.llm.componentClient()
		}
		service = llmpkg.NewService(s.config, s.store, s.configDB, client)
	}
	return service.Generate(ctx, req)
}
