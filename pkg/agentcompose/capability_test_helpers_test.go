package agentcompose

import (
	"context"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/storage"
)

type fixedGatewaySource struct {
	settings storage.CapabilityGatewaySettings
}

func (f fixedGatewaySource) GetCapabilityGateway(context.Context) (storage.CapabilityGatewaySettings, error) {
	return f.settings, nil
}

func newTestCapabilityProvider(addr, proxyTarget string) CapabilityProvider {
	return capabilities.NewProvider(fixedGatewaySource{settings: storage.CapabilityGatewaySettings{Addr: addr}}, proxyTarget)
}
