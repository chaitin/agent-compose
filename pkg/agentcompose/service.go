package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/capproxy"
	"agent-compose/pkg/imagecache"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type Service struct {
	config     *appconfig.Config
	store      *Store
	configDB   *ConfigStore
	driver     Driver
	runtimes   RuntimeProvider
	executor   *Executor
	loaders    *LoaderManager
	images     ImageBackend
	ociImages  ImageBackend
	autoImages ImageBackend
	llm        *LLMClient
	cap        CapabilityProvider
	bus        *LoaderBus
	streams    *SessionStreamBroker
	dashboard  *DashboardOverviewHub
	events     *EventDispatcher
	sessions   *SessionRPCBridge
	startedAt  time.Time
	startOnce  sync.Once
	startErr   error
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
	dashboard, _ := do.Invoke[*DashboardOverviewHub](di)
	if dashboard == nil {
		rootCtx, _ := do.Invoke[context.Context](di)
		if rootCtx == nil {
			rootCtx = context.Background()
		}
		dashboard = newDashboardOverviewHub(rootCtx, newDashboardOverviewAggregator(do.MustInvoke[*Store](di), do.MustInvoke[*ConfigStore](di)), 250*time.Millisecond)
	}
	imageCacheRoot := strings.TrimSpace(config.ImageCacheRoot)
	if imageCacheRoot == "" {
		imageCacheRoot = filepath.Join(config.DataRoot, "images")
		config.ImageCacheRoot = imageCacheRoot
	}
	dockerImages := NewDockerImageBackend()
	ociCache, err := imagecache.New(imagecache.Config{
		Root:               imageCacheRoot,
		DefaultRegistry:    config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		return nil, err
	}
	config.ImageCacheRoot = ociCache.Root()
	ociImages := NewOCIImageBackend(ociCache)
	autoImages := NewAutoImageBackend(config.ImageStoreMode, dockerImages, ociImages)
	return &Service{
		config:     config,
		store:      do.MustInvoke[*Store](di),
		configDB:   do.MustInvoke[*ConfigStore](di),
		driver:     do.MustInvoke[Driver](di),
		runtimes:   do.MustInvoke[RuntimeProvider](di),
		executor:   do.MustInvoke[*Executor](di),
		loaders:    do.MustInvoke[*LoaderManager](di),
		images:     dockerImages,
		ociImages:  ociImages,
		autoImages: autoImages,
		llm:        do.MustInvoke[*LLMClient](di),
		cap:        do.MustInvoke[capabilityIntegration](di),
		bus:        do.MustInvoke[*LoaderBus](di),
		streams:    do.MustInvoke[*SessionStreamBroker](di),
		dashboard:  dashboard,
		events:     NewEventDispatcher(do.MustInvoke[context.Context](di), do.MustInvoke[*ConfigStore](di), do.MustInvoke[*LoaderBus](di)),
		sessions:   do.MustInvoke[*SessionRPCBridge](di),
		startedAt:  time.Now().UTC(),
	}, nil
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
	do.Provide(di, NewCapabilityProvider)
	do.Provide(di, NewCapProxyServer)
	do.Provide(di, NewLoaderBus)
	do.Provide(di, NewSessionStreamBroker)
	do.Provide(di, NewDashboardOverviewAggregator)
	do.Provide(di, NewDashboardOverviewHub)
	do.Provide(di, NewLoaderEngine)
	do.Provide(di, NewSessionRPCBridge)
	do.Provide(di, NewLoaderManager)
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

func (s *Service) CreateSession(ctx context.Context, req *connect.Request[agentcomposev1.CreateSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return s.sessions.CreateSession(ctx, req)
}

func (s *Service) WatchSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest], stream *connect.ServerStream[agentcomposev1.WatchSessionResponse]) error {
	prepareStreamingHeaders(stream.ResponseHeader())
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if sendErr := stream.Send(&agentcomposev1.WatchSessionResponse{
		EventType: agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_SESSION_UPDATED,
		Session:   toProtoSessionSummary(&session.Summary),
	}); sendErr != nil {
		return connect.NewError(connect.CodeUnknown, sendErr)
	}
	events, cancel := s.streams.Subscribe(session.Summary.ID)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if sendErr := stream.Send(toProtoWatchSessionResponse(event)); sendErr != nil {
				return connect.NewError(connect.CodeUnknown, sendErr)
			}
		}
	}
}

func (s *Service) GetGlobalEnvConfig(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.GlobalEnvConfigResponse], error) {
	_ = req
	items, err := s.configDB.ListGlobalEnv(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(toProtoGlobalEnvConfig(items)), nil
}

func (s *Service) UpdateGlobalEnvConfig(ctx context.Context, req *connect.Request[agentcomposev1.UpdateGlobalEnvConfigRequest]) (*connect.Response[agentcomposev1.GlobalEnvConfigResponse], error) {
	items := make([]SessionEnvVar, 0, len(req.Msg.GetEnvItems()))
	for _, item := range req.Msg.GetEnvItems() {
		items = append(items, SessionEnvVar{Name: item.GetName(), Value: item.GetValue(), Secret: item.GetSecret()})
	}
	items = normalizeEnvItems(items)
	items, err := s.preserveUnchangedGlobalEnvSecrets(ctx, items)
	if err != nil {
		return nil, err
	}
	saved, err := s.configDB.ReplaceGlobalEnv(ctx, items)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(toProtoGlobalEnvConfig(saved)), nil
}

func (s *Service) preserveUnchangedGlobalEnvSecrets(ctx context.Context, items []SessionEnvVar) ([]SessionEnvVar, error) {
	existingItems, err := s.configDB.ListGlobalEnv(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	existingByName := make(map[string]SessionEnvVar, len(existingItems))
	for _, item := range existingItems {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		existingByName[name] = item
	}
	for index, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" || !item.Secret || strings.TrimSpace(item.Value) != "" {
			continue
		}
		existing, ok := existingByName[name]
		if !ok || !existing.Secret || existing.Value == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("secret env %s requires a value", name))
		}
		items[index].Value = existing.Value
	}
	return items, nil
}

func (s *Service) ListWorkspaceConfigs(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.ListWorkspaceConfigsResponse], error) {
	_ = req
	items, err := s.configDB.ListWorkspaceConfigs(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListWorkspaceConfigsResponse{}
	for _, item := range items {
		resp.Workspaces = append(resp.Workspaces, toProtoWorkspaceConfig(item))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) CreateWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.CreateWorkspaceConfigRequest]) (*connect.Response[agentcomposev1.WorkspaceConfigResponse], error) {
	configJSON := strings.TrimSpace(req.Msg.GetConfigJson())
	workspaceType := strings.ToLower(strings.TrimSpace(req.Msg.GetType()))
	workspaceID := ""
	if workspaceType == "file" {
		workspaceID = uuid.NewString()
		configJSON = defaultFileWorkspaceConfigJSON(s.config, workspaceID)
		if _, err := validateFileWorkspaceConfig(s.config, workspaceID, configJSON); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if err := s.checkFileWorkspaceContentCreatable(workspaceID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	item, err := s.configDB.CreateWorkspaceConfig(ctx, WorkspaceConfig{
		ID:         workspaceID,
		Name:       req.Msg.GetName(),
		Type:       workspaceType,
		ConfigJSON: configJSON,
		Comment:    req.Msg.GetComment(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if workspaceType == "file" {
		if err := s.createFileWorkspaceContent(item.ID, item.ConfigJSON); err != nil {
			deleteErr := s.configDB.DeleteWorkspaceConfig(ctx, item.ID)
			if deleteErr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create file workspace content: %w; rollback workspace config: %v", err, deleteErr))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewResponse(&agentcomposev1.WorkspaceConfigResponse{Workspace: toProtoWorkspaceConfig(item)}), nil
}

func (s *Service) UpdateWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.UpdateWorkspaceConfigRequest]) (*connect.Response[agentcomposev1.WorkspaceConfigResponse], error) {
	configJSON := strings.TrimSpace(req.Msg.GetConfigJson())
	workspaceType := strings.ToLower(strings.TrimSpace(req.Msg.GetType()))
	previous, err := s.configDB.GetWorkspaceConfig(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if workspaceType == "file" {
		configJSON = defaultFileWorkspaceConfigJSON(s.config, req.Msg.GetWorkspaceId())
		if _, err := validateFileWorkspaceConfig(s.config, req.Msg.GetWorkspaceId(), configJSON); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	wasFile := strings.EqualFold(strings.TrimSpace(previous.Type), "file")
	if workspaceType == "file" {
		if err := s.checkFileWorkspaceContentCreatable(req.Msg.GetWorkspaceId()); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	} else if wasFile {
		if err := s.checkFileWorkspaceContentRemovable(previous); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	item, err := s.configDB.UpdateWorkspaceConfig(ctx, WorkspaceConfig{
		ID:         req.Msg.GetWorkspaceId(),
		Name:       req.Msg.GetName(),
		Type:       workspaceType,
		ConfigJSON: configJSON,
		Comment:    req.Msg.GetComment(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if workspaceType == "file" {
		if err := s.createFileWorkspaceContent(item.ID, item.ConfigJSON); err != nil {
			_, rollbackErr := s.configDB.UpdateWorkspaceConfig(ctx, previous)
			if rollbackErr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create file workspace content: %w; rollback workspace config: %v", err, rollbackErr))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	} else if wasFile {
		if err := s.removeFileWorkspaceContent(previous); err != nil {
			_, rollbackErr := s.configDB.UpdateWorkspaceConfig(ctx, previous)
			if rollbackErr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("remove file workspace content: %w; rollback workspace config: %v", err, rollbackErr))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewResponse(&agentcomposev1.WorkspaceConfigResponse{Workspace: toProtoWorkspaceConfig(item)}), nil
}

func (s *Service) DeleteWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.WorkspaceConfigIDRequest]) (*connect.Response[emptypb.Empty], error) {
	workspace, err := s.configDB.GetWorkspaceConfig(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if strings.EqualFold(strings.TrimSpace(workspace.Type), "file") {
		if err := s.checkFileWorkspaceContentRemovable(workspace); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	if err := s.configDB.DeleteWorkspaceConfig(ctx, req.Msg.GetWorkspaceId()); err != nil {
		if strings.Contains(err.Error(), "referenced by") {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if strings.EqualFold(strings.TrimSpace(workspace.Type), "file") {
		if err := s.removeFileWorkspaceContent(workspace); err != nil {
			_, rollbackErr := s.configDB.CreateWorkspaceConfig(ctx, workspace)
			if rollbackErr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("remove file workspace content: %w; rollback workspace config: %v", err, rollbackErr))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (s *Service) createFileWorkspaceContent(workspaceID, configJSON string) error {
	content, err := openFileWorkspaceContent(s.config, WorkspaceConfig{
		ID:         workspaceID,
		Type:       "file",
		ConfigJSON: configJSON,
	})
	if err != nil {
		return err
	}
	return content.Root.Close()
}

func (s *Service) checkFileWorkspaceContentCreatable(workspaceID string) error {
	relRoot, err := fileWorkspaceContentRelRoot(workspaceID)
	if err != nil {
		return err
	}
	dataRoot, err := openFileWorkspaceDataRoot(s.config)
	if err != nil {
		return err
	}
	defer func() { _ = dataRoot.Close() }()
	for _, dir := range []string{"workspaces", filepath.ToSlash(filepath.Join("workspaces", strings.TrimSpace(workspaceID))), relRoot} {
		info, err := dataRoot.Lstat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("file workspace path %s is a symlink", dir)
		}
		if !info.IsDir() {
			return fmt.Errorf("file workspace path %s is not a directory", dir)
		}
	}
	return nil
}

func (s *Service) checkFileWorkspaceContentRemovable(workspace WorkspaceConfig) error {
	dataRoot, _, err := s.fileWorkspaceContentRemovalTarget(workspace)
	if err != nil {
		return err
	}
	return dataRoot.Close()
}

func (s *Service) removeFileWorkspaceContent(workspace WorkspaceConfig) error {
	dataRoot, relRoot, err := s.fileWorkspaceContentRemovalTarget(workspace)
	if err != nil {
		return err
	}
	defer func() { _ = dataRoot.Close() }()
	return dataRoot.RemoveAll(relRoot)
}

func (s *Service) fileWorkspaceContentRemovalTarget(workspace WorkspaceConfig) (*os.Root, string, error) {
	dataRoot, err := openFileWorkspaceDataRoot(s.config)
	if err != nil {
		return nil, "", err
	}
	relRoot, err := fileWorkspaceContentRelRoot(workspace.ID)
	if err != nil {
		_ = dataRoot.Close()
		return nil, "", err
	}
	info, err := dataRoot.Lstat(relRoot)
	if err != nil && !os.IsNotExist(err) {
		_ = dataRoot.Close()
		return nil, "", err
	}
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		_ = dataRoot.Close()
		return nil, "", fmt.Errorf("file workspace content root %s is a symlink", relRoot)
	}
	return dataRoot, relRoot, nil
}

func (s *Service) ResumeSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return s.sessions.ResumeSession(ctx, req)
}

func (s *Service) reconcileSessionRuntimeState(ctx context.Context, session *Session) (*Session, error) {
	if session == nil || session.Summary.VMStatus != VMStatusRunning {
		return session, nil
	}
	driver, err := driverpkg.ResolveSessionRuntimeDriver(session.Summary.Driver, s.config.RuntimeDriver)
	if err != nil {
		return nil, err
	}
	if driver != driverpkg.RuntimeDriverMicrosandbox {
		return session, nil
	}
	proxyState, err := s.store.GetProxyState(session.Summary.ID)
	if err != nil {
		return nil, err
	}
	if jupyterTargetReachable(proxyState, 250*time.Millisecond) {
		return session, nil
	}
	vmState, err := s.store.GetVMState(session.Summary.ID)
	if err != nil {
		return nil, err
	}
	runtime, err := s.runtimes.ForDriver(driver)
	if err != nil {
		return nil, err
	}
	microsandboxRuntime, ok := runtime.(sessionAliveRuntime)
	if !ok {
		return session, nil
	}
	alive, err := microsandboxRuntime.IsSessionAlive(ctx, session, vmState)
	if err != nil {
		return nil, err
	}
	if alive {
		return session, nil
	}
	now := time.Now().UTC()
	vmState.StoppedAt = now
	vmState.LastError = ""
	vmState.BoxID = ""
	if err := s.store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return nil, err
	}
	session.Summary.VMStatus = VMStatusStopped
	if err := s.store.UpdateSession(ctx, session); err != nil {
		return nil, err
	}
	if s.configDB != nil {
		_ = s.configDB.RevokeLLMFacadeTokensForSession(ctx, session.Summary.ID)
	}
	_ = s.store.AddEvent(ctx, session.Summary.ID, SessionEvent{
		ID:        uuid.NewString(),
		Type:      "session.runtime_lost",
		Level:     "warn",
		Message:   "session marked stopped after microsandbox runtime became unreachable",
		CreatedAt: now,
	})
	return s.store.GetSession(ctx, session.Summary.ID)
}

func (s *Service) StopSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return s.sessions.StopSession(ctx, req)
}

func (s *Service) GetSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return s.sessions.GetSession(ctx, req)
}

func (s *Service) ListSessions(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
	return s.sessions.ListSessions(ctx, req)
}

func jupyterTargetReachable(proxyState ProxyState, timeout time.Duration) bool {
	_, port := driverpkg.JupyterConnectTarget(toDriverProxyState(proxyState))
	if port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", driverpkg.JupyterConnectAddress(toDriverProxyState(proxyState)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (s *Service) ensureSessionProxyReady(ctx context.Context, sessionID string) (*Session, ProxyState, error) {
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, ProxyState{}, err
	}
	proxyState, err := s.store.GetProxyState(session.Summary.ID)
	if err != nil {
		return nil, ProxyState{}, err
	}
	if session.Summary.VMStatus == VMStatusRunning && jupyterTargetReachable(proxyState, 1500*time.Millisecond) {
		return session, proxyState, nil
	}
	startCtx, cancel := context.WithTimeout(ctx, s.config.SessionStartTimeout)
	defer cancel()
	if err := prepareSessionWorkspace(startCtx, s.config, s.configDB, session); err != nil {
		session.Summary.VMStatus = VMStatusFailed
		_ = s.store.UpdateSession(ctx, session)
		return nil, ProxyState{}, err
	}
	if err := s.driver.StartSessionVM(startCtx, session); err != nil {
		session.Summary.VMStatus = VMStatusFailed
		_ = s.store.UpdateSession(ctx, session)
		return nil, ProxyState{}, err
	}
	session.Summary.VMStatus = VMStatusRunning
	if err := s.store.UpdateSession(ctx, session); err != nil {
		return nil, ProxyState{}, err
	}
	loaded, err := s.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, ProxyState{}, err
	}
	proxyState, err = s.store.GetProxyState(session.Summary.ID)
	if err != nil {
		return nil, ProxyState{}, err
	}
	return loaded, proxyState, nil
}

func (s *Service) GetSessionProxy(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionProxyResponse], error) {
	return s.sessions.GetSessionProxy(ctx, req)
}

func (s *Service) ExecuteCell(ctx context.Context, req *connect.Request[agentcomposev1.ExecuteCellRequest]) (*connect.Response[agentcomposev1.ExecuteCellResponse], error) {
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if session.Summary.VMStatus != VMStatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is not running"))
	}
	cell, err := s.executor.ExecuteCell(ctx, session, fromProtoCellType(req.Msg.GetType()), req.Msg.GetSource())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	loaded, err := s.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.publishLoaderTopic("agent-compose.cell.completed", cellTopicPayload(session.Summary.ID, cell, "api"))
	return connect.NewResponse(&agentcomposev1.ExecuteCellResponse{Session: toProtoSessionSummary(&loaded.Summary), Cell: toProtoCell(cell)}), nil
}

func (s *Service) ExecuteCellStream(ctx context.Context, req *connect.Request[agentcomposev1.ExecuteCellRequest], stream *connect.ServerStream[agentcomposev1.ExecuteCellStreamResponse]) error {
	prepareStreamingHeaders(stream.ResponseHeader())
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if session.Summary.VMStatus != VMStatusRunning {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is not running"))
	}

	streamErr := func(sendErr error) error {
		if sendErr == nil {
			return nil
		}
		return connect.NewError(connect.CodeUnknown, sendErr)
	}
	cell, err := s.executor.ExecuteCellStream(ctx, session, fromProtoCellType(req.Msg.GetType()), req.Msg.GetSource(), CellExecutionStream{
		OnStart: func(cell NotebookCell) error {
			return streamErr(stream.Send(&agentcomposev1.ExecuteCellStreamResponse{
				EventType: agentcomposev1.ExecuteCellStreamEventType_EXECUTE_CELL_STREAM_EVENT_TYPE_STARTED,
				Session:   toProtoSessionSummary(&session.Summary),
				Cell:      toProtoCell(cell),
				CellId:    cell.ID,
			}))
		},
		OnChunk: func(cellID string, chunk ExecChunk) error {
			if chunk.Text == "" {
				return nil
			}
			return streamErr(stream.Send(&agentcomposev1.ExecuteCellStreamResponse{
				EventType: agentcomposev1.ExecuteCellStreamEventType_EXECUTE_CELL_STREAM_EVENT_TYPE_OUTPUT,
				CellId:    cellID,
				Chunk:     chunk.Text,
				IsStderr:  chunk.IsStderr,
			}))
		},
	})
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	loaded, err := s.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	s.publishLoaderTopic("agent-compose.cell.completed", cellTopicPayload(session.Summary.ID, cell, "api"))
	return streamErr(stream.Send(&agentcomposev1.ExecuteCellStreamResponse{
		EventType: agentcomposev1.ExecuteCellStreamEventType_EXECUTE_CELL_STREAM_EVENT_TYPE_COMPLETED,
		Session:   toProtoSessionSummary(&loaded.Summary),
		Cell:      toProtoCell(cell),
		CellId:    cell.ID,
	}))
}

func (s *Service) ListCells(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.ListCellsResponse], error) {
	cells, err := s.store.ListCells(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListCellsResponse{SessionId: req.Msg.GetSessionId()}
	for _, cell := range cells {
		resp.Cells = append(resp.Cells, toProtoCell(cell))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) SendAgentMessage(ctx context.Context, req *connect.Request[agentcomposev1.SendAgentMessageRequest]) (*connect.Response[agentcomposev1.SendAgentMessageResponse], error) {
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if session.Summary.VMStatus != VMStatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is not running"))
	}
	message := strings.TrimSpace(req.Msg.GetMessage())
	if message == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("message is required"))
	}
	agentConfig := s.resolveSessionAgentConfig(ctx, session, req.Msg.GetAgent())
	cell, userEvent, assistantEvent, err := s.executor.ExecuteAgentRequest(ctx, session, ExecuteAgentRequest{
		Agent:             agentConfig.Provider,
		AgentDefinitionID: agentConfig.AgentDefinitionID,
		Model:             agentConfig.Model,
		ProviderEnvItems:  agentConfig.EnvItems,
		Message:           message,
	})
	_ = cell
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.publishLoaderTopic("agent-compose.agent.completed", cellTopicPayload(session.Summary.ID, cell, "api"))
	return connect.NewResponse(&agentcomposev1.SendAgentMessageResponse{UserEvent: toProtoEvent(userEvent), AssistantEvent: toProtoEvent(assistantEvent)}), nil
}

func (s *Service) SendAgentMessageStream(ctx context.Context, req *connect.Request[agentcomposev1.SendAgentMessageRequest], stream *connect.ServerStream[agentcomposev1.SendAgentMessageStreamResponse]) error {
	prepareStreamingHeaders(stream.ResponseHeader())
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if session.Summary.VMStatus != VMStatusRunning {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is not running"))
	}
	message := strings.TrimSpace(req.Msg.GetMessage())
	if message == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("message is required"))
	}
	agentConfig := s.resolveSessionAgentConfig(ctx, session, req.Msg.GetAgent())

	streamErr := func(sendErr error) error {
		if sendErr == nil {
			return nil
		}
		return connect.NewError(connect.CodeUnknown, sendErr)
	}

	cell, userEvent, assistantEvent, err := s.executor.ExecuteAgentRequest(ctx, session, ExecuteAgentRequest{
		Agent:             agentConfig.Provider,
		AgentDefinitionID: agentConfig.AgentDefinitionID,
		Model:             agentConfig.Model,
		ProviderEnvItems:  agentConfig.EnvItems,
		Message:           message,
		Stream: AgentExecutionStream{
			OnStart: func(cell NotebookCell) error {
				return streamErr(stream.Send(&agentcomposev1.SendAgentMessageStreamResponse{
					EventType: agentcomposev1.SendAgentMessageStreamEventType_SEND_AGENT_MESSAGE_STREAM_EVENT_TYPE_STARTED,
					Session:   toProtoSessionSummary(&session.Summary),
					Run:       toProtoAgentRun(cell),
					RunId:     cell.ID,
				}))
			},
			OnChunk: func(cellID string, chunk ExecChunk) error {
				return streamErr(stream.Send(&agentcomposev1.SendAgentMessageStreamResponse{
					EventType: agentcomposev1.SendAgentMessageStreamEventType_SEND_AGENT_MESSAGE_STREAM_EVENT_TYPE_OUTPUT,
					RunId:     cellID,
					Chunk:     chunk.Text,
					IsStderr:  chunk.IsStderr,
				}))
			},
		},
	})
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	loaded, err := s.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	s.publishLoaderTopic("agent-compose.agent.completed", cellTopicPayload(session.Summary.ID, cell, "api"))
	return streamErr(stream.Send(&agentcomposev1.SendAgentMessageStreamResponse{
		EventType:      agentcomposev1.SendAgentMessageStreamEventType_SEND_AGENT_MESSAGE_STREAM_EVENT_TYPE_COMPLETED,
		Session:        toProtoSessionSummary(&loaded.Summary),
		Run:            toProtoAgentRun(cell),
		RunId:          cell.ID,
		UserEvent:      toProtoEvent(userEvent),
		AssistantEvent: toProtoEvent(assistantEvent),
	}))
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

func applyAgentProviderEnv(session *Session, agentEnv []SessionEnvVar) {
	if session == nil || len(agentEnv) == 0 {
		return
	}
	providerEnv := session.ProviderEnvItems
	if len(providerEnv) == 0 {
		providerEnv = session.EnvItems
	}
	session.ProviderEnvItems = mergeEnvItems(agentEnv, providerEnv)
}

func sessionTagValue(tags []SessionTag, name string) string {
	for _, tag := range tags {
		if strings.TrimSpace(tag.Name) == name {
			return strings.TrimSpace(tag.Value)
		}
	}
	return ""
}

func prepareStreamingHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	headers.Set("Cache-Control", "no-cache, no-transform")
	headers.Set("X-Accel-Buffering", "no")
}

func (s *Service) ListSessionEvents(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.ListSessionEventsResponse], error) {
	events, err := s.store.ListEvents(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListSessionEventsResponse{SessionId: req.Msg.GetSessionId()}
	for _, event := range events {
		resp.Events = append(resp.Events, toProtoEvent(event))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) Generate(ctx context.Context, req *connect.Request[agentcomposev1.GenerateLLMRequest]) (*connect.Response[agentcomposev1.GenerateLLMResponse], error) {
	if s.llm == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("llm client is unavailable"))
	}
	result, err := s.llm.Generate(ctx, req.Msg.GetPrompt(), req.Msg.GetModel(), req.Msg.GetOutputSchema())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev1.GenerateLLMResponse{
		Text:         result.Text,
		Model:        result.Model,
		ResponseId:   result.ResponseID,
		FinishReason: result.FinishReason,
		Json:         llmJSONResponseText(result.Text, req.Msg.GetOutputSchema()),
	}), nil
}

func llmJSONResponseText(text, outputSchemaJSON string) string {
	if strings.TrimSpace(outputSchemaJSON) == "" {
		return ""
	}
	return strings.TrimSpace(text)
}

func normalizeAgentKind(agent string) string {
	agent = strings.ToLower(strings.TrimSpace(agent))
	switch agent {
	case "":
		return ""
	case "codex":
		return "codex"
	case "claude", "claude-code", "claude_code":
		return "claude"
	case "gemini", "gemini-cli", "gemini_cli":
		return "gemini"
	case "opencode", "open-code", "open_code":
		return "opencode"
	default:
		return agent
	}
}

type agentExecResponse struct {
	Provider   string `json:"provider"`
	SessionID  string `json:"sessionId"`
	StopReason string `json:"stopReason"`
	FinalText  string `json:"finalText"`
	JSON       any    `json:"json"`
	Transcript string `json:"transcript"`
	Stderr     string `json:"stderr"`
}

const agentResultPrefix = "__AGENT_RESULT__"
const commandResultPrefix = "__COMMAND_RESULT__"

type runtimeCommandRequestJSON struct {
	Mode           string            `json:"mode"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Script         string            `json:"script,omitempty"`
	Cwd            string            `json:"cwd"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutMs      int64             `json:"timeoutMs,omitempty"`
	MaxOutputBytes int64             `json:"maxOutputBytes"`
	ArtifactDir    string            `json:"artifactDir"`
}

const agentSystemPromptFileName = "system-prompt.txt" // keep in sync with runtime/javascript/src/system-context.ts

// hostAgentSystemPromptPath is the session agent identity file the host writes and the
// guest reads via convention from --state-root (guest /data/state/agents/system-prompts/system-prompt.txt).
// Returns "" when the session workspace path is unknown.
func hostAgentSystemPromptPath(session *Session) string {
	if session == nil || strings.TrimSpace(session.Summary.WorkspacePath) == "" {
		return ""
	}
	return filepath.Join(hostSessionDir(session), "state", "agents", "system-prompts", agentSystemPromptFileName)
}

func writeAgentPromptFile(config *appconfig.Config, session *Session, agent, message string) (string, error) {
	hostSessionDir := filepath.Dir(session.Summary.WorkspacePath)
	promptDir := filepath.Join(hostSessionDir, "state", "agents", "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		return "", fmt.Errorf("create agent prompt dir: %w", err)
	}
	name := fmt.Sprintf("%s-%d.txt", normalizeAgentKind(agent), time.Now().UTC().UnixNano())
	hostPath := filepath.Join(promptDir, name)
	if err := os.WriteFile(hostPath, []byte(message), 0o644); err != nil {
		return "", fmt.Errorf("write agent prompt file: %w", err)
	}
	return filepath.Join(config.GuestStateRoot, "agents", "prompts", name), nil
}

// writeAgentSystemPromptFile materializes agent identity for the guest runtime at a
// fixed convention path under the session state tree:
//
//	state/agents/system-prompts/system-prompt.txt
//
// The guest discovers this file from --state-root (no CLI flag). When systemPrompt is
// empty, the file is removed so later runs in the same session cannot read stale identity.
func writeAgentSystemPromptFile(session *Session, systemPrompt string) error {
	systemPrompt = strings.TrimSpace(systemPrompt)
	hostPath := hostAgentSystemPromptPath(session)
	if hostPath == "" {
		if systemPrompt == "" {
			return nil
		}
		return fmt.Errorf("session workspace path is required to write agent system prompt")
	}
	if systemPrompt == "" {
		if err := os.Remove(hostPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove agent system prompt file: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
		return fmt.Errorf("create agent system prompt dir: %w", err)
	}
	if err := os.WriteFile(hostPath, []byte(systemPrompt), 0o644); err != nil {
		return fmt.Errorf("write agent system prompt file: %w", err)
	}
	return nil
}

func writeAgentOutputSchemaFile(config *appconfig.Config, session *Session, agent, schemaJSON string) (string, error) {
	schemaJSON = strings.TrimSpace(schemaJSON)
	if schemaJSON == "" {
		return "", nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(schemaJSON), &decoded); err != nil {
		return "", fmt.Errorf("decode agent output schema json: %w", err)
	}
	if _, ok := decoded.(map[string]any); !ok {
		return "", fmt.Errorf("agent output schema must be a JSON object")
	}
	hostSessionDir := filepath.Dir(session.Summary.WorkspacePath)
	schemaDir := filepath.Join(hostSessionDir, "state", "agents", "schemas")
	if err := os.MkdirAll(schemaDir, 0o755); err != nil {
		return "", fmt.Errorf("create agent schema dir: %w", err)
	}
	name := fmt.Sprintf("%s-%d.json", normalizeAgentKind(agent), time.Now().UTC().UnixNano())
	hostPath := filepath.Join(schemaDir, name)
	if err := os.WriteFile(hostPath, []byte(schemaJSON), 0o644); err != nil {
		return "", fmt.Errorf("write agent schema file: %w", err)
	}
	return filepath.Join(config.GuestStateRoot, "agents", "schemas", name), nil
}

func findAgentExecPayload(raw string) (agentExecResponse, bool) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, agentResultPrefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, agentResultPrefix))
		}
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var payload agentExecResponse
		if json.Unmarshal([]byte(line), &payload) == nil {
			return payload, true
		}
	}
	return agentExecResponse{}, false
}

func parseAgentExecResult(agent string, result ExecResult) (AgentRunResult, error) {
	raw := firstNonEmpty(result.Stdout, result.Output)
	if strings.TrimSpace(raw) == "" {
		if detail := summarizeAgentExecFailure(result); detail != "" {
			return AgentRunResult{}, fmt.Errorf("agent %s returned empty stdout: %s", agent, detail)
		}
		return AgentRunResult{}, fmt.Errorf("agent %s returned empty stdout", agent)
	}
	payload, ok := findAgentExecPayload(raw)
	if !ok && strings.TrimSpace(result.Output) != strings.TrimSpace(raw) {
		payload, ok = findAgentExecPayload(result.Output)
	}
	if !ok {
		if detail := summarizeAgentExecFailure(result); detail != "" {
			return AgentRunResult{}, fmt.Errorf("decode agent result for %s: no result payload found: %s", agent, detail)
		}
		return AgentRunResult{}, fmt.Errorf("decode agent result for %s: no result payload found", agent)
	}
	humanOutput := strings.TrimSpace(result.Stderr)
	if transcript := strings.TrimSpace(payload.Transcript); transcript != "" {
		humanOutput = transcript
	} else if strings.TrimSpace(humanOutput) == "" {
		humanOutput = strings.TrimSpace(payload.FinalText)
	}
	return AgentRunResult{
		Agent:         firstNonEmpty(strings.TrimSpace(payload.Provider), normalizeAgentKind(agent)),
		DisplayOutput: humanOutput,
		FinalText:     strings.TrimSpace(payload.FinalText),
		JSONText:      strings.TrimSpace(payload.FinalText),
		Transcript:    strings.TrimSpace(payload.Transcript),
		SessionID:     strings.TrimSpace(payload.SessionID),
		StopReason:    strings.TrimSpace(payload.StopReason),
		ExitCode:      result.ExitCode,
		Success:       result.Success,
	}, nil
}

func agentTraceEvents(transcript string, createdAt time.Time) []SessionEvent {
	lines := strings.Split(transcript, "\n")
	events := make([]SessionEvent, 0)
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		eventType, name, ok := parseAgentTraceMarker(line)
		if !ok {
			continue
		}
		details, consumed := collectAgentTraceDetails(eventType, lines[index+1:])
		index += consumed
		message := name
		if strings.TrimSpace(details) != "" {
			if message == "" {
				message = strings.TrimSpace(details)
			} else {
				message += "\n" + strings.TrimSpace(details)
			}
		}
		events = append(events, SessionEvent{
			ID:        uuid.NewString(),
			Type:      eventType,
			Level:     "info",
			Message:   message,
			CreatedAt: createdAt,
		})
	}
	return events
}

func collectAgentTraceDetails(eventType string, lines []string) (string, int) {
	details := make([]string, 0, len(lines))
	for offset, raw := range lines {
		line := strings.TrimSpace(raw)
		if _, _, marker := parseAgentTraceMarker(line); marker {
			return strings.Join(details, "\n"), offset
		}
		if eventType != "agent.assistant" && line == "" {
			return strings.Join(details, "\n"), offset + 1
		}
		details = append(details, raw)
	}
	return strings.Join(details, "\n"), len(lines)
}

func parseAgentTraceMarker(line string) (string, string, bool) {
	if strings.HasPrefix(line, "[tool:") && strings.HasSuffix(line, "]") {
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "[tool:"), "]"))
		if name != "" {
			return "agent.tool", name, true
		}
	}
	if strings.HasPrefix(line, "[hook:") && strings.HasSuffix(line, "]") {
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "[hook:"), "]"))
		if name != "" {
			return "agent.hook", name, true
		}
	}
	return "", "", false
}

func validateLoaderCommandRequest(request LoaderCommandRequest) error {
	switch strings.ToLower(strings.TrimSpace(request.Mode)) {
	case "exec":
		if strings.TrimSpace(request.Command) == "" {
			return fmt.Errorf("command is required")
		}
	case "shell":
		if strings.TrimSpace(request.Script) == "" {
			return fmt.Errorf("script is required")
		}
	default:
		return fmt.Errorf("loader command mode must be exec or shell")
	}
	return nil
}

func loaderCommandContext(ctx context.Context, timeoutMs int64) (context.Context, context.CancelFunc) {
	if timeoutMs <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
}

func loaderCommandCellSource(request LoaderCommandRequest) string {
	if strings.EqualFold(strings.TrimSpace(request.Mode), "shell") {
		return request.Script
	}
	items := append([]string{request.Command}, request.Args...)
	return strings.Join(items, " ")
}

func runtimeCommandRequestPayload(config *appconfig.Config, request LoaderCommandRequest, guestCellDir string) runtimeCommandRequestJSON {
	appconfig.ApplyDefaultGuestPaths(config)
	maxOutputBytes := request.MaxOutputBytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = defaultLoaderCommandMaxOutputBytes
	}
	cwd := strings.TrimSpace(request.Cwd)
	if cwd == "" {
		cwd = config.GuestWorkspacePath
	}
	return runtimeCommandRequestJSON{
		Mode:           strings.ToLower(strings.TrimSpace(request.Mode)),
		Command:        request.Command,
		Args:           append([]string(nil), request.Args...),
		Script:         request.Script,
		Cwd:            cwd,
		Env:            request.Env,
		TimeoutMs:      request.TimeoutMs,
		MaxOutputBytes: maxOutputBytes,
		ArtifactDir:    guestCellDir,
	}
}

func buildLoaderCommandExecSpec(config *appconfig.Config, session *Session, guestRequestPath string) ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	commandHome := guestSessionHome(config)
	env := buildSessionExecEnv(config, session, commandHome)

	command := strings.Join([]string{
		"set -e",
		"cd " + shellQuote(config.GuestWorkspacePath),
		"mkdir -p " + shellQuote(commandHome),
		"agent-compose-runtime exec" +
			" --request-file " + shellQuote(guestRequestPath) +
			" --state-root " + shellQuote(config.GuestStateRoot) +
			" --workspace " + shellQuote(config.GuestWorkspacePath) +
			" --home " + shellQuote(commandHome),
	}, " && ")

	return ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", command},
		Env:     env,
		Cwd:     config.GuestWorkspacePath,
	}
}

func buildSessionExecEnv(config *appconfig.Config, session *Session, home string) map[string]string {
	appconfig.ApplyDefaultGuestPaths(config)
	env := runtimeEnvMap(session.EnvItems)
	if env == nil {
		env = map[string]string{}
	}
	for key, value := range managedRuntimeEnvMap(session.RuntimeEnvItems) {
		env[key] = value
	}
	env["GOPATH"] = "/usr/local/go"
	env["PATH"] = "/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	env["SESSION_ID"] = session.Summary.ID
	env["WORKSPACE"] = config.GuestWorkspacePath
	env["STATE_ROOT"] = config.GuestStateRoot
	env["RUNTIME_ROOT"] = config.GuestRuntimeRoot
	env["VERSION"] = config.Version
	return env
}

func findCommandExecPayload(raw string) (RuntimeCommandResult, bool) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, commandResultPrefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, commandResultPrefix))
		}
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var payload RuntimeCommandResult
		if json.Unmarshal([]byte(line), &payload) == nil {
			return payload, true
		}
	}
	return RuntimeCommandResult{}, false
}

func parseCommandExecResult(result ExecResult) (RuntimeCommandResult, error) {
	raw := firstNonEmpty(result.Stdout, result.Output)
	if strings.TrimSpace(raw) == "" {
		return RuntimeCommandResult{}, fmt.Errorf("decode command result: empty stdout")
	}
	payload, ok := findCommandExecPayload(raw)
	if !ok && strings.TrimSpace(result.Output) != strings.TrimSpace(raw) {
		payload, ok = findCommandExecPayload(result.Output)
	}
	if !ok {
		return RuntimeCommandResult{}, fmt.Errorf("decode command result: no result payload found")
	}
	return payload, nil
}

func mirrorRuntimeCommandArtifacts(hostCellDir string, result RuntimeCommandResult) error {
	files := map[string]string{
		"stdout.txt": result.Stdout,
		"stderr.txt": result.Stderr,
		"output.txt": result.Output,
	}
	for name, content := range files {
		path := filepath.Join(hostCellDir, name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write command artifact %s: %w", name, err)
		}
	}
	// command-result.json is written by the guest runtime (agent-compose-runtime exec)
	// directly into the shared cell dir, so the host must NOT write it again.
	// Re-writing it here clobbers the guest's file, which fails whenever the host
	// process and the guest container run as different users (e.g. host=ubuntu,
	// guest=root with the docker driver): the host cannot truncate/overwrite the
	// root-owned file, surfacing as "write command result artifact: permission denied".
	return nil
}

func summarizeAgentExecFailure(result ExecResult) string {
	detail := strings.TrimSpace(firstNonEmpty(result.Stderr, result.Output, result.Stdout))
	if detail == "" {
		return ""
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > 240 {
		detail = detail[:240] + "..."
	}
	return detail
}

func stripAgentResultPayload(raw string) string {
	idx := strings.LastIndex(raw, agentResultPrefix)
	if idx < 0 {
		return raw
	}
	return raw[:idx]
}

func sanitizeAgentExecResult(result ExecResult) ExecResult {
	cleaned := result
	cleaned.Stdout = stripAgentResultPayload(result.Stdout)
	cleaned.Output = stripAgentResultPayload(result.Output)
	return cleaned
}

func (e *Executor) resolveAgentSystemPrompt(ctx context.Context, session *Session, agentDefinitionID string) (string, error) {
	if e == nil || e.configDB == nil {
		return "", nil
	}
	agentID := strings.TrimSpace(agentDefinitionID)
	if agentID == "" {
		taggedAgentID := sessionTagValue(session.Summary.Tags, agentSessionTagID)
		if !sessionHasAgentTag(session, taggedAgentID) {
			return "", nil
		}
		agentID = taggedAgentID
	}
	if agentID == "" {
		return "", nil
	}
	agentDef, err := e.configDB.GetAgentDefinition(ctx, agentID)
	if err != nil {
		// Agent identity is optional at execution time; lookup failures degrade to MPI-only context.
		slog.Warn("resolve agent system prompt failed", "agent_id", agentID, "error", err)
		return "", nil
	}
	return strings.TrimSpace(agentDef.SystemPrompt), nil
}

func (e *Executor) executeAgentRun(ctx context.Context, session *Session, agent, agentDefinitionID, model, runID, message, outputSchemaJSON string, stream ExecStreamWriter) (ExecResult, AgentRunResult, error) {
	if session.Summary.VMStatus != VMStatusRunning {
		return ExecResult{}, AgentRunResult{}, fmt.Errorf("session is not running")
	}
	appconfig.ApplyDefaultGuestPaths(e.config)
	vmState, err := e.store.GetVMState(session.Summary.ID)
	if err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	promptPath, err := writeAgentPromptFile(e.config, session, agent, message)
	if err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	schemaPath, err := writeAgentOutputSchemaFile(e.config, session, agent, outputSchemaJSON)
	if err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	systemPrompt, err := e.resolveAgentSystemPrompt(ctx, session, agentDefinitionID)
	if err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	if err := writeAgentSystemPromptFile(session, systemPrompt); err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	runtime, err := e.runtimes.ForSession(session)
	if err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	spec := buildAgentExecSpec(e.config, session, agent, model, promptPath, schemaPath)
	managedEnv, err := ensureSessionLLMFacadeConfig(ctx, e.config, e.configDB, session, agent, model, llmFacadeTokenSourceAgent, runID)
	if err != nil {
		return ExecResult{}, AgentRunResult{}, err
	}
	if len(managedEnv) > 0 {
		spec.Env = mergeManagedExecEnv(spec.Env, managedEnv)
		// The per-run facade token is only needed while this bounded run executes.
		// Retire it as soon as the run returns so live tokens never accumulate over
		// the lifetime of a long-running session. Use a detached context because the
		// run context may already be cancelled (e.g. timeout) by the time this runs.
		if e.configDB != nil {
			if token := managedEnv["AGENT_COMPOSE_SESSION_TOKEN"]; token != "" {
				defer func() { _ = e.configDB.DeleteLLMFacadeToken(context.WithoutCancel(ctx), token) }()
			}
		}
	}
	result, err := runtime.ExecStream(ctx, session, vmState, spec, stream)
	if err != nil {
		return sanitizeAgentExecResult(result), AgentRunResult{}, err
	}
	parsed, err := parseAgentExecResult(agent, result)
	if err != nil {
		return sanitizeAgentExecResult(result), AgentRunResult{}, err
	}
	return sanitizeAgentExecResult(result), parsed, nil
}

func buildAgentExecSpec(config *appconfig.Config, session *Session, agent, model, promptPath, schemaPath string) ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	agentHome := guestSessionHome(config)
	env := buildSessionExecEnv(config, session, agentHome)

	promptCommand := "agent-compose-runtime prompt" +
		" --provider " + shellQuote(agent) +
		" --message-file " + shellQuote(promptPath) +
		" --state-root " + shellQuote(config.GuestStateRoot) +
		" --workspace " + shellQuote(config.GuestWorkspacePath) +
		" --home " + shellQuote(agentHome)
	if strings.TrimSpace(model) != "" {
		promptCommand += " --model " + shellQuote(strings.TrimSpace(model))
	}
	if strings.TrimSpace(schemaPath) != "" {
		promptCommand += " --output-schema-file " + shellQuote(schemaPath)
	}
	command := strings.Join([]string{
		"set -e",
		"cd " + shellQuote(config.GuestWorkspacePath),
		"mkdir -p " + shellQuote(agentHome),
		promptCommand,
	}, " && ")

	return ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", command},
		Env:     env,
		Cwd:     config.GuestWorkspacePath,
	}
}

func summarizeAgentResult(result AgentRunResult) string {
	body := firstNonEmpty(result.FinalText, result.DisplayOutput, result.Transcript)
	if strings.TrimSpace(body) == "" {
		if result.Success {
			return fmt.Sprintf("%s finished without output", result.Agent)
		}
		return fmt.Sprintf("%s failed without output", result.Agent)
	}
	return body
}

func toProtoSessionDetail(session *Session) *agentcomposev1.SessionDetail {
	resp := &agentcomposev1.SessionDetail{Summary: toProtoSessionSummary(&session.Summary), WorkspaceId: session.WorkspaceID, Workspace: toProtoSessionWorkspace(session.Workspace)}
	for _, item := range session.EnvItems {
		value := item.Value
		if item.Secret && value != "" {
			value = "********"
		}
		resp.EnvItems = append(resp.EnvItems, &agentcomposev1.SessionEnvVar{Name: item.Name, Value: value, Secret: item.Secret})
	}
	return resp
}

func toProtoSessionSummary(summary *SessionSummary) *agentcomposev1.SessionSummary {
	resp := &agentcomposev1.SessionSummary{
		SessionId:     summary.ID,
		Title:         summary.Title,
		TriggerSource: summary.TriggerSource,
		Driver:        summary.Driver,
		VmStatus:      summary.VMStatus,
		GuestImage:    summary.GuestImage,
		WorkspacePath: summary.WorkspacePath,
		ProxyPath:     summary.ProxyPath,
		CreatedAt:     summary.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:     summary.UpdatedAt.Format(time.RFC3339Nano),
		CellCount:     uint32(summary.CellCount),
		EventCount:    uint32(summary.EventCount),
	}
	for _, tag := range summary.Tags {
		resp.Tags = append(resp.Tags, &agentcomposev1.SessionTag{Name: tag.Name, Value: tag.Value})
	}
	return resp
}

func toProtoGlobalEnvConfig(items []SessionEnvVar) *agentcomposev1.GlobalEnvConfigResponse {
	resp := &agentcomposev1.GlobalEnvConfigResponse{}
	for _, item := range items {
		value := item.Value
		if item.Secret && value != "" {
			value = "********"
		}
		resp.EnvItems = append(resp.EnvItems, &agentcomposev1.SessionEnvVar{Name: item.Name, Value: value, Secret: item.Secret})
	}
	return resp
}

func toSessionWorkspaceSnapshot(item WorkspaceConfig) *SessionWorkspace {
	return &SessionWorkspace{
		ID:         item.ID,
		Name:       item.Name,
		Type:       item.Type,
		ConfigJSON: item.ConfigJSON,
	}
}

func toProtoSessionWorkspace(item *SessionWorkspace) *agentcomposev1.SessionWorkspaceSnapshot {
	if item == nil {
		return nil
	}
	return &agentcomposev1.SessionWorkspaceSnapshot{
		Id:         item.ID,
		Name:       item.Name,
		Type:       item.Type,
		ConfigJson: item.ConfigJSON,
	}
}

func toProtoWorkspaceConfig(item WorkspaceConfig) *agentcomposev1.WorkspaceConfig {
	return &agentcomposev1.WorkspaceConfig{
		Id:         item.ID,
		Name:       item.Name,
		Type:       item.Type,
		ConfigJson: item.ConfigJSON,
		Comment:    item.Comment,
		CreatedAt:  item.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:  item.UpdatedAt.Format(time.RFC3339Nano),
	}
}

func toProtoCell(cell NotebookCell) *agentcomposev1.NotebookCell {
	return &agentcomposev1.NotebookCell{
		Id:             cell.ID,
		Source:         cell.Source,
		Stdout:         cell.Stdout,
		Stderr:         cell.Stderr,
		Output:         firstNonEmpty(cell.Output, cell.Stdout+cell.Stderr),
		Success:        cell.Success,
		CreatedAt:      cell.CreatedAt.Format(time.RFC3339Nano),
		Type:           toProtoCellType(cell.Type),
		ExitCode:       int32(cell.ExitCode),
		Agent:          cell.Agent,
		AgentSessionId: cell.AgentSessionID,
		StopReason:     cell.StopReason,
		Running:        cell.Running,
	}
}

func toProtoAgentRun(cell NotebookCell) *agentcomposev1.AgentRun {
	return &agentcomposev1.AgentRun{
		Id:             cell.ID,
		Agent:          cell.Agent,
		Message:        cell.Source,
		Output:         firstNonEmpty(cell.Output, cell.Stdout+cell.Stderr),
		ExitCode:       int32(cell.ExitCode),
		Success:        cell.Success,
		CreatedAt:      cell.CreatedAt.Format(time.RFC3339Nano),
		AgentSessionId: cell.AgentSessionID,
		StopReason:     cell.StopReason,
		Running:        cell.Running,
	}
}

func fromProtoCellType(cellType agentcomposev1.CellType) string {
	switch cellType {
	case agentcomposev1.CellType_CELL_TYPE_SHELL:
		return CellTypeShell
	case agentcomposev1.CellType_CELL_TYPE_PYTHON:
		return CellTypePython
	case agentcomposev1.CellType_CELL_TYPE_AGENT:
		return CellTypeAgent
	case agentcomposev1.CellType_CELL_TYPE_JAVASCRIPT, agentcomposev1.CellType_CELL_TYPE_UNSPECIFIED:
		return CellTypeJavaScript
	default:
		return CellTypeJavaScript
	}
}

func toProtoWatchSessionResponse(event sessionWatchEvent) *agentcomposev1.WatchSessionResponse {
	resp := &agentcomposev1.WatchSessionResponse{
		Chunk:    event.Chunk,
		IsStderr: event.IsStderr,
		CellId:   event.CellID,
	}
	switch event.EventType {
	case sessionWatchEventTypeSessionUpdated:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_SESSION_UPDATED
		if event.Session != nil {
			resp.Session = toProtoSessionSummary(event.Session)
		}
	case sessionWatchEventTypeCellStarted:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_CELL_STARTED
		if event.Cell != nil {
			resp.Cell = toProtoCell(*event.Cell)
			resp.CellId = event.Cell.ID
		}
	case sessionWatchEventTypeCellOutput:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_CELL_OUTPUT
	case sessionWatchEventTypeCellCompleted:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_CELL_COMPLETED
		if event.Cell != nil {
			resp.Cell = toProtoCell(*event.Cell)
			resp.CellId = event.Cell.ID
		}
	case sessionWatchEventTypeEventAdded:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_EVENT_ADDED
		if event.Event != nil {
			resp.Event = toProtoEvent(*event.Event)
		}
	default:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_UNSPECIFIED
	}
	return resp
}

func toProtoCellType(cellType string) agentcomposev1.CellType {
	switch cellType {
	case CellTypeShell:
		return agentcomposev1.CellType_CELL_TYPE_SHELL
	case CellTypePython:
		return agentcomposev1.CellType_CELL_TYPE_PYTHON
	case CellTypeAgent:
		return agentcomposev1.CellType_CELL_TYPE_AGENT
	case CellTypeJavaScript:
		fallthrough
	default:
		return agentcomposev1.CellType_CELL_TYPE_JAVASCRIPT
	}
}

func toProtoEvent(event SessionEvent) *agentcomposev1.SessionEvent {
	return &agentcomposev1.SessionEvent{
		Id:        event.ID,
		Type:      event.Type,
		Level:     event.Level,
		Message:   event.Message,
		CreatedAt: event.CreatedAt.Format(time.RFC3339Nano),
	}
}
