package capability

import (
	"context"
	"strings"
)

const (
	ProxyTargetEnvName         = "CAP_GRPC_TARGET"
	SessionTokenEnvName        = "CAP_TOKEN"
	CapsetTagName              = "capset"
	GuideWarningEventType      = "capability.guide.warning"
	DefaultNotConfiguredStatus = "not_configured"
)

// GatewaySettings is the page-configured OctoBus connection. The
// deployment-fixed proxy listen/target addresses are intentionally not stored
// here.
type GatewaySettings struct {
	Addr  string
	Token string
}

func (s GatewaySettings) Trimmed() GatewaySettings {
	return GatewaySettings{
		Addr:  strings.TrimSpace(s.Addr),
		Token: strings.TrimSpace(s.Token),
	}
}

func (s GatewaySettings) Configured() bool {
	return strings.TrimSpace(s.Addr) != ""
}

// GatewaySource supplies the page-configured OctoBus connection.
type GatewaySource interface {
	GetCapabilityGateway(ctx context.Context) (GatewaySettings, error)
}

type SessionEnvVar struct {
	Name   string
	Value  string
	Secret bool
}

type SessionTag struct {
	Name  string
	Value string
}
