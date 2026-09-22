package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
	"github.com/labstack/echo/v4"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type RuntimeLLMTokenStore interface {
	GetLLMFacadeToken(context.Context, string) (llms.FacadeToken, error)
}

type RuntimeLLMSandboxStore interface {
	GetSandbox(context.Context, string) (*domain.Sandbox, error)
}

// RuntimeLLMConnectionResolver resolves the upstream target of the connection a
// facade token is bound to, for one literal upstream model.
//
// The token names the connection, so a proxied call never selects one: it looks
// up the connection the run was prepared with and forwards the requested model
// to it. That is what makes the daemon a weak caller — it holds an address and a
// credential, and the upstream decides which models it serves.
type RuntimeLLMConnectionResolver func(ctx context.Context, connectionID, model string) (llms.ResolvedTarget, error)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type RuntimeLLMOptions struct {
	Tokens      RuntimeLLMTokenStore
	Sandboxes   RuntimeLLMSandboxStore
	Connections RuntimeLLMConnectionResolver
	Client      HTTPDoer
	// MaxOutputTokens, when > 0, is injected into every proxied upstream LLM
	// request using the field names supported by the upstream protocol. codex
	// does not send max_output_tokens itself, so without this API proxies that
	// default to a small limit silently truncate long outputs.
	MaxOutputTokens int
}

func RegisterRuntimeLLMFacadeRoutes(app *echo.Echo, opts RuntimeLLMOptions) {
	handler := runtimeLLMHandler{opts: opts}
	app.POST("/api/runtime/sandboxes/:sandbox_id/llm/openai/v1/responses", handler.handleResponses)
	app.POST("/api/runtime/sandboxes/:sandbox_id/llm/openai/v1/chat/completions", handler.handleChatCompletions)
	app.POST("/api/runtime/sandboxes/:sandbox_id/llm/anthropic/v1/messages", handler.handleAnthropicMessages)
}

type runtimeLLMHandler struct {
	opts RuntimeLLMOptions
}

func (h runtimeLLMHandler) handleResponses(c echo.Context) error {
	return h.handle(c, protocolbridge.ProtocolOpenAIResponses, llms.APIProtocolResponses)
}

func (h runtimeLLMHandler) handleChatCompletions(c echo.Context) error {
	return h.handle(c, protocolbridge.ProtocolOpenAIChat, llms.APIProtocolChatCompletions)
}

func (h runtimeLLMHandler) handleAnthropicMessages(c echo.Context) error {
	return h.handle(c, protocolbridge.ProtocolAnthropicMessages, llms.APIProtocolMessages)
}

// resolvedRuntimeLLMRequest is the request state handle needs after
// authorizeAndResolveRuntimeLLMRequest has validated the caller and decided
// which upstream target to proxy the request to.
type resolvedRuntimeLLMRequest struct {
	InboundAdapter   protocolbridge.Adapter
	LLMRequest       *protocolbridge.LLMRequest
	Body             []byte
	Target           llms.ResolvedTarget
	UpstreamProtocol protocolbridge.Protocol
	UpstreamEndpoint string
}

// authorizeAndResolveRuntimeLLMRequest validates the caller's facade token,
// checks the sandbox is running, decodes the inbound LLM request, and resolves
// the connection the token was issued for. If handled is true, the caller must
// return err as-is (a response has already been written).
func (h runtimeLLMHandler) authorizeAndResolveRuntimeLLMRequest(c echo.Context, inboundProtocol protocolbridge.Protocol, facadeWireAPI string) (resolvedRuntimeLLMRequest, bool, error) {
	if h.opts.Tokens == nil || h.opts.Sandboxes == nil || h.opts.Connections == nil {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusInternalServerError, map[string]string{"error": "llm facade dependencies are required"})
	}
	sandboxID := strings.TrimSpace(c.Param("sandbox_id"))
	rawToken := llms.RuntimeFacadeToken(c.Request().Header)
	if sandboxID == "" || rawToken == "" {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusUnauthorized, map[string]string{"error": "llm facade token is required"})
	}
	token, err := h.opts.Tokens.GetLLMFacadeToken(c.Request().Context(), rawToken)
	if err != nil {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid llm facade token"})
	}
	now := time.Now().UTC()
	if token.SandboxID != sandboxID || !token.RevokedAt.IsZero() || (!token.ExpiresAt.IsZero() && now.After(token.ExpiresAt)) {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusForbidden, map[string]string{"error": "llm facade token is not valid for this sandbox"})
	}
	if token.WireAPI != "" && llms.NormalizeWireAPI(token.WireAPI) != llms.NormalizeWireAPI(facadeWireAPI) {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusForbidden, map[string]string{"error": "llm facade token wire api mismatch"})
	}
	session, err := h.opts.Sandboxes.GetSandbox(c.Request().Context(), sandboxID)
	if err != nil {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusForbidden, map[string]string{"error": "sandbox is not available"})
	}
	if session.Summary.VMStatus == domain.VMStatusStopped || session.Summary.VMStatus == domain.VMStatusFailed {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusForbidden, map[string]string{"error": "sandbox is not running"})
	}
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, 64<<20))
	if err != nil {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusBadRequest, map[string]string{"error": "read llm request failed"})
	}
	inboundAdapter, err := llms.ProtocolAdapter(inboundProtocol)
	if err != nil {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	llmReq, err := inboundAdapter.DecodeRequest(body)
	if err != nil {
		raw, status := inboundAdapter.EncodeError(err)
		return resolvedRuntimeLLMRequest{}, true, WriteRuntimeLLMEncodedError(c, raw, status)
	}
	requestedModel := strings.TrimSpace(llmReq.Model)
	if requestedModel == "" {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusBadRequest, map[string]string{"error": "llm model is required"})
	}
	model, authorized := token.ResolveUpstreamModel(requestedModel)
	if !authorized {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusForbidden, map[string]string{"error": "llm facade token model mismatch"})
	}
	if token.ProviderID == "" {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusForbidden, map[string]string{"error": "llm facade token is not bound to a connection"})
	}
	// The model the upstream is asked for is the literal one. The guest's own
	// spelling reached the token at preparation time; from here on the request
	// carries what the connection understands.
	llmReq.Model = model
	target, err := h.opts.Connections(c.Request().Context(), token.ProviderID, model)
	if err != nil {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	upstreamProtocol, upstreamEndpoint, err := llms.UpstreamProtocolAndEndpoint(target)
	if err != nil {
		return resolvedRuntimeLLMRequest{}, true, c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return resolvedRuntimeLLMRequest{
		InboundAdapter:   inboundAdapter,
		LLMRequest:       llmReq,
		Body:             body,
		Target:           target,
		UpstreamProtocol: upstreamProtocol,
		UpstreamEndpoint: upstreamEndpoint,
	}, false, nil
}

func (h runtimeLLMHandler) handle(c echo.Context, inboundProtocol protocolbridge.Protocol, facadeWireAPI string) error {
	resolved, handled, err := h.authorizeAndResolveRuntimeLLMRequest(c, inboundProtocol, facadeWireAPI)
	if handled {
		return err
	}
	inboundAdapter, llmReq, body := resolved.InboundAdapter, resolved.LLMRequest, resolved.Body
	target, upstreamProtocol, upstreamEndpoint := resolved.Target, resolved.UpstreamProtocol, resolved.UpstreamEndpoint

	if inboundProtocol == upstreamProtocol {
		upstreamBody, err := llms.RewriteRuntimeRequestForUpstream(body, target, upstreamProtocol)
		if err != nil {
			raw, status := inboundAdapter.EncodeError(err)
			return WriteRuntimeLLMEncodedError(c, raw, status)
		}
		upstreamBody = injectMaxOutputTokens(upstreamBody, upstreamProtocol, effectiveMaxOutputTokens(target, h.opts.MaxOutputTokens))
		return h.proxyTransparent(c, proxyTransparentRequest{
			UpstreamEndpoint: upstreamEndpoint,
			Body:             upstreamBody,
			Target:           target,
			UpstreamProtocol: upstreamProtocol,
		})
	}
	upstreamBody, err := llms.EncodeRuntimeUpstreamRequest(inboundProtocol, upstreamProtocol, target, llmReq)
	if err != nil {
		raw, status := inboundAdapter.EncodeError(err)
		return WriteRuntimeLLMEncodedError(c, raw, status)
	}
	upstreamBody = injectMaxOutputTokens(upstreamBody, upstreamProtocol, effectiveMaxOutputTokens(target, h.opts.MaxOutputTokens))
	upstreamReq, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, upstreamEndpoint, bytes.NewReader(upstreamBody))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create upstream llm request failed"})
	}
	llms.CopyRuntimeHeaders(upstreamReq.Header, c.Request().Header)
	llms.ApplyForwardHeaders(upstreamReq.Header, target.Headers)
	resp, err := h.httpClient().Do(upstreamReq)
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "call upstream llm failed"})
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		llms.CopyRuntimeResponseHeaders(c.Response().Header(), resp.Header)
		c.Response().WriteHeader(resp.StatusCode)
		if err := llms.CopyRuntimeResponseBody(c.Response().Writer, resp); err != nil && !errors.Is(err, http.ErrAbortHandler) {
			return err
		}
		return nil
	}
	if llms.RuntimeResponseShouldFlush(resp.Header) {
		return BridgeRuntimeLLMStreamResponse(c, resp, runtimeLLMStreamBridgeRequest{
			InboundProtocol:  inboundProtocol,
			UpstreamProtocol: upstreamProtocol,
			UpstreamFamily:   llms.NormalizeProviderType(target.Provider.ProviderType),
			Model:            target.Model.Name,
		})
	}
	upstreamRespBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "read upstream llm response failed"})
	}
	clientBody, err := llms.EncodeRuntimeClientResponse(inboundProtocol, upstreamProtocol, target, upstreamRespBody)
	if err != nil {
		raw, status := inboundAdapter.EncodeError(err)
		return WriteRuntimeLLMEncodedError(c, raw, status)
	}
	llms.CopyRuntimeResponseHeaders(c.Response().Header(), resp.Header)
	c.Response().Header().Set("Content-Type", "application/json")
	c.Response().Header().Del("Content-Length")
	c.Response().WriteHeader(resp.StatusCode)
	_, err = c.Response().Writer.Write(clientBody)
	return err
}

func effectiveMaxOutputTokens(target llms.ResolvedTarget, fallback int) int {
	if target.MaxOutputTokens > 0 {
		return target.MaxOutputTokens
	}
	return fallback
}

// injectMaxOutputTokens adds the configured output-token limit to a proxied
// upstream LLM request body. codex does not send max_output_tokens itself
// (see openai/codex#36180), and the protocol bridge defaults the chat
// completions max_completion_tokens to a small value (e.g. 4096); upstream
// proxies honor that field and silently truncate long agent outputs. Setting
// both OpenAI Chat field names ensures the configured limit wins. Anthropic
// Messages receives only max_tokens because it rejects max_completion_tokens.
func injectMaxOutputTokens(body []byte, upstreamProtocol protocolbridge.Protocol, maxTokens int) []byte {
	if maxTokens <= 0 || len(body) == 0 {
		return body
	}
	fields := map[string]int{}
	switch upstreamProtocol {
	case protocolbridge.ProtocolOpenAIResponses:
		fields["max_output_tokens"] = maxTokens
	case protocolbridge.ProtocolOpenAIChat:
		fields["max_tokens"] = maxTokens
		fields["max_completion_tokens"] = maxTokens
	case protocolbridge.ProtocolAnthropicMessages:
		fields["max_tokens"] = maxTokens
	default:
		return body
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	for field, value := range fields {
		obj[field] = value
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// proxyTransparentRequest bundles the upstream request details proxyTransparent
// needs to relay a request whose inbound and upstream wire protocols match.
type proxyTransparentRequest struct {
	UpstreamEndpoint string
	Body             []byte
	Target           llms.ResolvedTarget
	UpstreamProtocol protocolbridge.Protocol
}

func (h runtimeLLMHandler) proxyTransparent(c echo.Context, req proxyTransparentRequest) error {
	target, upstreamProtocol := req.Target, req.UpstreamProtocol
	upstreamReq, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, req.UpstreamEndpoint, bytes.NewReader(req.Body))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create upstream llm request failed"})
	}
	llms.CopyRuntimeHeaders(upstreamReq.Header, c.Request().Header)
	llms.ApplyForwardHeaders(upstreamReq.Header, target.Headers)
	resp, err := h.httpClient().Do(upstreamReq)
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "call upstream llm failed"})
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && llms.UseGenericResponsesTextParts(target, upstreamProtocol) {
		if llms.RuntimeResponseShouldFlush(resp.Header) {
			return BridgeRuntimeLLMStreamResponse(c, resp, runtimeLLMStreamBridgeRequest{
				InboundProtocol:  protocolbridge.ProtocolOpenAIResponses,
				UpstreamProtocol: protocolbridge.ProtocolOpenAIResponses,
				UpstreamFamily:   llms.ProviderFamilyOpenAI,
				Model:            target.Model.Name,
			})
		}
		upstreamRespBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			return c.JSON(http.StatusBadGateway, map[string]string{"error": "read upstream llm response failed"})
		}
		clientBody, err := llms.EncodeRuntimeClientResponse(protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIChat, target, upstreamRespBody)
		if err != nil {
			adapter := protocolbridge.NewOpenAIResponsesAdapter()
			raw, status := adapter.EncodeError(err)
			return WriteRuntimeLLMEncodedError(c, raw, status)
		}
		llms.CopyRuntimeResponseHeaders(c.Response().Header(), resp.Header)
		c.Response().Header().Set("Content-Type", "application/json")
		c.Response().Header().Del("Content-Length")
		c.Response().WriteHeader(resp.StatusCode)
		_, err = c.Response().Writer.Write(clientBody)
		return err
	}
	llms.CopyRuntimeResponseHeaders(c.Response().Header(), resp.Header)
	c.Response().WriteHeader(resp.StatusCode)
	if err := llms.CopyRuntimeResponseBody(c.Response().Writer, resp); err != nil && !errors.Is(err, http.ErrAbortHandler) {
		return err
	}
	return nil
}

func (h runtimeLLMHandler) httpClient() HTTPDoer {
	if h.opts.Client != nil {
		return h.opts.Client
	}
	return http.DefaultClient
}

func WriteRuntimeLLMEncodedError(c echo.Context, raw []byte, status int) error {
	if status == 0 {
		status = http.StatusBadRequest
	}
	return c.Blob(status, "application/json", raw)
}

// runtimeLLMStreamBridgeRequest bundles the protocol/model metadata
// BridgeRuntimeLLMStreamResponse needs to translate an upstream SSE stream
// into the inbound wire protocol.
type runtimeLLMStreamBridgeRequest struct {
	InboundProtocol  protocolbridge.Protocol
	UpstreamProtocol protocolbridge.Protocol
	UpstreamFamily   string
	Model            string
}

func BridgeRuntimeLLMStreamResponse(c echo.Context, resp *http.Response, req runtimeLLMStreamBridgeRequest) error {
	inboundProtocol := req.InboundProtocol
	decoder, encoder, err := llms.RuntimeStreamBridge(req.InboundProtocol, req.UpstreamProtocol, req.UpstreamFamily, req.Model)
	if err != nil {
		return err
	}
	llms.CopyRuntimeResponseHeaders(c.Response().Header(), resp.Header)
	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Del("Content-Length")
	c.Response().Header().Del("Content-Encoding")
	c.Response().WriteHeader(resp.StatusCode)
	flusher, _ := c.Response().Writer.(http.Flusher)
	writeEvents := func(events []protocolbridge.RawStreamEvent) error {
		for _, event := range events {
			if err := llms.WriteRawSSEEvent(c.Response().Writer, event); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		return nil
	}
	textOpen := false
	encodePart := func(part protocolbridge.StreamPart) error {
		if inboundProtocol == protocolbridge.ProtocolOpenAIResponses {
			switch part.Type {
			case protocolbridge.StreamTextStart:
				textOpen = true
			case protocolbridge.StreamTextDelta:
				textOpen = true
			case protocolbridge.StreamTextEnd:
				if !textOpen {
					return nil
				}
				textOpen = false
			case protocolbridge.StreamFinish:
				if textOpen {
					events, encodeErr := encoder.Encode(protocolbridge.StreamPart{Type: protocolbridge.StreamTextEnd})
					if encodeErr != nil {
						return encodeErr
					}
					if err := writeEvents(events); err != nil {
						return err
					}
					textOpen = false
				}
			}
		}
		events, encodeErr := encoder.Encode(part)
		if encodeErr != nil {
			return encodeErr
		}
		return writeEvents(events)
	}
	err = llms.ReadRawSSEEvents(resp.Body, func(event protocolbridge.RawStreamEvent) error {
		parts, decodeErr := decoder.Decode(event)
		if decodeErr != nil {
			return decodeErr
		}
		for _, part := range parts {
			if err := encodePart(part); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = writeEvents(encoder.EncodeError(err))
		return nil
	}
	parts, err := decoder.Close()
	if err != nil {
		_ = writeEvents(encoder.EncodeError(err))
		return nil
	}
	for _, part := range parts {
		if err := encodePart(part); err != nil {
			_ = writeEvents(encoder.EncodeError(err))
			return err
		}
	}
	events, err := encoder.Close()
	if err != nil {
		_ = writeEvents(encoder.EncodeError(err))
		return nil
	}
	return writeEvents(events)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
