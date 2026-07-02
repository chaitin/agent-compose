package app

import (
	"context"
	"strings"
	"time"
)

func (s *Service) JupyterProxyBasePath() string {
	if s == nil || s.config == nil {
		return ""
	}
	return strings.TrimRight(s.config.JupyterProxyBasePath, "/")
}

func (s *Service) EnsureSessionProxyReady(ctx context.Context, sessionID string) (*Session, ProxyState, error) {
	return s.ensureSessionProxyReady(ctx, sessionID)
}

func (s *Service) GetSessionProxyState(sessionID string) (ProxyState, error) {
	return s.store.GetProxyState(sessionID)
}

func JupyterTargetReachable(proxyState ProxyState, timeout time.Duration) bool {
	return jupyterTargetReachable(proxyState, timeout)
}
