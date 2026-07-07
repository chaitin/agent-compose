package adapters

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/dashboard"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/volumes"
	"agent-compose/pkg/workspaces"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

type SessionRPCBridge struct {
	config    *appconfig.Config
	store     *sessionstore.Store
	configDB  *configstore.ConfigStore
	driver    sessions.SessionDriver
	runtimes  RuntimeProvider
	bus       *loaders.Bus
	streams   *sessions.StreamBroker
	cap       capabilities.Provider
	volumes   LoaderVolumeResolver
	dashboard *dashboard.Hub
}

func NewSessionRPCBridge(config *appconfig.Config, store *sessionstore.Store, configDB *configstore.ConfigStore, driver sessions.SessionDriver, runtimes RuntimeProvider, bus *loaders.Bus, streams *sessions.StreamBroker, cap capabilities.Provider, volumeResolver LoaderVolumeResolver, dashboard *dashboard.Hub) *SessionRPCBridge {
	return &SessionRPCBridge{
		config:    config,
		store:     store,
		configDB:  configDB,
		driver:    driver,
		runtimes:  runtimes,
		bus:       bus,
		streams:   streams,
		cap:       cap,
		volumes:   volumeResolver,
		dashboard: dashboard,
	}
}

func (b *SessionRPCBridge) CallJSON(ctx context.Context, method, requestJSON string) (string, error) {
	return b.CallJSONWithSource(ctx, method, requestJSON, domain.SessionTypeScript)
}

func (b *SessionRPCBridge) CallJSONWithSource(ctx context.Context, method, requestJSON, source string) (string, error) {
	method = strings.TrimSpace(method)
	switch method {
	case "CreateSession":
		var req agentcomposev1.CreateSessionRequest
		if err := unmarshalSessionProtoJSON(requestJSON, &req); err != nil {
			return "", err
		}
		resp, err := b.createSession(ctx, connect.NewRequest(&req), source)
		if err != nil {
			return "", err
		}
		return marshalSessionProtoJSON(resp.Msg)
	case "ResumeSession":
		var req agentcomposev1.SessionIDRequest
		if err := unmarshalSessionProtoJSON(requestJSON, &req); err != nil {
			return "", err
		}
		resp, err := b.resumeSession(ctx, connect.NewRequest(&req), source)
		if err != nil {
			return "", err
		}
		return marshalSessionProtoJSON(resp.Msg)
	case "StopSession":
		var req agentcomposev1.SessionIDRequest
		if err := unmarshalSessionProtoJSON(requestJSON, &req); err != nil {
			return "", err
		}
		resp, err := b.stopSession(ctx, connect.NewRequest(&req), source)
		if err != nil {
			return "", err
		}
		return marshalSessionProtoJSON(resp.Msg)
	case "GetSession":
		var req agentcomposev1.SessionIDRequest
		if err := unmarshalSessionProtoJSON(requestJSON, &req); err != nil {
			return "", err
		}
		resp, err := b.GetSession(ctx, connect.NewRequest(&req))
		if err != nil {
			return "", err
		}
		return marshalSessionProtoJSON(resp.Msg)
	case "ListSessions":
		var req agentcomposev1.ListSessionsRequest
		if err := unmarshalSessionProtoJSON(requestJSON, &req); err != nil {
			return "", err
		}
		resp, err := b.ListSessions(ctx, connect.NewRequest(&req))
		if err != nil {
			return "", err
		}
		return marshalSessionProtoJSON(resp.Msg)
	case "GetSessionProxy":
		var req agentcomposev1.SessionIDRequest
		if err := unmarshalSessionProtoJSON(requestJSON, &req); err != nil {
			return "", err
		}
		resp, err := b.GetSessionProxy(ctx, connect.NewRequest(&req))
		if err != nil {
			return "", err
		}
		return marshalSessionProtoJSON(resp.Msg)
	default:
		return "", fmt.Errorf("unsupported session rpc %q", method)
	}
}

func unmarshalSessionProtoJSON(raw string, msg proto.Message) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if err := protojson.Unmarshal([]byte(raw), msg); err != nil {
		return fmt.Errorf("decode session rpc request: %w", err)
	}
	return nil
}

func marshalSessionProtoJSON(msg proto.Message) (string, error) {
	if msg == nil {
		return "", nil
	}
	data, err := protojson.MarshalOptions{}.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("encode session rpc response: %w", err)
	}
	return string(data), nil
}

func (b *SessionRPCBridge) publishLoaderTopic(topic string, payload map[string]any) {
	if b == nil || b.bus == nil {
		return
	}
	b.bus.Publish(domain.LoaderTopicEvent{
		Topic:     topic,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})
}

func (b *SessionRPCBridge) CreateSession(ctx context.Context, req *connect.Request[agentcomposev1.CreateSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return b.createSession(ctx, req, domain.SessionTypeManual)
}

func (b *SessionRPCBridge) createSession(ctx context.Context, req *connect.Request[agentcomposev1.CreateSessionRequest], source string) (*connect.Response[agentcomposev1.SessionResponse], error) {
	tags := make([]domain.SessionTag, 0, len(req.Msg.GetTags()))
	for _, tag := range req.Msg.GetTags() {
		tags = append(tags, domain.SessionTag{Name: tag.GetName(), Value: tag.GetValue()})
	}
	envItems := make([]domain.SessionEnvVar, 0, len(req.Msg.GetEnvItems()))
	for _, item := range req.Msg.GetEnvItems() {
		envItems = append(envItems, domain.SessionEnvVar{Name: item.GetName(), Value: item.GetValue(), Secret: item.GetSecret()})
	}
	globalEnvItems, err := b.configDB.ListGlobalEnv(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	envItems = domain.MergeEnvItems(globalEnvItems, envItems)
	providerEnvItems := envItems
	envItems = llms.FilterPersistedRuntimeEnv(envItems)
	capabilityVars, capabilityTags := capabilities.BuildGatewaySessionVars(capabilities.ProxyTarget(b.cap), req.Msg.GetCapsetIds())
	envItems = domain.MergeEnvItems(envItems, capabilityVars)
	tags = append(tags, capabilityTags...)

	var workspaceSnapshot *domain.SessionWorkspace
	workspaceID := strings.TrimSpace(req.Msg.GetWorkspaceId())
	if workspaceID != "" {
		workspaceConfig, err := b.configDB.GetWorkspaceConfig(ctx, workspaceID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		workspaceSnapshot = toSessionWorkspaceSnapshot(workspaceConfig)
	}

	driver, err := driverpkg.ResolveSessionRuntimeDriver(req.Msg.GetDriver(), b.config.RuntimeDriver)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	guestImage := driverpkg.ResolveSessionGuestImage(req.Msg.GetGuestImage(), driverpkg.DefaultGuestImageForDriver(b.config, driver))
	volumeMounts, volumeWarnings, err := b.resolveVolumeMounts(ctx, req.Msg.GetVolumes())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	session, err := b.store.CreateSessionWithOptions(ctx, req.Msg.GetTitle(), req.Msg.GetBaseWorkspace(), driver, guestImage, workspaceID, source, workspaceSnapshot, envItems, tags, sessionstore.CreateSessionOptions{
		VolumeMounts: volumeMounts,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	session.ProviderEnvItems = providerEnvItems
	b.recordVolumeWarnings(ctx, session.Summary.ID, volumeWarnings)
	if err := workspaces.PrepareSessionWorkspace(ctx, b.config, b.configDB, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = b.store.UpdateSession(ctx, session)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	writeCapabilityGuide(ctx, b.cap, b.store, b.streams, session, req.Msg.GetCapsetIds())
	if err := b.driver.StartSessionVM(ctx, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = b.store.UpdateSession(ctx, session)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := b.store.UpdateSession(ctx, session); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	b.streams.PublishSessionUpdated(&session.Summary)
	if b.dashboard != nil {
		b.dashboard.Notify("session_updated")
	}
	event := domain.SessionEvent{
		ID:        uuid.NewString(),
		Type:      "session.created",
		Level:     "info",
		Message:   fmt.Sprintf("session started with %s driver using guest image %s", session.Summary.Driver, session.Summary.GuestImage),
		CreatedAt: time.Now().UTC(),
	}
	_ = b.store.AddEvent(ctx, session.Summary.ID, event)
	b.streams.PublishEventAdded(session.Summary.ID, event)
	loaded, err := b.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	domain.RestoreSessionTransientFields(loaded, session)
	b.publishLoaderTopic("agent-compose.session.created", loaders.SessionTopicPayload(loaded, source))
	return connect.NewResponse(&agentcomposev1.SessionResponse{Session: api.SessionDetailToProto(loaded)}), nil
}

func (b *SessionRPCBridge) resolveVolumeMounts(ctx context.Context, items []*agentcomposev1.VolumeMountSpec) ([]domain.SessionVolumeMount, []string, error) {
	specs := api.VolumeMountSpecsFromProto(items)
	if len(specs) == 0 {
		return nil, nil, nil
	}
	if b.volumes == nil {
		return nil, nil, fmt.Errorf("volume resolver is required")
	}
	return b.volumes.ResolveMounts(ctx, specs, volumes.ResolveOptions{})
}

func (b *SessionRPCBridge) recordVolumeWarnings(ctx context.Context, sessionID string, warnings []string) {
	if b == nil || b.store == nil || len(warnings) == 0 {
		return
	}
	for _, warning := range warnings {
		event := domain.SessionEvent{ID: uuid.NewString(), Type: "session.volume.warning", Level: "warn", Message: warning, CreatedAt: time.Now().UTC()}
		_ = b.store.AddEvent(ctx, sessionID, event)
		if b.streams != nil {
			b.streams.PublishEventAdded(sessionID, event)
		}
	}
}

func (b *SessionRPCBridge) ResumeSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return b.resumeSession(ctx, req, domain.SessionTypeManual)
}

func (b *SessionRPCBridge) resumeSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest], source string) (*connect.Response[agentcomposev1.SessionResponse], error) {
	session, err := b.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	loaded, err := b.sessionLifecycle().ResumeLoaded(ctx, session, capabilities.SessionCapsets(session))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	b.publishLoaderTopic("agent-compose.session.resumed", loaders.SessionTopicPayload(loaded, source))
	return connect.NewResponse(&agentcomposev1.SessionResponse{Session: api.SessionDetailToProto(loaded)}), nil
}

func (b *SessionRPCBridge) ReconcileRuntimeState(ctx context.Context, session *domain.Session) (*domain.Session, error) {
	return b.sessionLifecycle().ReconcileRuntimeState(ctx, session)
}

func (b *SessionRPCBridge) StopSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return b.stopSession(ctx, req, domain.SessionTypeManual)
}

func (b *SessionRPCBridge) stopSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest], source string) (*connect.Response[agentcomposev1.SessionResponse], error) {
	session, err := b.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if reconciled, recErr := b.ReconcileRuntimeState(ctx, session); recErr != nil {
		slog.Warn("failed to reconcile session runtime state before stop", "session_id", session.Summary.ID, "error", recErr)
	} else {
		session = reconciled
	}
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return connect.NewResponse(&agentcomposev1.SessionResponse{Session: api.SessionDetailToProto(session)}), nil
	}
	loaded, stopped, err := b.sessionLifecycle().StopLoaded(ctx, session)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if stopped {
		b.publishLoaderTopic("agent-compose.session.stopped", loaders.SessionTopicPayload(loaded, source))
	}
	return connect.NewResponse(&agentcomposev1.SessionResponse{Session: api.SessionDetailToProto(loaded)}), nil
}

func (b *SessionRPCBridge) GetSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	session, err := b.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if reconciled, recErr := b.ReconcileRuntimeState(ctx, session); recErr != nil {
		slog.Warn("failed to reconcile session runtime state during get", "session_id", session.Summary.ID, "error", recErr)
	} else {
		session = reconciled
	}
	return connect.NewResponse(&agentcomposev1.SessionResponse{Session: api.SessionDetailToProto(session)}), nil
}

func (b *SessionRPCBridge) ListSessions(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
	options, err := api.SessionListOptionsFromProto(req.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	result, err := b.store.ListSessions(ctx, options)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListSessionsResponse{
		TotalCount: uint32(result.TotalCount),
		HasMore:    result.HasMore,
		NextOffset: uint32(result.NextOffset),
	}
	for _, session := range result.Sessions {
		if reconciled, recErr := b.ReconcileRuntimeState(ctx, session); recErr != nil {
			slog.Warn("failed to reconcile session runtime state during list", "session_id", session.Summary.ID, "error", recErr)
		} else {
			session = reconciled
		}
		resp.Sessions = append(resp.Sessions, api.SessionSummaryToProto(&session.Summary))
	}
	return connect.NewResponse(resp), nil
}

func (b *SessionRPCBridge) EnsureSessionProxyReady(ctx context.Context, sessionID string) (domain.ProxyState, error) {
	_, proxyState, err := b.sessionLifecycle().EnsureProxyReady(ctx, sessionID)
	return proxyState, err
}

func (b *SessionRPCBridge) GetSessionProxy(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionProxyResponse], error) {
	session, proxyState, err := b.sessionLifecycle().EnsureProxyReady(ctx, req.Msg.GetSessionId())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	notebookURL := session.Summary.ProxyPath
	if proxyState.Token != "" {
		notebookURL += "?token=" + url.QueryEscape(proxyState.Token)
	}
	return connect.NewResponse(&agentcomposev1.SessionProxyResponse{
		SessionId:   session.Summary.ID,
		ProxyPath:   session.Summary.ProxyPath,
		NotebookUrl: notebookURL,
		Driver:      session.Summary.Driver,
		VmStatus:    session.Summary.VMStatus,
	}), nil
}

func (b *SessionRPCBridge) sessionLifecycle() sessions.Lifecycle {
	return sessions.Lifecycle{
		Config:       b.config,
		Store:        b.store,
		Workspace:    b.configDB,
		Driver:       b.driver,
		Liveness:     sessionRuntimeLiveness{runtimes: b.runtimes},
		TokenRevoker: b.configDB,
		Notifier: sessionLifecycleNotifier{
			streams:   b.streams,
			dashboard: b.dashboard,
		},
		GuideWriter: func(ctx context.Context, session *domain.Session, capsetIDs []string) {
			writeCapabilityGuide(ctx, b.cap, b.store, b.streams, session, capsetIDs)
		},
	}
}

type sessionRuntimeLiveness struct {
	runtimes RuntimeProvider
}

func (p sessionRuntimeLiveness) IsSessionAlive(ctx context.Context, driver string, session *domain.Session, vmState domain.VMState) (bool, bool, error) {
	if p.runtimes == nil {
		return false, false, nil
	}
	runtime, err := p.runtimes.ForDriver(driver)
	if err != nil {
		return false, false, err
	}
	aliveRuntime, ok := runtime.(interface {
		IsSessionAlive(context.Context, *domain.Session, domain.VMState) (bool, error)
	})
	if !ok {
		return false, false, nil
	}
	alive, err := aliveRuntime.IsSessionAlive(ctx, session, vmState)
	return alive, true, err
}

type sessionLifecycleNotifier struct {
	streams   *sessions.StreamBroker
	dashboard *dashboard.Hub
}

func (n sessionLifecycleNotifier) PublishSessionUpdated(summary *domain.SessionSummary) {
	if n.streams != nil {
		n.streams.PublishSessionUpdated(summary)
	}
}

func (n sessionLifecycleNotifier) PublishEventAdded(sessionID string, event domain.SessionEvent) {
	if n.streams != nil {
		n.streams.PublishEventAdded(sessionID, event)
	}
}

func (n sessionLifecycleNotifier) NotifyDashboard(reason string) {
	if n.dashboard != nil {
		n.dashboard.Notify(reason)
	}
}

func writeCapabilityGuide(ctx context.Context, provider capabilities.Provider, store *sessionstore.Store, streams *sessions.StreamBroker, session *domain.Session, capsetIDs []string) {
	ids := capabilities.NormalizeCapsetIDs(capsetIDs)
	if len(ids) == 0 || provider == nil || session == nil {
		return
	}
	catalogPath := capabilities.SessionGuidePath(session)
	if catalogPath == "" {
		return
	}
	var b strings.Builder
	rendered := false
	for _, id := range ids {
		guide, err := provider.CapabilityGuide(ctx, id)
		if err != nil {
			slog.Warn("capability guide render skipped", "capset", id, "session_id", session.Summary.ID, "error", err)
			recordCapabilityGuideWarning(ctx, store, streams, session.Summary.ID, fmt.Sprintf("capability guide render skipped for capset %s", id))
			continue
		}
		if rendered {
			b.WriteString("\n\n")
		}
		b.Write(guide)
		rendered = true
	}
	if !rendered {
		return
	}
	content := b.String()
	if preamble := capabilities.GuidePreamble(capabilities.ProxyTarget(provider)); preamble != "" {
		content = preamble + content
	}
	if err := os.MkdirAll(filepath.Dir(catalogPath), 0o755); err != nil {
		slog.Warn("capability guide dir create failed", "session_id", session.Summary.ID, "error", err)
		recordCapabilityGuideWarning(ctx, store, streams, session.Summary.ID, "capability guide directory create failed")
		return
	}
	if err := os.WriteFile(catalogPath, []byte(content), 0o644); err != nil {
		slog.Warn("capability guide write failed", "session_id", session.Summary.ID, "error", err)
		recordCapabilityGuideWarning(ctx, store, streams, session.Summary.ID, "capability guide write failed")
	}
}

func recordCapabilityGuideWarning(ctx context.Context, store *sessionstore.Store, streams *sessions.StreamBroker, sessionID, message string) {
	if store == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	event := domain.SessionEvent{
		ID:        uuid.NewString(),
		Type:      "capability.guide.warning",
		Level:     "warning",
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.AddEvent(ctx, sessionID, event); err != nil {
		slog.Warn("capability guide warning event failed", "session_id", sessionID, "error", err)
		return
	}
	if streams != nil {
		streams.PublishEventAdded(sessionID, event)
	}
}

func toSessionWorkspaceSnapshot(item domain.WorkspaceConfig) *domain.SessionWorkspace {
	return &domain.SessionWorkspace{
		ID:         item.ID,
		Name:       item.Name,
		Type:       item.Type,
		ConfigJSON: item.ConfigJSON,
	}
}
