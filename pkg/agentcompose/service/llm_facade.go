package agentcompose

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
	"github.com/labstack/echo/v4"
)

const runtimeLLMFacadePrefix = "/api/runtime/sessions/"

func IsRuntimeLLMFacadeRequest(r *http.Request) bool {
	if r == nil || r.Method != http.MethodPost {
		return false
	}
	path := r.URL.Path
	if !strings.HasPrefix(path, runtimeLLMFacadePrefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, runtimeLLMFacadePrefix), "/")
	if len(parts) < 5 || parts[0] == "" || parts[1] != "llm" {
		return false
	}
	switch {
	case len(parts) == 5 && parts[2] == "openai" && parts[3] == "v1" && parts[4] == "responses":
		return true
	case len(parts) == 6 && parts[2] == "openai" && parts[3] == "v1" && parts[4] == "chat" && parts[5] == "completions":
		return true
	case len(parts) == 5 && parts[2] == "anthropic" && parts[3] == "v1" && parts[4] == "messages":
		return true
	default:
		return false
	}
}

func registerRuntimeLLMFacadeRoutes(app *echo.Echo, service *Service) {
	app.POST("/api/runtime/sessions/:session_id/llm/openai/v1/responses", service.handleRuntimeLLMResponses)
	app.POST("/api/runtime/sessions/:session_id/llm/openai/v1/chat/completions", service.handleRuntimeLLMChatCompletions)
	app.POST("/api/runtime/sessions/:session_id/llm/anthropic/v1/messages", service.handleRuntimeLLMAnthropicMessages)
}

func (s *Service) handleRuntimeLLMResponses(c echo.Context) error {
	return s.handleRuntimeLLM(c, protocolbridge.ProtocolOpenAIResponses, llms.APIProtocolResponses)
}

func (s *Service) handleRuntimeLLMChatCompletions(c echo.Context) error {
	return s.handleRuntimeLLM(c, protocolbridge.ProtocolOpenAIChat, llms.APIProtocolChatCompletions)
}

func (s *Service) handleRuntimeLLMAnthropicMessages(c echo.Context) error {
	return s.handleRuntimeLLM(c, protocolbridge.ProtocolAnthropicMessages, llms.APIProtocolMessages)
}

func (s *Service) handleRuntimeLLM(c echo.Context, inboundProtocol protocolbridge.Protocol, facadeWireAPI string) error {
	sessionID := strings.TrimSpace(c.Param("session_id"))
	rawToken := llms.RuntimeFacadeToken(c.Request().Header)
	if sessionID == "" || rawToken == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "llm facade token is required"})
	}
	token, err := s.configDB.GetLLMFacadeToken(c.Request().Context(), rawToken)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid llm facade token"})
	}
	now := time.Now().UTC()
	if token.SessionID != sessionID || !token.RevokedAt.IsZero() || (!token.ExpiresAt.IsZero() && now.After(token.ExpiresAt)) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "llm facade token is not valid for this session"})
	}
	if token.WireAPI != "" && llms.NormalizeWireAPI(token.WireAPI) != llms.NormalizeWireAPI(facadeWireAPI) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "llm facade token wire api mismatch"})
	}
	session, err := s.store.GetSession(c.Request().Context(), sessionID)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "session is not available"})
	}
	if session.Summary.VMStatus == domain.VMStatusStopped || session.Summary.VMStatus == domain.VMStatusFailed {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "session is not running"})
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
		return writeRuntimeLLMEncodedError(c, raw, status)
	}
	model := strings.TrimSpace(llmReq.Model)
	if model == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "llm model is required"})
	}
	if token.Model != "" && model != "" && token.Model != model {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "llm facade token model mismatch"})
	}
	target, err := resolveRuntimeLLMTarget(c.Request().Context(), s.config, s.configDB, firstNonEmpty(token.Model, model), token.ProviderID)
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
			return writeRuntimeLLMEncodedError(c, raw, status)
		}
		return s.proxyRuntimeLLMTransparent(c, upstreamEndpoint, upstreamBody, target, upstreamProtocol)
	}
	upstreamBody, err := llms.EncodeRuntimeUpstreamRequest(inboundProtocol, upstreamProtocol, target, llmReq)
	if err != nil {
		raw, status := inboundAdapter.EncodeError(err)
		return writeRuntimeLLMEncodedError(c, raw, status)
	}
	upstreamReq, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, upstreamEndpoint, bytes.NewReader(upstreamBody))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create upstream llm request failed"})
	}
	llms.CopyRuntimeHeaders(upstreamReq.Header, c.Request().Header)
	llms.ApplyForwardHeaders(upstreamReq.Header, target.Headers)
	resp, err := s.llm.client.Do(upstreamReq)
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
		return bridgeRuntimeLLMStreamResponse(c, resp, inboundProtocol, upstreamProtocol, llms.NormalizeProviderType(target.Provider.ProviderType), target.Model.Name)
	}
	upstreamRespBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "read upstream llm response failed"})
	}
	clientBody, err := llms.EncodeRuntimeClientResponse(inboundProtocol, upstreamProtocol, target, upstreamRespBody)
	if err != nil {
		raw, status := inboundAdapter.EncodeError(err)
		return writeRuntimeLLMEncodedError(c, raw, status)
	}
	llms.CopyRuntimeResponseHeaders(c.Response().Header(), resp.Header)
	c.Response().Header().Set("Content-Type", "application/json")
	c.Response().Header().Del("Content-Length")
	c.Response().WriteHeader(resp.StatusCode)
	_, err = c.Response().Writer.Write(clientBody)
	return err
}

func (s *Service) proxyRuntimeLLMTransparent(c echo.Context, upstreamEndpoint string, body []byte, target llms.ResolvedTarget, upstreamProtocol protocolbridge.Protocol) error {
	upstreamReq, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, upstreamEndpoint, bytes.NewReader(body))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create upstream llm request failed"})
	}
	llms.CopyRuntimeHeaders(upstreamReq.Header, c.Request().Header)
	llms.ApplyForwardHeaders(upstreamReq.Header, target.Headers)
	resp, err := s.llm.client.Do(upstreamReq)
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "call upstream llm failed"})
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && llms.UseGenericResponsesTextParts(target, upstreamProtocol) {
		if llms.RuntimeResponseShouldFlush(resp.Header) {
			return bridgeRuntimeLLMStreamResponse(c, resp, protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIResponses, llms.ProviderFamilyOpenAI, target.Model.Name)
		}
		upstreamRespBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			return c.JSON(http.StatusBadGateway, map[string]string{"error": "read upstream llm response failed"})
		}
		clientBody, err := llms.EncodeRuntimeClientResponse(protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIChat, target, upstreamRespBody)
		if err != nil {
			adapter := protocolbridge.NewOpenAIResponsesAdapter()
			raw, status := adapter.EncodeError(err)
			return writeRuntimeLLMEncodedError(c, raw, status)
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

func writeRuntimeLLMEncodedError(c echo.Context, raw []byte, status int) error {
	if status == 0 {
		status = http.StatusBadRequest
	}
	return c.Blob(status, "application/json", raw)
}

func bridgeRuntimeLLMStreamResponse(c echo.Context, resp *http.Response, inboundProtocol, upstreamProtocol protocolbridge.Protocol, upstreamFamily, model string) error {
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
