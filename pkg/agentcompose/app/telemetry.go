package app

import (
	"github.com/samber/do/v2"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/telemetry"
)

// NewTelemetryProvider builds the daemon trace provider from the same OTLP
// endpoint and headers that configure native provider export in the guest. An
// unset endpoint yields a disabled provider, so a daemon without telemetry
// configuration exports nothing and opens no new connection.
func NewTelemetryProvider(di do.Injector) (*telemetry.Provider, error) {
	conf := do.MustInvoke[*appconfig.Config](di)
	return telemetry.NewProvider(conf.AgentTelemetry.Endpoint, conf.AgentTelemetry.Headers, conf.Version)
}

// NewTelemetryTracer exposes the daemon tracer to the components that start
// spans. It is a no-op tracer while the provider is disabled.
func NewTelemetryTracer(di do.Injector) (*telemetry.Tracer, error) {
	return do.MustInvoke[*telemetry.Provider](di).Tracer(), nil
}
