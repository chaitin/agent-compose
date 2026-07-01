package agentcompose

import (
	"context"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/sessions"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

type sessionWatchEventType int

type sessionWatchEvent struct {
	SessionID string
	EventType sessionWatchEventType
	Session   *SessionSummary
	Cell      *NotebookCell
	Event     *SessionEvent
	CellID    string
	Chunk     string
	IsStderr  bool
}

type SessionStreamBroker struct {
	mu          sync.RWMutex
	nextID      int
	subscribers map[string]map[int]chan sessionWatchEvent
	component   *sessions.SessionStreamBroker
}

func NewSessionStreamBroker(di do.Injector) (*SessionStreamBroker, error) {
	_ = di
	return &SessionStreamBroker{subscribers: map[string]map[int]chan sessionWatchEvent{}}, nil
}

func (b *SessionStreamBroker) componentBroker() *sessions.SessionStreamBroker {
	if b == nil {
		broker, _ := sessions.NewSessionStreamBroker(nil)
		return broker
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.component == nil {
		b.component, _ = sessions.NewSessionStreamBroker(nil)
	}
	return b.component
}

func (b *SessionStreamBroker) Subscribe(sessionID string) (<-chan sessionWatchEvent, func()) {
	sessionID = strings.TrimSpace(sessionID)
	ch := make(chan sessionWatchEvent, 256)
	if b == nil || sessionID == "" {
		close(ch)
		return ch, func() {}
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	if b.subscribers == nil {
		b.subscribers = map[string]map[int]chan sessionWatchEvent{}
	}
	if b.subscribers[sessionID] == nil {
		b.subscribers[sessionID] = map[int]chan sessionWatchEvent{}
	}
	b.subscribers[sessionID][id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		items := b.subscribers[sessionID]
		if items == nil {
			return
		}
		item, ok := items[id]
		if !ok {
			return
		}
		delete(items, id)
		close(item)
		if len(items) == 0 {
			delete(b.subscribers, sessionID)
		}
	}
}

func (b *SessionStreamBroker) PublishSessionUpdated(summary *SessionSummary) {
	b.componentBroker().PublishSessionUpdated(summary)
}

func (b *SessionStreamBroker) PublishCellStarted(sessionID string, cell NotebookCell) {
	b.componentBroker().PublishCellStarted(sessionID, cell)
}

func (b *SessionStreamBroker) PublishCellOutput(sessionID, cellID, chunk string, isStderr bool) {
	b.componentBroker().PublishCellOutput(sessionID, cellID, chunk, isStderr)
}

func (b *SessionStreamBroker) PublishCellCompleted(sessionID string, cell NotebookCell) {
	b.componentBroker().PublishCellCompleted(sessionID, cell)
}

func (b *SessionStreamBroker) PublishEventAdded(sessionID string, event SessionEvent) {
	b.componentBroker().PublishEventAdded(sessionID, event)
}

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
	component *sessions.SessionRPCBridge
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

func (b *SessionRPCBridge) componentBridge() *sessions.SessionRPCBridge {
	if b == nil {
		return nil
	}
	if b.component == nil {
		var streams *sessions.SessionStreamBroker
		if b.streams != nil {
			streams = b.streams.componentBroker()
		} else {
			streams, _ = sessions.NewSessionStreamBroker(nil)
		}
		b.component = sessions.NewSessionRPCBridgeFromDeps(b.config, b.store, b.configDB, b.driver, b.runtimes, b.bus, streams, b.cap, b.dashboard)
	}
	return b.component
}

func (b *SessionRPCBridge) CallJSON(ctx context.Context, method, requestJSON string) (string, error) {
	return b.componentBridge().CallJSON(ctx, method, requestJSON)
}

func (b *SessionRPCBridge) CallJSONWithSource(ctx context.Context, method, requestJSON, source string) (string, error) {
	return b.componentBridge().CallJSONWithSource(ctx, method, requestJSON, source)
}

func (b *SessionRPCBridge) CreateSession(ctx context.Context, req *connect.Request[agentcomposev1.CreateSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return b.componentBridge().CreateSession(ctx, req)
}

func (b *SessionRPCBridge) ResumeSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return b.componentBridge().ResumeSession(ctx, req)
}

func (b *SessionRPCBridge) StopSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return b.componentBridge().StopSession(ctx, req)
}

func (b *SessionRPCBridge) GetSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return b.componentBridge().GetSession(ctx, req)
}

func (b *SessionRPCBridge) ListSessions(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
	return b.componentBridge().ListSessions(ctx, req)
}

func (b *SessionRPCBridge) EnsureSessionProxyReady(ctx context.Context, sessionID string) (*Session, ProxyState, error) {
	return b.componentBridge().EnsureSessionProxyReady(ctx, sessionID)
}

func (b *SessionRPCBridge) ensureSessionProxyReady(ctx context.Context, sessionID string) (*Session, ProxyState, error) {
	return b.EnsureSessionProxyReady(ctx, sessionID)
}

func (b *SessionRPCBridge) ReconcileSessionRuntimeState(ctx context.Context, session *Session) (*Session, error) {
	return b.componentBridge().ReconcileSessionRuntimeState(ctx, session)
}

func (b *SessionRPCBridge) reconcileSessionRuntimeState(ctx context.Context, session *Session) (*Session, error) {
	return b.ReconcileSessionRuntimeState(ctx, session)
}

func (b *SessionRPCBridge) GetSessionProxy(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionProxyResponse], error) {
	return b.componentBridge().GetSessionProxy(ctx, req)
}

func sessionListOptionsFromProto(req *agentcomposev1.ListSessionsRequest) (SessionListOptions, error) {
	return sessions.SessionListOptionsFromProto(req)
}

func parseOptionalRFC3339(raw, field string) (time.Time, error) {
	return sessions.ParseOptionalRFC3339(raw, field)
}

func registerProxyRoutes(app *echo.Echo, service *Service) {
	sessions.RegisterProxyRoutes(app, service.config, service.store, service.sessions.componentBridge())
}
