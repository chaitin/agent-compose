package llms

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// ProbeUpstreamAndLog probes one connection and writes the report. Every
// trigger — daemon startup and a provider write — goes through it, so the log
// shape and the declared-protocol warning cannot drift apart.
func ProbeUpstreamAndLog(ctx context.Context, logger *slog.Logger, prober *UpstreamProber, provider Provider, model string) {
	if prober == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	result, err := prober.Probe(ctx, UpstreamProbeRequest{Provider: provider, Model: model})
	if err != nil {
		logger.Warn("llm upstream probe could not run", "connection", provider.ID, "error", err)
		return
	}
	logger.Info("llm upstream probe", "connection", provider.ID, "endpoint", result.Endpoint, "model", result.Model)
	LogUpstreamProbe(logger, result)
	if declared, supported, mismatch := DeclaredProtocolUnsupported(provider, result); mismatch {
		logger.Warn("llm upstream does not support the declared protocol",
			"connection", provider.ID, "endpoint", result.Endpoint,
			"declared", string(declared), "supported", joinProtocols(supported))
	}
}

// ProbeConfiguredUpstreams probes every enabled connection once and logs the
// report. A connection that declares no model is probed with a model the
// endpoint itself advertises, so the check still reaches a real deployment.
//
// The caller bounds the whole sweep through ctx: probes after the deadline fail
// fast and are reported as inconclusive rather than delaying the trigger.
func ProbeConfiguredUpstreams(ctx context.Context, logger *slog.Logger, prober *UpstreamProber, store CatalogStore) error {
	if prober == nil || store == nil {
		return nil
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return fmt.Errorf("list llm connections for probe: %w", err)
	}
	bindings, err := store.ListLLMProviderModelConfigs(ctx)
	if err != nil {
		return fmt.Errorf("list llm model bindings for probe: %w", err)
	}
	defaultProvider, defaultModel, hasDefault, err := store.DefaultLLMModelReference(ctx)
	if err != nil {
		return fmt.Errorf("read the llm default model for probe: %w", err)
	}
	models := probeModelIndex(bindings, defaultProvider, defaultModel, hasDefault)
	for _, provider := range providers {
		ProbeUpstreamAndLog(ctx, logger, prober, provider, models[provider.ID])
	}
	return nil
}

// probeModelIndex picks one representative model per connection: the catalog
// default model when it belongs to that connection, otherwise the
// lexicographically first bound model. One model is enough to prove an endpoint
// serves a protocol.
func probeModelIndex(bindings []ProviderModelBinding, defaultProvider, defaultModel string, hasDefault bool) map[string]string {
	models := map[string]string{}
	for _, binding := range bindings {
		providerID := strings.TrimSpace(binding.ProviderID)
		modelID := strings.TrimSpace(binding.ModelID)
		if providerID == "" || modelID == "" {
			continue
		}
		if current, ok := models[providerID]; !ok || modelID < current {
			models[providerID] = modelID
		}
	}
	if !hasDefault {
		return models
	}
	if providerID := strings.TrimSpace(defaultProvider); providerID != "" {
		models[providerID] = strings.TrimSpace(defaultModel)
	}
	return models
}

func joinProtocols(protocols []Protocol) string {
	if len(protocols) == 0 {
		return "none proven"
	}
	names := make([]string, 0, len(protocols))
	for _, protocol := range protocols {
		names = append(names, string(protocol))
	}
	return strings.Join(names, ", ")
}
