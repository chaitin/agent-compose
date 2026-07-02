package agentcompose

import (
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
