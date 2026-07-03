package sessions

import (
	"agent-compose/pkg/agentcompose/workspaces"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"context"
	"net"
	"time"

	"github.com/google/uuid"
)

type LifecycleStore interface {
	GetSession(context.Context, string) (*domain.Session, error)
	UpdateSession(context.Context, *domain.Session) error
	GetVMState(string) (domain.VMState, error)
	SaveVMState(string, domain.VMState) error
	GetProxyState(string) (domain.ProxyState, error)
	AddEvent(context.Context, string, domain.SessionEvent) error
}

type SessionDriver interface {
	StartSessionVM(context.Context, *domain.Session) error
	StopSessionVM(context.Context, *domain.Session) error
}

type RuntimeLivenessProvider interface {
	IsSessionAlive(context.Context, string, *domain.Session, domain.VMState) (bool, bool, error)
}

type FacadeTokenRevoker interface {
	RevokeLLMFacadeTokensForSession(context.Context, string) error
}

type LifecycleNotifier interface {
	PublishSessionUpdated(*domain.SessionSummary)
	PublishEventAdded(string, domain.SessionEvent)
	NotifyDashboard(string)
}

type CapabilityGuideWriter func(context.Context, *domain.Session, []string)

type Lifecycle struct {
	Config       *appconfig.Config
	Store        LifecycleStore
	Workspace    workspaces.Store
	Driver       SessionDriver
	Liveness     RuntimeLivenessProvider
	TokenRevoker FacadeTokenRevoker
	Notifier     LifecycleNotifier
	GuideWriter  CapabilityGuideWriter
}

func (l Lifecycle) ReconcileRuntimeState(ctx context.Context, session *domain.Session) (*domain.Session, error) {
	if session == nil || session.Summary.VMStatus != domain.VMStatusRunning {
		return session, nil
	}
	driver, err := driverpkg.ResolveSessionRuntimeDriver(session.Summary.Driver, l.Config.RuntimeDriver)
	if err != nil {
		return nil, err
	}
	if driver != driverpkg.RuntimeDriverMicrosandbox {
		return session, nil
	}
	proxyState, err := l.Store.GetProxyState(session.Summary.ID)
	if err != nil {
		return nil, err
	}
	if JupyterTargetReachable(proxyState, 250*time.Millisecond) {
		return session, nil
	}
	vmState, err := l.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return nil, err
	}
	alive, ok, err := l.Liveness.IsSessionAlive(ctx, driver, session, vmState)
	if err != nil {
		return nil, err
	}
	if !ok || alive {
		return session, nil
	}
	now := time.Now().UTC()
	vmState.StoppedAt = now
	vmState.LastError = ""
	vmState.BoxID = ""
	if err := l.Store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return nil, err
	}
	session.Summary.VMStatus = domain.VMStatusStopped
	if err := l.Store.UpdateSession(ctx, session); err != nil {
		return nil, err
	}
	if l.TokenRevoker != nil {
		_ = l.TokenRevoker.RevokeLLMFacadeTokensForSession(ctx, session.Summary.ID)
	}
	event := domain.SessionEvent{
		ID:        uuid.NewString(),
		Type:      "session.runtime_lost",
		Level:     "warn",
		Message:   "session marked stopped after microsandbox runtime became unreachable",
		CreatedAt: now,
	}
	_ = l.Store.AddEvent(ctx, session.Summary.ID, event)
	if l.Notifier != nil {
		l.Notifier.PublishSessionUpdated(&session.Summary)
		l.Notifier.NotifyDashboard("session_updated")
		l.Notifier.PublishEventAdded(session.Summary.ID, event)
	}
	return l.Store.GetSession(ctx, session.Summary.ID)
}

func (l Lifecycle) EnsureProxyReady(ctx context.Context, sessionID string) (*domain.Session, domain.ProxyState, error) {
	session, err := l.Store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, domain.ProxyState{}, err
	}
	proxyState, err := l.Store.GetProxyState(session.Summary.ID)
	if err != nil {
		return nil, domain.ProxyState{}, err
	}
	if session.Summary.VMStatus == domain.VMStatusRunning && JupyterTargetReachable(proxyState, 1500*time.Millisecond) {
		return session, proxyState, nil
	}
	startCtx, cancel := context.WithTimeout(ctx, l.Config.SessionStartTimeout)
	defer cancel()
	if err := workspaces.PrepareSessionWorkspace(startCtx, l.Config, l.Workspace, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = l.Store.UpdateSession(ctx, session)
		return nil, domain.ProxyState{}, err
	}
	if err := l.Driver.StartSessionVM(startCtx, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = l.Store.UpdateSession(ctx, session)
		return nil, domain.ProxyState{}, err
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := l.Store.UpdateSession(ctx, session); err != nil {
		return nil, domain.ProxyState{}, err
	}
	loaded, err := l.Store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, domain.ProxyState{}, err
	}
	proxyState, err = l.Store.GetProxyState(session.Summary.ID)
	if err != nil {
		return nil, domain.ProxyState{}, err
	}
	return loaded, proxyState, nil
}

func (l Lifecycle) ResumeLoaded(ctx context.Context, session *domain.Session, capsetIDs []string) (*domain.Session, error) {
	if err := workspaces.PrepareSessionWorkspace(ctx, l.Config, l.Workspace, session); err != nil {
		return nil, err
	}
	if l.GuideWriter != nil {
		l.GuideWriter(ctx, session, capsetIDs)
	}
	if err := l.Driver.StartSessionVM(ctx, session); err != nil {
		return nil, err
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := l.Store.UpdateSession(ctx, session); err != nil {
		return nil, err
	}
	l.publishSessionUpdated(&session.Summary)
	event := domain.SessionEvent{
		ID:        uuid.NewString(),
		Type:      "session.resumed",
		Level:     "info",
		Message:   "session resumed with " + session.Summary.Driver + " driver using guest image " + session.Summary.GuestImage,
		CreatedAt: time.Now().UTC(),
	}
	_ = l.Store.AddEvent(ctx, session.Summary.ID, event)
	l.publishEventAdded(session.Summary.ID, event)
	loaded, err := l.Store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, err
	}
	domain.RestoreSessionTransientFields(loaded, session)
	return loaded, nil
}

func (l Lifecycle) StopLoaded(ctx context.Context, session *domain.Session) (*domain.Session, bool, error) {
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return session, false, nil
	}
	if err := l.Driver.StopSessionVM(ctx, session); err != nil {
		return nil, false, err
	}
	session.Summary.VMStatus = domain.VMStatusStopped
	if err := l.Store.UpdateSession(ctx, session); err != nil {
		return nil, false, err
	}
	l.publishSessionUpdated(&session.Summary)
	event := domain.SessionEvent{
		ID:        uuid.NewString(),
		Type:      "session.stopped",
		Level:     "info",
		Message:   "session stopped",
		CreatedAt: time.Now().UTC(),
	}
	_ = l.Store.AddEvent(ctx, session.Summary.ID, event)
	l.publishEventAdded(session.Summary.ID, event)
	loaded, err := l.Store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, false, err
	}
	return loaded, true, nil
}

func (l Lifecycle) publishSessionUpdated(summary *domain.SessionSummary) {
	if l.Notifier == nil {
		return
	}
	l.Notifier.PublishSessionUpdated(summary)
	l.Notifier.NotifyDashboard("session_updated")
}

func (l Lifecycle) publishEventAdded(sessionID string, event domain.SessionEvent) {
	if l.Notifier != nil {
		l.Notifier.PublishEventAdded(sessionID, event)
	}
}

func JupyterTargetReachable(proxyState domain.ProxyState, timeout time.Duration) bool {
	_, port := driverpkg.JupyterConnectTarget(execution.ToDriverProxyState(proxyState))
	if port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", driverpkg.JupyterConnectAddress(execution.ToDriverProxyState(proxyState)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
