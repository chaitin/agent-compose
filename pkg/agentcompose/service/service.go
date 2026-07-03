package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"errors"
	"fmt"
	"log/slog"
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

	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/agentcompose/workspaces"
	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/capproxy"
	"agent-compose/pkg/dashboard"
	"agent-compose/pkg/events"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/imagecache"
	"agent-compose/pkg/images"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
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
	images     images.Backend
	ociImages  images.Backend
	autoImages images.Backend
	llm        *LLMClient
	cap        capabilities.Provider
	bus        *loaders.Bus
	streams    *sessions.StreamBroker
	dashboard  *dashboard.Hub
	events     *events.Dispatcher
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
	agentcomposev2connect.UnimplementedSandboxServiceHandler
}

func NewService(di do.Injector) (*Service, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	dashboard, _ := do.Invoke[*dashboard.Hub](di)
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
	dockerImages := images.NewDockerBackend()
	ociCache, err := imagecache.New(imagecache.Config{
		Root:               imageCacheRoot,
		DefaultRegistry:    config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		return nil, err
	}
	config.ImageCacheRoot = ociCache.Root()
	ociImages := images.NewOCIBackend(ociCache)
	autoImages := images.NewAutoBackend(config.ImageStoreMode, dockerImages, ociImages)
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
		bus:        do.MustInvoke[*loaders.Bus](di),
		streams:    do.MustInvoke[*sessions.StreamBroker](di),
		dashboard:  dashboard,
		events:     NewEventDispatcher(do.MustInvoke[context.Context](di), do.MustInvoke[*ConfigStore](di), do.MustInvoke[*loaders.Bus](di)),
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
	do.Provide(di, loaders.NewBus)
	do.Provide(di, NewSessionStreamBroker)
	do.Provide(di, NewDashboardOverviewAggregator)
	do.Provide(di, NewDashboardOverviewHub)
	do.Provide(di, loaders.NewLoaderEngine)
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
	path, handler = agentcomposev2connect.NewSandboxServiceHandler(service)
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
		Session:   api.SessionSummaryToProto(&session.Summary),
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
			if sendErr := stream.Send(api.WatchSessionResponseToProto(event)); sendErr != nil {
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
	return connect.NewResponse(api.GlobalEnvConfigToProto(items)), nil
}

func (s *Service) UpdateGlobalEnvConfig(ctx context.Context, req *connect.Request[agentcomposev1.UpdateGlobalEnvConfigRequest]) (*connect.Response[agentcomposev1.GlobalEnvConfigResponse], error) {
	items := make([]SessionEnvVar, 0, len(req.Msg.GetEnvItems()))
	for _, item := range req.Msg.GetEnvItems() {
		items = append(items, SessionEnvVar{Name: item.GetName(), Value: item.GetValue(), Secret: item.GetSecret()})
	}
	items = domain.NormalizeEnvItems(items)
	items, err := s.preserveUnchangedGlobalEnvSecrets(ctx, items)
	if err != nil {
		return nil, err
	}
	saved, err := s.configDB.ReplaceGlobalEnv(ctx, items)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(api.GlobalEnvConfigToProto(saved)), nil
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
		resp.Workspaces = append(resp.Workspaces, api.WorkspaceConfigToProto(item))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) CreateWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.CreateWorkspaceConfigRequest]) (*connect.Response[agentcomposev1.WorkspaceConfigResponse], error) {
	configJSON := strings.TrimSpace(req.Msg.GetConfigJson())
	workspaceType := strings.ToLower(strings.TrimSpace(req.Msg.GetType()))
	workspaceID := ""
	if workspaceType == "file" {
		workspaceID = uuid.NewString()
		configJSON = workspaces.DefaultFileConfigJSON(s.config, workspaceID)
		if _, err := workspaces.ValidateFileWorkspaceConfig(s.config, workspaceID, configJSON); err != nil {
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
	return connect.NewResponse(&agentcomposev1.WorkspaceConfigResponse{Workspace: api.WorkspaceConfigToProto(item)}), nil
}

func (s *Service) UpdateWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.UpdateWorkspaceConfigRequest]) (*connect.Response[agentcomposev1.WorkspaceConfigResponse], error) {
	configJSON := strings.TrimSpace(req.Msg.GetConfigJson())
	workspaceType := strings.ToLower(strings.TrimSpace(req.Msg.GetType()))
	previous, err := s.configDB.GetWorkspaceConfig(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if workspaceType == "file" {
		configJSON = workspaces.DefaultFileConfigJSON(s.config, req.Msg.GetWorkspaceId())
		if _, err := workspaces.ValidateFileWorkspaceConfig(s.config, req.Msg.GetWorkspaceId(), configJSON); err != nil {
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
	return connect.NewResponse(&agentcomposev1.WorkspaceConfigResponse{Workspace: api.WorkspaceConfigToProto(item)}), nil
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
		if errors.Is(err, ErrReferenced) {
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
	content, err := workspaces.OpenFileWorkspaceContent(s.config, WorkspaceConfig{
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
	relRoot, err := workspaces.FileWorkspaceContentRelRoot(workspaceID)
	if err != nil {
		return err
	}
	dataRoot, err := workspaces.OpenFileWorkspaceDataRoot(s.config)
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
	dataRoot, err := workspaces.OpenFileWorkspaceDataRoot(s.config)
	if err != nil {
		return nil, "", err
	}
	relRoot, err := workspaces.FileWorkspaceContentRelRoot(workspace.ID)
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
	return s.sessionLifecycle(nil).ReconcileRuntimeState(ctx, session)
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
	return sessions.JupyterTargetReachable(proxyState, timeout)
}

func (s *Service) ensureSessionProxyReady(ctx context.Context, sessionID string) (*Session, ProxyState, error) {
	return s.sessionLifecycle(nil).EnsureProxyReady(ctx, sessionID)
}

func (s *Service) GetSessionProxy(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionProxyResponse], error) {
	return s.sessions.GetSessionProxy(ctx, req)
}

func (s *Service) ExecuteCell(ctx context.Context, req *connect.Request[agentcomposev1.ExecuteCellRequest]) (*connect.Response[agentcomposev1.ExecuteCellResponse], error) {
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is not running"))
	}
	cell, err := s.executor.ExecuteCell(ctx, session, api.CellTypeFromProto(req.Msg.GetType()), req.Msg.GetSource())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	loaded, err := s.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.publishLoaderTopic("agent-compose.cell.completed", loaders.CellTopicPayload(session.Summary.ID, cell, "api"))
	return connect.NewResponse(&agentcomposev1.ExecuteCellResponse{Session: api.SessionSummaryToProto(&loaded.Summary), Cell: api.CellToProto(cell)}), nil
}

func (s *Service) ExecuteCellStream(ctx context.Context, req *connect.Request[agentcomposev1.ExecuteCellRequest], stream *connect.ServerStream[agentcomposev1.ExecuteCellStreamResponse]) error {
	prepareStreamingHeaders(stream.ResponseHeader())
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is not running"))
	}

	streamErr := func(sendErr error) error {
		if sendErr == nil {
			return nil
		}
		return connect.NewError(connect.CodeUnknown, sendErr)
	}
	cell, err := s.executor.ExecuteCellStream(ctx, session, api.CellTypeFromProto(req.Msg.GetType()), req.Msg.GetSource(), execution.CellExecutionStream{
		OnStart: func(cell NotebookCell) error {
			return streamErr(stream.Send(&agentcomposev1.ExecuteCellStreamResponse{
				EventType: agentcomposev1.ExecuteCellStreamEventType_EXECUTE_CELL_STREAM_EVENT_TYPE_STARTED,
				Session:   api.SessionSummaryToProto(&session.Summary),
				Cell:      api.CellToProto(cell),
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
	s.publishLoaderTopic("agent-compose.cell.completed", loaders.CellTopicPayload(session.Summary.ID, cell, "api"))
	return streamErr(stream.Send(&agentcomposev1.ExecuteCellStreamResponse{
		EventType: agentcomposev1.ExecuteCellStreamEventType_EXECUTE_CELL_STREAM_EVENT_TYPE_COMPLETED,
		Session:   api.SessionSummaryToProto(&loaded.Summary),
		Cell:      api.CellToProto(cell),
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
		resp.Cells = append(resp.Cells, api.CellToProto(cell))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) SendAgentMessage(ctx context.Context, req *connect.Request[agentcomposev1.SendAgentMessageRequest]) (*connect.Response[agentcomposev1.SendAgentMessageResponse], error) {
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is not running"))
	}
	message := strings.TrimSpace(req.Msg.GetMessage())
	if message == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("message is required"))
	}
	agentConfig := s.resolveSessionAgentConfig(ctx, session, req.Msg.GetAgent())
	cell, userEvent, assistantEvent, err := s.executor.ExecuteAgentRequest(ctx, session, execution.ExecuteAgentRequest{
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
	s.publishLoaderTopic("agent-compose.agent.completed", loaders.CellTopicPayload(session.Summary.ID, cell, "api"))
	return connect.NewResponse(&agentcomposev1.SendAgentMessageResponse{UserEvent: api.SessionEventToProto(userEvent), AssistantEvent: api.SessionEventToProto(assistantEvent)}), nil
}

func (s *Service) SendAgentMessageStream(ctx context.Context, req *connect.Request[agentcomposev1.SendAgentMessageRequest], stream *connect.ServerStream[agentcomposev1.SendAgentMessageStreamResponse]) error {
	prepareStreamingHeaders(stream.ResponseHeader())
	session, err := s.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if session.Summary.VMStatus != domain.VMStatusRunning {
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

	cell, userEvent, assistantEvent, err := s.executor.ExecuteAgentRequest(ctx, session, execution.ExecuteAgentRequest{
		Agent:             agentConfig.Provider,
		AgentDefinitionID: agentConfig.AgentDefinitionID,
		Model:             agentConfig.Model,
		ProviderEnvItems:  agentConfig.EnvItems,
		Message:           message,
		Stream: execution.AgentExecutionStream{
			OnStart: func(cell NotebookCell) error {
				return streamErr(stream.Send(&agentcomposev1.SendAgentMessageStreamResponse{
					EventType: agentcomposev1.SendAgentMessageStreamEventType_SEND_AGENT_MESSAGE_STREAM_EVENT_TYPE_STARTED,
					Session:   api.SessionSummaryToProto(&session.Summary),
					Run:       api.AgentRunToProto(cell),
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
	s.publishLoaderTopic("agent-compose.agent.completed", loaders.CellTopicPayload(session.Summary.ID, cell, "api"))
	return streamErr(stream.Send(&agentcomposev1.SendAgentMessageStreamResponse{
		EventType:      agentcomposev1.SendAgentMessageStreamEventType_SEND_AGENT_MESSAGE_STREAM_EVENT_TYPE_COMPLETED,
		Session:        api.SessionSummaryToProto(&loaded.Summary),
		Run:            api.AgentRunToProto(cell),
		RunId:          cell.ID,
		UserEvent:      api.SessionEventToProto(userEvent),
		AssistantEvent: api.SessionEventToProto(assistantEvent),
	}))
}

type agentExecutionConfig = execution.AgentConfig

func (s *Service) resolveSessionAgentConfig(ctx context.Context, session *Session, requested string) agentExecutionConfig {
	provider := domain.NormalizeAgentKind(requested)
	config := agentExecutionConfig{Provider: provider}
	if session == nil {
		return config
	}
	agentID := sessionTagValue(session.Summary.Tags, domain.AgentSessionTagID)
	if agentID == "" || !domain.SessionHasAgentTag(session, agentID) || s.configDB == nil {
		return config
	}
	agent, err := s.configDB.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return config
	}
	return agentExecutionConfigFromDefinition(agent, provider)
}

func agentExecutionConfigFromDefinition(agent domain.AgentDefinition, fallbackProvider string) agentExecutionConfig {
	return execution.AgentConfigFromDefinition(agent, fallbackProvider)
}

func applyAgentProviderEnv(session *Session, agentEnv []SessionEnvVar) {
	execution.ApplyAgentProviderEnv(session, agentEnv)
}

func sessionTagValue(tags []SessionTag, name string) string {
	return execution.SessionTagValue(tags, name)
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
		resp.Events = append(resp.Events, api.SessionEventToProto(event))
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

type agentExecResponse struct {
	Provider   string `json:"provider"`
	SessionID  string `json:"sessionId"`
	StopReason string `json:"stopReason"`
	FinalText  string `json:"finalText"`
	JSON       any    `json:"json"`
	Transcript string `json:"transcript"`
	Stderr     string `json:"stderr"`
}

const agentResultPrefix = execution.AgentResultPrefix
const commandResultPrefix = execution.CommandResultPrefix

type runtimeCommandRequestJSON = execution.RuntimeCommandRequest

const agentSystemPromptFileName = execution.AgentSystemPromptFileName // keep in sync with runtime/javascript/src/system-context.ts

// hostAgentSystemPromptPath is the session agent identity file the host writes and the
// guest reads via convention from --state-root (guest /data/state/agents/system-prompts/system-prompt.txt).
// Returns "" when the session workspace path is unknown.
func hostAgentSystemPromptPath(session *Session) string {
	return execution.HostAgentSystemPromptPath(session)
}

func writeAgentPromptFile(config *appconfig.Config, session *Session, agent, message string) (string, error) {
	return execution.WriteAgentPromptFile(config, session, agent, message)
}

// writeAgentSystemPromptFile materializes agent identity for the guest runtime at a
// fixed convention path under the session state tree:
//
//	state/agents/system-prompts/system-prompt.txt
//
// The guest discovers this file from --state-root (no CLI flag). When systemPrompt is
// empty, the file is removed so later runs in the same session cannot read stale identity.
func writeAgentSystemPromptFile(session *Session, systemPrompt string) error {
	return execution.WriteAgentSystemPromptFile(session, systemPrompt)
}

func writeAgentOutputSchemaFile(config *appconfig.Config, session *Session, agent, schemaJSON string) (string, error) {
	return execution.WriteAgentOutputSchemaFile(config, session, agent, schemaJSON)
}

func agentTraceEvents(transcript string, createdAt time.Time) []SessionEvent {
	return execution.AgentTraceEvents(transcript, createdAt)
}

func runtimeCommandRequestPayload(config *appconfig.Config, request domain.LoaderCommandRequest, guestCellDir string) runtimeCommandRequestJSON {
	return execution.RuntimeCommandRequestPayload(config, request, guestCellDir)
}

func buildLoaderCommandExecSpec(config *appconfig.Config, session *Session, guestRequestPath string) ExecSpec {
	commandHome := guestSessionHome(config)
	return execution.BuildLoaderCommandExecSpec(config, session, guestRequestPath, commandHome)
}

func buildSessionExecEnv(config *appconfig.Config, session *Session, home string) map[string]string {
	return execution.BuildSessionExecEnv(config, session, home)
}

func mirrorRuntimeCommandArtifacts(hostCellDir string, result RuntimeCommandResult) error {
	return execution.MirrorRuntimeCommandArtifacts(hostCellDir, result)
}

func (e *Executor) resolveAgentSystemPrompt(ctx context.Context, session *Session, agentDefinitionID string) (string, error) {
	if e == nil || e.configDB == nil {
		return "", nil
	}
	agentID := strings.TrimSpace(agentDefinitionID)
	if agentID == "" {
		taggedAgentID := sessionTagValue(session.Summary.Tags, domain.AgentSessionTagID)
		if !domain.SessionHasAgentTag(session, taggedAgentID) {
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
	if session.Summary.VMStatus != domain.VMStatusRunning {
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
		spec.Env = llms.MergeManagedExecEnv(spec.Env, managedEnv)
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
		return execution.SanitizeAgentExecResult(result), AgentRunResult{}, err
	}
	parsed, err := execution.ParseAgentExecResult(agent, result)
	if err != nil {
		return execution.SanitizeAgentExecResult(result), AgentRunResult{}, err
	}
	return execution.SanitizeAgentExecResult(result), parsed, nil
}

func buildAgentExecSpec(config *appconfig.Config, session *Session, agent, model, promptPath, schemaPath string) ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	agentHome := guestSessionHome(config)
	env := buildSessionExecEnv(config, session, agentHome)

	promptCommand := "agent-compose-runtime prompt" +
		" --provider " + execution.ShellQuote(agent) +
		" --message-file " + execution.ShellQuote(promptPath) +
		" --state-root " + execution.ShellQuote(config.GuestStateRoot) +
		" --workspace " + execution.ShellQuote(config.GuestWorkspacePath) +
		" --home " + execution.ShellQuote(agentHome)
	if strings.TrimSpace(model) != "" {
		promptCommand += " --model " + execution.ShellQuote(strings.TrimSpace(model))
	}
	if strings.TrimSpace(schemaPath) != "" {
		promptCommand += " --output-schema-file " + execution.ShellQuote(schemaPath)
	}
	command := strings.Join([]string{
		"set -e",
		"cd " + execution.ShellQuote(config.GuestWorkspacePath),
		"mkdir -p " + execution.ShellQuote(agentHome),
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

func toSessionWorkspaceSnapshot(item WorkspaceConfig) *SessionWorkspace {
	return &SessionWorkspace{
		ID:         item.ID,
		Name:       item.Name,
		Type:       item.Type,
		ConfigJSON: item.ConfigJSON,
	}
}
