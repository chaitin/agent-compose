package adapters

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/llms"
)

// LLMUpstreamProber verifies which protocols a configured connection actually
// serves and logs the report.
//
// Probing is a diagnostic: no routing decision depends on it, an operator can
// turn it off with LLM_UPSTREAM_PROBE, and every request it issues is bounded by
// the configured timeout. It owns the probe client so the startup sweep and the
// per-write check share one timeout and one log shape.
type LLMUpstreamProber struct {
	enabled bool
	timeout time.Duration
	logger  *slog.Logger
	prober  *llms.UpstreamProber
}

// NewLLMUpstreamProber builds the prober from daemon configuration.
func NewLLMUpstreamProber(config *appconfig.Config, logger *slog.Logger) *LLMUpstreamProber {
	if config == nil {
		return &LLMUpstreamProber{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	timeout := config.LLMProbeTimeout
	if timeout <= 0 {
		timeout = llms.DefaultProbeTimeout
	}
	return &LLMUpstreamProber{
		enabled: config.LLMUpstreamProbe,
		timeout: timeout,
		logger:  logger,
		prober:  llms.NewUpstreamProber(&http.Client{Timeout: timeout}),
	}
}

// ProbeConfigured probes every enabled connection once, after startup has
// materialized the catalog. The whole sweep shares one deadline, so a hanging
// upstream cannot delay readiness indefinitely and the remaining connections are
// reported inconclusive instead.
func (p *LLMUpstreamProber) ProbeConfigured(ctx context.Context, store llms.CatalogStore) {
	if p == nil || !p.enabled || store == nil {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	if err := llms.ProbeConfiguredUpstreams(probeCtx, p.logger, p.prober, store); err != nil {
		p.logger.Warn("llm upstream probe did not complete", "error", err)
	}
}

// ProbeConnection probes one connection, which is what the LLM service calls
// after a provider write. It blocks the write for at most the configured
// timeout, so an operator sees the verdict immediately without the request
// being able to hang.
func (p *LLMUpstreamProber) ProbeConnection(ctx context.Context, provider llms.Provider, model string) {
	if p == nil || !p.enabled {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	llms.ProbeUpstreamAndLog(probeCtx, p.logger, p.prober, provider, model)
}

// Capabilities reports the cached verdict for a connection. It is read-only and
// never probes: a client asking for configuration should not wait on an
// upstream, and reporting "not probed yet" is honest about what the daemon
// knows. Probing disabled means no verdict is ever reported.
func (p *LLMUpstreamProber) Capabilities(provider llms.Provider) (llms.UpstreamProbeResult, bool) {
	if p == nil || !p.enabled || p.prober == nil {
		return llms.UpstreamProbeResult{}, false
	}
	return p.prober.LookupUpstreamProbe(provider)
}
