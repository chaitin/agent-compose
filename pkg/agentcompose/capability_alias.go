package agentcompose

import (
	"context"

	"github.com/samber/do/v2"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/capproxy"
)

const (
	capProxyTargetEnvName         = capabilities.CapProxyTargetEnvName
	capabilitySessionTokenEnvName = capabilities.CapabilitySessionTokenEnvName
	capabilityCapsetTagName       = capabilities.CapabilityCapsetTagName
)

type CapabilityProvider = capabilities.Provider
type capabilityIntegration = capabilities.Integration
type CapabilityService = capabilities.Service

func NewCapabilityProvider(di do.Injector) (capabilityIntegration, error) {
	return capabilities.NewCapabilityProvider(di)
}

func NewCapProxyServer(di do.Injector) (*capproxy.Server, error) {
	return capabilities.NewCapProxyServer(di)
}

func normalizeCapsetIDs(ids []string) []string {
	return capabilities.NormalizeCapsetIDs(ids)
}

func sessionCapabilityCapsets(session *Session) []string {
	return capabilities.SessionCapabilityCapsets(session)
}

func buildCapabilityGatewaySessionVars(publicTarget string, capsetIDs []string) ([]SessionEnvVar, []SessionTag) {
	return capabilities.BuildGatewaySessionVars(publicTarget, capsetIDs)
}

func writeCapabilityGuide(ctx context.Context, provider CapabilityProvider, store *Store, streams *SessionStreamBroker, session *Session, capsetIDs []string) {
	capabilities.WriteGuide(ctx, provider, store, streams, session, capsetIDs)
}

func capabilityGatewayProxyTarget(provider CapabilityProvider) string {
	return capabilities.GatewayProxyTarget(provider)
}

func sessionCapabilityGuidePath(session *Session) string {
	return capabilities.SessionGuidePath(session)
}
