package llms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	// DefaultProbeTimeout bounds each probe request when the injected client
	// carries no timeout of its own.
	DefaultProbeTimeout = 10 * time.Second
	// probeBodyLimit caps how much of a probe response is read and classified.
	probeBodyLimit = 4 << 10
	// probeDetailLimit caps the response excerpt a probe keeps for a log line.
	probeDetailLimit = 200
	// probeModelListLimit caps how many model ids a probe reports.
	probeModelListLimit = 50
)

// ProbeOutcome classifies how one protocol probe ended.
type ProbeOutcome string

const (
	// ProbeSupported means the endpoint served the minimal request over this
	// protocol.
	ProbeSupported ProbeOutcome = "supported"
	// ProbeUnsupported means the endpoint does not serve this protocol.
	ProbeUnsupported ProbeOutcome = "unsupported"
	// ProbeInconclusive means the probe could not decide: a timeout, a server
	// error, a rate limit, or a 2xx body that did not look like the protocol.
	ProbeInconclusive ProbeOutcome = "inconclusive"
	// ProbeAuthFailed means the endpoint rejected the credential. That says
	// nothing about which protocols it serves.
	ProbeAuthFailed ProbeOutcome = "auth_failed"
)

// ProtocolProbe is the result of probing one protocol on one connection.
type ProtocolProbe struct {
	Protocol Protocol
	Outcome  ProbeOutcome
	Detail   string
}

// UpstreamProbeRequest describes the connection to probe and the model to name
// in the minimal requests. The model should be one the connection declares, so
// the probe exercises the deployment a run would actually use.
type UpstreamProbeRequest struct {
	Provider Provider
	Model    string
}

// UpstreamProbeResult reports what one endpoint was observed to serve.
//
// Protocol support is a set, not a choice: one endpoint commonly serves both
// OpenAI protocols, so a probe never stops at the first success.
type UpstreamProbeResult struct {
	Endpoint string
	// Model is the model the protocol probes named. It is the requested model,
	// or one the endpoint advertises when the connection declares none.
	Model        string
	Models       []string
	ModelsDetail string
	Protocols    []ProtocolProbe
}

// SupportedProtocols returns the protocols the probe proved are served here.
func (r UpstreamProbeResult) SupportedProtocols() []Protocol {
	var protocols []Protocol
	for _, probe := range r.Protocols {
		if probe.Outcome == ProbeSupported {
			protocols = append(protocols, probe.Protocol)
		}
	}
	return protocols
}

// Supports reports whether the probe proved the endpoint serves protocol.
func (r UpstreamProbeResult) Supports(protocol Protocol) bool {
	for _, probe := range r.Protocols {
		if probe.Protocol == protocol && probe.Outcome == ProbeSupported {
			return true
		}
	}
	return false
}

// Proven reports whether the probe reached a supported-or-unsupported verdict
// for at least one protocol. An unreachable endpoint proves nothing, and a
// caller must not turn that silence into a claim.
func (r UpstreamProbeResult) Proven() bool {
	for _, probe := range r.Protocols {
		if probe.Outcome == ProbeSupported || probe.Outcome == ProbeUnsupported {
			return true
		}
	}
	return false
}

// UpstreamProbeProtocols returns the protocols worth probing for a connection.
// The probe stays inside the connection's family because that is the family the
// daemon would route to it.
func UpstreamProbeProtocols(provider Provider) []Protocol {
	if NormalizeProviderType(provider.ProviderType) == ProviderFamilyAnthropic {
		return []Protocol{ProtocolMessages}
	}
	return []Protocol{ProtocolChatCompletions, ProtocolResponses}
}

// UpstreamProber probes configured connections over HTTP. It owns the client so
// a caller controls the transport, the proxy, and the timeout.
type UpstreamProber struct {
	client *http.Client
}

// NewUpstreamProber returns a prober that issues requests through client. A nil
// client, or one without a timeout, gets DefaultProbeTimeout.
func NewUpstreamProber(client *http.Client) *UpstreamProber {
	if client == nil {
		client = &http.Client{}
	}
	if client.Timeout <= 0 {
		cloned := *client
		cloned.Timeout = DefaultProbeTimeout
		client = &cloned
	}
	return &UpstreamProber{client: client}
}

// Probe lists the connection's models and then probes every protocol its family
// can serve, each independently. The error reports a request the daemon cannot
// even build (no endpoint); an unreachable or refusing upstream is part of the
// result, not an error.
//
// The model-list request runs first, both to log the inventory and to supply a
// model when the connection declares none. A gateway that lists no models still
// gets every protocol probed with the caller's model; only a connection with no
// model on either side is reported inconclusive.
func (p *UpstreamProber) Probe(ctx context.Context, req UpstreamProbeRequest) (UpstreamProbeResult, error) {
	provider := req.Provider
	result := UpstreamProbeResult{Endpoint: strings.TrimSpace(provider.BaseURL)}
	if result.Endpoint == "" {
		return result, fmt.Errorf("probe connection %q: a base url is required", provider.ID)
	}
	headers, err := ProviderForwardHeaders(provider)
	if err != nil {
		return result, fmt.Errorf("probe connection %q headers: %w", provider.ID, err)
	}
	result.Models, result.ModelsDetail = p.listModels(ctx, provider, headers)
	model := strings.TrimSpace(req.Model)
	if model == "" && len(result.Models) > 0 {
		model = result.Models[0]
	}
	result.Model = model
	protocols := UpstreamProbeProtocols(provider)
	if model == "" {
		for _, protocol := range protocols {
			result.Protocols = append(result.Protocols, ProtocolProbe{
				Protocol: protocol,
				Outcome:  ProbeInconclusive,
				Detail:   "the connection declares no model and the endpoint listed none",
			})
		}
		return result, nil
	}
	for _, protocol := range protocols {
		result.Protocols = append(result.Protocols, p.probeProtocol(ctx, provider, headers, model, protocol))
	}
	return result, nil
}

// listModels performs the inventory request. Its outcome never gates the
// protocol probes: a gateway without /v1/models can still serve /responses or
// /chat/completions.
func (p *UpstreamProber) listModels(ctx context.Context, provider Provider, headers http.Header) ([]string, string) {
	endpoint := upstreamModelsEndpoint(provider)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, summariseProbeDetail(err.Error())
	}
	copyProbeHeaders(request.Header, headers)
	response, err := p.client.Do(request)
	if err != nil {
		return nil, summariseProbeDetail(err.Error())
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, probeBodyLimit))
	if err != nil {
		return nil, summariseProbeDetail("read response: " + err.Error())
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Sprintf("HTTP %d", response.StatusCode)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "the response is not a model list"
	}
	seen := make(map[string]struct{}, len(payload.Data))
	models := make([]string, 0, len(payload.Data))
	for _, entry := range payload.Data {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	sort.Strings(models)
	if len(models) > probeModelListLimit {
		models = models[:probeModelListLimit]
	}
	return models, ""
}

func (p *UpstreamProber) probeProtocol(ctx context.Context, provider Provider, headers http.Header, model string, protocol Protocol) ProtocolProbe {
	payload, err := probeRequestBody(protocol, model)
	if err != nil {
		return ProtocolProbe{Protocol: protocol, Outcome: ProbeInconclusive, Detail: summariseProbeDetail(err.Error())}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, EndpointForProvider(provider, string(protocol)), bytes.NewReader(payload))
	if err != nil {
		return ProtocolProbe{Protocol: protocol, Outcome: ProbeInconclusive, Detail: summariseProbeDetail(err.Error())}
	}
	copyProbeHeaders(request.Header, headers)
	request.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return ProtocolProbe{Protocol: protocol, Outcome: ProbeInconclusive, Detail: summariseProbeDetail(err.Error())}
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, probeBodyLimit))
	if err != nil {
		return ProtocolProbe{Protocol: protocol, Outcome: ProbeInconclusive, Detail: summariseProbeDetail("read response: " + err.Error())}
	}
	return ClassifyProbeResponse(protocol, response.StatusCode, body)
}

// ClassifyProbeResponse maps one HTTP response to a protocol outcome. It is
// pure, so the classification table is testable without a server.
//
// Only a 404/405/501 verdict is treated as proof the protocol is absent. Every
// ambiguous answer stays inconclusive rather than claiming the upstream does not
// support a protocol it may simply have been rate-limiting.
func ClassifyProbeResponse(protocol Protocol, statusCode int, body []byte) ProtocolProbe {
	probe := ProtocolProbe{Protocol: protocol}
	switch {
	case statusCode >= 200 && statusCode < 300:
		if responseMatchesProtocol(protocol, body) {
			probe.Outcome = ProbeSupported
			probe.Detail = "served a " + string(protocol) + " response"
			return probe
		}
		probe.Outcome = ProbeInconclusive
		probe.Detail = fmt.Sprintf("HTTP %d with a body that does not match %s: %s", statusCode, protocol, summariseProbeBody(body))
	case statusCode == http.StatusNotFound || statusCode == http.StatusMethodNotAllowed || statusCode == http.StatusNotImplemented:
		probe.Outcome = ProbeUnsupported
		probe.Detail = fmt.Sprintf("HTTP %d", statusCode)
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		probe.Outcome = ProbeAuthFailed
		probe.Detail = fmt.Sprintf("HTTP %d", statusCode)
	case statusCode == http.StatusBadRequest || statusCode == http.StatusUnprocessableEntity:
		// The request was well formed and reached a handler for this path, so a
		// semantic rejection means the protocol is served here.
		probe.Outcome = ProbeSupported
		probe.Detail = fmt.Sprintf("HTTP %d: %s", statusCode, summariseProbeBody(body))
	default:
		probe.Outcome = ProbeInconclusive
		probe.Detail = fmt.Sprintf("HTTP %d", statusCode)
	}
	return probe
}

// DeclaredProtocolUnsupported reports the mismatch worth surfacing: the
// connection declares a protocol the probe proved this endpoint cannot serve.
// It reports false when the probe proved nothing, so an unreachable upstream
// never produces a misleading claim.
func DeclaredProtocolUnsupported(provider Provider, result UpstreamProbeResult) (declared Protocol, supported []Protocol, mismatch bool) {
	declared = NormalizeProtocol(provider.DefaultWireAPI)
	if !declared.Valid() || !result.Proven() || result.Supports(declared) {
		return declared, result.SupportedProtocols(), false
	}
	return declared, result.SupportedProtocols(), true
}

// LogUpstreamProbe writes the probe report in the operator-facing form used by
// every trigger: the model list first, then one line per protocol.
func LogUpstreamProbe(logger *slog.Logger, result UpstreamProbeResult) {
	if logger == nil {
		logger = slog.Default()
	}
	if result.ModelsDetail != "" {
		logger.Warn("endpoint model list unavailable", "endpoint", result.Endpoint, "detail", result.ModelsDetail)
	} else {
		logger.Info("endpoint supports model list", "endpoint", result.Endpoint, "models", strings.Join(result.Models, ", "))
	}
	for _, probe := range result.Protocols {
		switch probe.Outcome {
		case ProbeSupported:
			logger.Info("endpoint supports protocol", "endpoint", result.Endpoint, "protocol", string(probe.Protocol), "detail", probe.Detail)
		case ProbeUnsupported:
			logger.Warn("endpoint does not support protocol", "endpoint", result.Endpoint, "protocol", string(probe.Protocol), "detail", probe.Detail)
		case ProbeAuthFailed:
			logger.Warn("endpoint rejected the credential; protocol support is unknown", "endpoint", result.Endpoint, "protocol", string(probe.Protocol))
		default:
			logger.Warn("endpoint protocol probe is inconclusive", "endpoint", result.Endpoint, "protocol", string(probe.Protocol), "detail", probe.Detail)
		}
	}
}

// probeRequestBody builds the smallest request that still proves the protocol:
// one user message and a one-token completion limit.
func probeRequestBody(protocol Protocol, model string) ([]byte, error) {
	messages := []map[string]string{{"role": "user", "content": "ping"}}
	var payload any
	switch protocol {
	case ProtocolChatCompletions:
		payload = map[string]any{"model": model, "messages": messages, "max_tokens": 1, "stream": false}
	case ProtocolMessages:
		payload = map[string]any{"model": model, "messages": messages, "max_tokens": 1}
	default:
		payload = map[string]any{"model": model, "input": "ping", "max_output_tokens": 1, "stream": false}
	}
	return json.Marshal(payload)
}

// responseMatchesProtocol guards against a gateway that answers 200 to
// everything: a success only counts when the body looks like the protocol that
// was asked for.
func responseMatchesProtocol(protocol Protocol, body []byte) bool {
	if len(bytes.TrimSpace(body)) == 0 {
		return false
	}
	var payload struct {
		Object  *string          `json:"object"`
		Type    *string          `json:"type"`
		Output  *json.RawMessage `json:"output"`
		Content *json.RawMessage `json:"content"`
		Choices *json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	// A present field is enough to recognize the shape; the values are not
	// inspected because a probe deliberately asks for the smallest possible
	// response.
	switch protocol {
	case ProtocolChatCompletions:
		return payload.Choices != nil || (payload.Object != nil && *payload.Object == "chat.completion")
	case ProtocolMessages:
		return payload.Content != nil || (payload.Type != nil && *payload.Type == "message")
	default:
		return payload.Output != nil || (payload.Object != nil && *payload.Object == "response")
	}
}

// upstreamModelsEndpoint derives /models from the endpoint one protocol uses,
// so it follows the same base-path normalization as a real call.
func upstreamModelsEndpoint(provider Provider) string {
	protocol := ProtocolResponses
	if NormalizeProviderType(provider.ProviderType) == ProviderFamilyAnthropic {
		protocol = ProtocolMessages
	}
	endpoint := strings.TrimRight(EndpointForProvider(provider, string(protocol)), "/")
	for _, suffix := range []string{"/responses", "/chat/completions", "/messages"} {
		if strings.HasSuffix(endpoint, suffix) {
			return strings.TrimSuffix(endpoint, suffix) + "/models"
		}
	}
	return endpoint + "/models"
}

func copyProbeHeaders(destination, source http.Header) {
	for key, values := range source {
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

// summariseProbeBody collapses a response body into one bounded, single-line
// excerpt. It carries no request headers, so no credential reaches the log.
func summariseProbeBody(body []byte) string {
	return summariseProbeDetail(string(body))
}

func summariseProbeDetail(detail string) string {
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > probeDetailLimit {
		detail = detail[:probeDetailLimit] + "..."
	}
	return detail
}
