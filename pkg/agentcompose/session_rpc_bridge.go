package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

type SessionRPCBridge struct {
	config    *appconfig.Config
	store     *Store
	configDB  *ConfigStore
	driver    Driver
	runtimes  RuntimeProvider
	bus       *LoaderBus
	streams   *SessionStreamBroker
	cap       CapabilityProvider
	dashboard *DashboardOverviewHub
}

func NewSessionRPCBridge(di do.Injector) (*SessionRPCBridge, error) {
	dashboard, _ := do.Invoke[*DashboardOverviewHub](di)
	return &SessionRPCBridge{
		config:    do.MustInvoke[*appconfig.Config](di),
		store:     do.MustInvoke[*Store](di),
		configDB:  do.MustInvoke[*ConfigStore](di),
		driver:    do.MustInvoke[Driver](di),
		runtimes:  do.MustInvoke[RuntimeProvider](di),
		bus:       do.MustInvoke[*LoaderBus](di),
		streams:   do.MustInvoke[*SessionStreamBroker](di),
		cap:       do.MustInvoke[capabilityIntegration](di),
		dashboard: dashboard,
	}, nil
}

var sessionProtoJSONMarshal = protojson.MarshalOptions{}

func (b *SessionRPCBridge) CallJSON(ctx context.Context, method, requestJSON string) (string, error) {
	return b.CallJSONWithSource(ctx, method, requestJSON, SessionTypeScript)
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
	data, err := sessionProtoJSONMarshal.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("encode session rpc response: %w", err)
	}
	return string(data), nil
}

func (b *SessionRPCBridge) publishLoaderTopic(topic string, payload map[string]any) {
	if b == nil || b.bus == nil {
		return
	}
	b.bus.Publish(LoaderTopicEvent{
		Topic:     topic,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})
}

func (b *SessionRPCBridge) notifyDashboard(reason string) {
	if b == nil || b.dashboard == nil {
		return
	}
	b.dashboard.Notify(reason)
}

func (b *SessionRPCBridge) CreateSession(ctx context.Context, req *connect.Request[agentcomposev1.CreateSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return b.createSession(ctx, req, SessionTypeManual)
}

func (b *SessionRPCBridge) createSession(ctx context.Context, req *connect.Request[agentcomposev1.CreateSessionRequest], source string) (*connect.Response[agentcomposev1.SessionResponse], error) {
	tags := make([]SessionTag, 0, len(req.Msg.GetTags()))
	for _, tag := range req.Msg.GetTags() {
		tags = append(tags, SessionTag{Name: tag.GetName(), Value: tag.GetValue()})
	}
	envItems := make([]SessionEnvVar, 0, len(req.Msg.GetEnvItems()))
	for _, item := range req.Msg.GetEnvItems() {
		envItems = append(envItems, SessionEnvVar{Name: item.GetName(), Value: item.GetValue(), Secret: item.GetSecret()})
	}
	globalEnvItems, err := b.configDB.ListGlobalEnv(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	envItems = mergeEnvItems(globalEnvItems, envItems)
	capabilityVars, capabilityTags := buildCapabilityGatewaySessionVars(capabilityGatewayProxyTarget(b.cap), req.Msg.GetCapsetIds())
	envItems = mergeEnvItems(envItems, capabilityVars)
	tags = append(tags, capabilityTags...)

	var workspaceSnapshot *SessionWorkspace
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
	session, err := b.store.CreateSession(ctx, req.Msg.GetTitle(), req.Msg.GetBaseWorkspace(), driver, guestImage, workspaceID, source, workspaceSnapshot, envItems, tags)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := prepareSessionWorkspace(ctx, b.config, b.configDB, session); err != nil {
		session.Summary.VMStatus = VMStatusFailed
		_ = b.store.UpdateSession(ctx, session)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	writeCapabilityGuide(ctx, b.cap, b.store, b.streams, session, req.Msg.GetCapsetIds())
	if err := b.driver.StartSessionVM(ctx, session); err != nil {
		session.Summary.VMStatus = VMStatusFailed
		_ = b.store.UpdateSession(ctx, session)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	session.Summary.VMStatus = VMStatusRunning
	if err := b.store.UpdateSession(ctx, session); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	b.streams.PublishSessionUpdated(&session.Summary)
	b.notifyDashboard("session_updated")
	event := SessionEvent{
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
	b.publishLoaderTopic("agent-compose.session.created", sessionTopicPayload(loaded, source))
	return connect.NewResponse(&agentcomposev1.SessionResponse{Session: toProtoSessionDetail(loaded)}), nil
}

func (b *SessionRPCBridge) ResumeSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return b.resumeSession(ctx, req, SessionTypeManual)
}

func (b *SessionRPCBridge) resumeSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest], source string) (*connect.Response[agentcomposev1.SessionResponse], error) {
	session, err := b.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if err := prepareSessionWorkspace(ctx, b.config, b.configDB, session); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	writeCapabilityGuide(ctx, b.cap, b.store, b.streams, session, sessionCapabilityCapsets(session))
	if err := b.driver.StartSessionVM(ctx, session); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	session.Summary.VMStatus = VMStatusRunning
	if err := b.store.UpdateSession(ctx, session); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	b.streams.PublishSessionUpdated(&session.Summary)
	b.notifyDashboard("session_updated")
	event := SessionEvent{ID: uuid.NewString(), Type: "session.resumed", Level: "info", Message: fmt.Sprintf("session resumed with %s driver using guest image %s", session.Summary.Driver, session.Summary.GuestImage), CreatedAt: time.Now().UTC()}
	_ = b.store.AddEvent(ctx, session.Summary.ID, event)
	b.streams.PublishEventAdded(session.Summary.ID, event)
	loaded, err := b.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	b.publishLoaderTopic("agent-compose.session.resumed", sessionTopicPayload(loaded, source))
	return connect.NewResponse(&agentcomposev1.SessionResponse{Session: toProtoSessionDetail(loaded)}), nil
}

func (b *SessionRPCBridge) reconcileSessionRuntimeState(ctx context.Context, session *Session) (*Session, error) {
	if session == nil || session.Summary.VMStatus != VMStatusRunning {
		return session, nil
	}
	driver, err := driverpkg.ResolveSessionRuntimeDriver(session.Summary.Driver, b.config.RuntimeDriver)
	if err != nil {
		return nil, err
	}
	if driver != driverpkg.RuntimeDriverMicrosandbox {
		return session, nil
	}
	proxyState, err := b.store.GetProxyState(session.Summary.ID)
	if err != nil {
		return nil, err
	}
	if jupyterTargetReachable(proxyState, 250*time.Millisecond) {
		return session, nil
	}
	vmState, err := b.store.GetVMState(session.Summary.ID)
	if err != nil {
		return nil, err
	}
	runtime, err := b.runtimes.ForDriver(driver)
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
	if err := b.store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return nil, err
	}
	session.Summary.VMStatus = VMStatusStopped
	if err := b.store.UpdateSession(ctx, session); err != nil {
		return nil, err
	}
	b.streams.PublishSessionUpdated(&session.Summary)
	b.notifyDashboard("session_updated")
	event := SessionEvent{
		ID:        uuid.NewString(),
		Type:      "session.runtime_lost",
		Level:     "warn",
		Message:   "session marked stopped after microsandbox runtime became unreachable",
		CreatedAt: now,
	}
	_ = b.store.AddEvent(ctx, session.Summary.ID, event)
	b.streams.PublishEventAdded(session.Summary.ID, event)
	return b.store.GetSession(ctx, session.Summary.ID)
}

func (b *SessionRPCBridge) StopSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return b.stopSession(ctx, req, SessionTypeManual)
}

func (b *SessionRPCBridge) stopSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest], source string) (*connect.Response[agentcomposev1.SessionResponse], error) {
	session, err := b.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if reconciled, recErr := b.reconcileSessionRuntimeState(ctx, session); recErr != nil {
		slog.Warn("failed to reconcile session runtime state before stop", "session_id", session.Summary.ID, "error", recErr)
	} else {
		session = reconciled
	}
	if session.Summary.VMStatus != VMStatusRunning {
		return connect.NewResponse(&agentcomposev1.SessionResponse{Session: toProtoSessionDetail(session)}), nil
	}
	if err := b.driver.StopSessionVM(ctx, session); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	session.Summary.VMStatus = VMStatusStopped
	if err := b.store.UpdateSession(ctx, session); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	b.streams.PublishSessionUpdated(&session.Summary)
	b.notifyDashboard("session_updated")
	event := SessionEvent{ID: uuid.NewString(), Type: "session.stopped", Level: "info", Message: "session stopped", CreatedAt: time.Now().UTC()}
	_ = b.store.AddEvent(ctx, session.Summary.ID, event)
	b.streams.PublishEventAdded(session.Summary.ID, event)
	loaded, err := b.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	b.publishLoaderTopic("agent-compose.session.stopped", sessionTopicPayload(loaded, source))
	return connect.NewResponse(&agentcomposev1.SessionResponse{Session: toProtoSessionDetail(loaded)}), nil
}

func (b *SessionRPCBridge) GetSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	session, err := b.store.GetSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if reconciled, recErr := b.reconcileSessionRuntimeState(ctx, session); recErr != nil {
		slog.Warn("failed to reconcile session runtime state during get", "session_id", session.Summary.ID, "error", recErr)
	} else {
		session = reconciled
	}
	return connect.NewResponse(&agentcomposev1.SessionResponse{Session: toProtoSessionDetail(session)}), nil
}

func (b *SessionRPCBridge) ListSessions(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
	options, err := sessionListOptionsFromProto(req.Msg)
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
		if reconciled, recErr := b.reconcileSessionRuntimeState(ctx, session); recErr != nil {
			slog.Warn("failed to reconcile session runtime state during list", "session_id", session.Summary.ID, "error", recErr)
		} else {
			session = reconciled
		}
		resp.Sessions = append(resp.Sessions, toProtoSessionSummary(&session.Summary))
	}
	return connect.NewResponse(resp), nil
}

func (b *SessionRPCBridge) ensureSessionProxyReady(ctx context.Context, sessionID string) (*Session, ProxyState, error) {
	session, err := b.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, ProxyState{}, err
	}
	proxyState, err := b.store.GetProxyState(session.Summary.ID)
	if err != nil {
		return nil, ProxyState{}, err
	}
	if session.Summary.VMStatus == VMStatusRunning && jupyterTargetReachable(proxyState, 1500*time.Millisecond) {
		return session, proxyState, nil
	}
	startCtx, cancel := context.WithTimeout(ctx, b.config.SessionStartTimeout)
	defer cancel()
	if err := prepareSessionWorkspace(startCtx, b.config, b.configDB, session); err != nil {
		session.Summary.VMStatus = VMStatusFailed
		_ = b.store.UpdateSession(ctx, session)
		return nil, ProxyState{}, err
	}
	if err := b.driver.StartSessionVM(startCtx, session); err != nil {
		session.Summary.VMStatus = VMStatusFailed
		_ = b.store.UpdateSession(ctx, session)
		return nil, ProxyState{}, err
	}
	session.Summary.VMStatus = VMStatusRunning
	if err := b.store.UpdateSession(ctx, session); err != nil {
		return nil, ProxyState{}, err
	}
	loaded, err := b.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, ProxyState{}, err
	}
	proxyState, err = b.store.GetProxyState(session.Summary.ID)
	if err != nil {
		return nil, ProxyState{}, err
	}
	return loaded, proxyState, nil
}

func (b *SessionRPCBridge) GetSessionProxy(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionProxyResponse], error) {
	session, proxyState, err := b.ensureSessionProxyReady(ctx, req.Msg.GetSessionId())
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
