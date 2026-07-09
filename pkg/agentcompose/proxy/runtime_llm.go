package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
	"github.com/labstack/echo/v4"

	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

type RuntimeLLMTokenStore interface {
	GetLLMFacadeToken(context.Context, string) (llms.FacadeToken, error)
}

type RuntimeLLMSandboxStore interface {
	GetSandbox(context.Context, string) (*domain.Sandbox, error)
}

type RuntimeLLMTargetResolver func(ctx context.Context, requestedModel, providerID string) (llms.ResolvedTarget, error)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type RuntimeLLMOptions struct {
	Tokens        RuntimeLLMTokenStore
	Sandboxes     RuntimeLLMSandboxStore
	ResolveTarget RuntimeLLMTargetResolver
	Client        HTTPDoer
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

func (h runtimeLLMHandler) handle(c echo.Context, inboundProtocol protocolbridge.Protocol, facadeWireAPI string) error {
	if h.opts.Tokens == nil || h.opts.Sandboxes == nil || h.opts.ResolveTarget == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "llm facade dependencies are required"})
	}
	sandboxID := strings.TrimSpace(c.Param("sandbox_id"))
	rawToken := llms.RuntimeFacadeToken(c.Request().Header)
	if sandboxID == "" || rawToken == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "llm facade token is required"})
	}
	token, err := h.opts.Tokens.GetLLMFacadeToken(c.Request().Context(), rawToken)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid llm facade token"})
	}
	now := time.Now().UTC()
	if token.SandboxID != sandboxID || !token.RevokedAt.IsZero() || (!token.ExpiresAt.IsZero() && now.After(token.ExpiresAt)) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "llm facade token is not valid for this sandbox"})
	}
	if token.WireAPI != "" && llms.NormalizeWireAPI(token.WireAPI) != llms.NormalizeWireAPI(facadeWireAPI) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "llm facade token wire api mismatch"})
	}
	session, err := h.opts.Sandboxes.GetSandbox(c.Request().Context(), sandboxID)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "sandbox is not available"})
	}
	if session.Summary.VMStatus == domain.VMStatusStopped || session.Summary.VMStatus == domain.VMStatusFailed {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "sandbox is not running"})
	}
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, 64<<20))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "read llm request failed"})
	}
	inboundAdapter, err := llms.ProtocolAdapter(inboundProtocol)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	llmReq, err := inboundAdapter.DecodeRequest(body)
	if err != nil {
		raw, status := inboundAdapter.EncodeError(err)
		return WriteRuntimeLLMEncodedError(c, raw, status)
	}
	model := strings.TrimSpace(llmReq.Model)
	if model == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "llm model is required"})
	}
	if token.Model != "" && model != "" && token.Model != model {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "llm facade token model mismatch"})
	}
	target, err := h.opts.ResolveTarget(c.Request().Context(), firstNonEmpty(token.Model, model), token.ProviderID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if token.ProviderID != "" && token.ProviderID != target.Provider.ID {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "llm facade token provider mismatch"})
	}
	upstreamProtocol, upstreamEndpoint, err := llms.UpstreamProtocolAndEndpoint(target)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if inboundProtocol == upstreamProtocol {
		upstreamBody, err := llms.RewriteRuntimeRequestForUpstream(body, target, upstreamProtocol)
		if err != nil {
			raw, status := inboundAdapter.EncodeError(err)
			return WriteRuntimeLLMEncodedError(c, raw, status)
		}
		return h.proxyTransparent(c, upstreamEndpoint, upstreamBody, target, upstreamProtocol)
	}
	upstreamBody, err := llms.EncodeRuntimeUpstreamRequest(inboundProtocol, upstreamProtocol, target, llmReq)
	if err != nil {
		raw, status := inboundAdapter.EncodeError(err)
		return WriteRuntimeLLMEncodedError(c, raw, status)
	}
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
		return BridgeRuntimeLLMStreamResponse(c, resp, inboundProtocol, upstreamProtocol, llms.NormalizeProviderType(target.Provider.ProviderType), target.Model.Name)
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

func (h runtimeLLMHandler) proxyTransparent(c echo.Context, upstreamEndpoint string, body []byte, target llms.ResolvedTarget, upstreamProtocol protocolbridge.Protocol) error {
	upstreamReq, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, upstreamEndpoint, bytes.NewReader(body))
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
			return BridgeRuntimeLLMStreamResponse(c, resp, protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIResponses, llms.ProviderFamilyOpenAI, target.Model.Name)
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

func BridgeRuntimeLLMStreamResponse(c echo.Context, resp *http.Response, inboundProtocol, upstreamProtocol protocolbridge.Protocol, upstreamFamily, model string) error {
	decoder, encoder, err := llms.RuntimeStreamBridge(inboundProtocol, upstreamProtocol, upstreamFamily, model)
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
