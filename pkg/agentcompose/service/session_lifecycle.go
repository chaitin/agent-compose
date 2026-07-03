package agentcompose

import (
	"context"

	"agent-compose/pkg/dashboard"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
)

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
	aliveRuntime, ok := runtime.(sessionAliveRuntime)
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

func (s *Service) sessionLifecycle(notifier sessions.LifecycleNotifier) sessions.Lifecycle {
	return sessions.Lifecycle{
		Config:       s.config,
		Store:        s.store,
		Workspace:    s.configDB,
		Driver:       s.driver,
		Liveness:     sessionRuntimeLiveness{runtimes: s.runtimes},
		TokenRevoker: s.configDB,
		Notifier:     notifier,
	}
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
