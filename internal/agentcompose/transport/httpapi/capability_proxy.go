package httpapi

import "agent-compose/internal/capproxy"

type CapabilityProxyConfig struct {
	Listen          string
	OctoBus         capproxy.OctoBusResolver
	SessionResolver capproxy.SessionResolver
}

func NewCapabilityProxyServer(config CapabilityProxyConfig) *capproxy.Server {
	return capproxy.NewServer(capproxy.Config{
		Listen:  config.Listen,
		OctoBus: config.OctoBus,
	}, config.SessionResolver)
}
