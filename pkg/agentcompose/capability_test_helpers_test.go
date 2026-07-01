package agentcompose

import (
	"context"

	"agent-compose/pkg/capabilities"
)

type fixedGatewaySource struct {
	settings CapabilityGatewaySettings
}

func (f fixedGatewaySource) GetCapabilityGateway(context.Context) (CapabilityGatewaySettings, error) {
	return f.settings, nil
}

func newTestCapabilityProvider(addr, proxyTarget string) CapabilityProvider {
	return capabilities.NewProvider(fixedGatewaySource{settings: CapabilityGatewaySettings{Addr: addr}}, proxyTarget)
}
