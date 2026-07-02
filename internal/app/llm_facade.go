package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	return s.handleRuntimeLLM(c, protocolbridge.ProtocolOpenAIResponses, llmAPIProtocolResponses)
}

func (s *Service) handleRuntimeLLMChatCompletions(c echo.Context) error {
	return s.handleRuntimeLLM(c, protocolbridge.ProtocolOpenAIChat, llmAPIProtocolChatCompletions)
}

func (s *Service) handleRuntimeLLMAnthropicMessages(c echo.Context) error {
	return s.handleRuntimeLLM(c, protocolbridge.ProtocolAnthropicMessages, llmAPIProtocolMessages)
}

func (s *Service) handleRuntimeLLM(c echo.Context, inboundProtocol protocolbridge.Protocol, facadeWireAPI string) error {
	sessionID := strings.TrimSpace(c.Param("session_id"))
	rawToken := runtimeLLMFacadeToken(c.Request().Header)
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
	if token.WireAPI != "" && normalizeLLMWireAPI(token.WireAPI) != normalizeLLMWireAPI(facadeWireAPI) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "llm facade token wire api mismatch"})
	}
	session, err := s.store.GetSession(c.Request().Context(), sessionID)
	if err != nil {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "session is not available"})
	}
	if session.Summary.VMStatus == VMStatusStopped || session.Summary.VMStatus == VMStatusFailed {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "session is not running"})
	}
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, 64<<20))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "read llm request failed"})
	}
	inboundAdapter, err := llmProtocolAdapter(inboundProtocol)
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
	upstreamProtocol, upstreamEndpoint, err := llmUpstreamProtocolAndEndpoint(target)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if inboundProtocol == upstreamProtocol {
		upstreamBody, err := rewriteRuntimeLLMRequestForUpstream(body, target, upstreamProtocol)
		if err != nil {
			raw, status := inboundAdapter.EncodeError(err)
			return writeRuntimeLLMEncodedError(c, raw, status)
		}
		return s.proxyRuntimeLLMTransparent(c, upstreamEndpoint, upstreamBody, target, upstreamProtocol)
	}
	upstreamBody, err := encodeRuntimeLLMUpstreamRequest(inboundProtocol, upstreamProtocol, target, llmReq)
	if err != nil {
		raw, status := inboundAdapter.EncodeError(err)
		return writeRuntimeLLMEncodedError(c, raw, status)
	}
	upstreamReq, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, upstreamEndpoint, bytes.NewReader(upstreamBody))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create upstream llm request failed"})
	}
	copyRuntimeLLMHeaders(upstreamReq.Header, c.Request().Header)
	applyLLMForwardHeaders(upstreamReq.Header, target.Headers)
	resp, err := s.llm.HTTPClient().Do(upstreamReq)
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "call upstream llm failed"})
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		copyRuntimeLLMResponseHeaders(c.Response().Header(), resp.Header)
		c.Response().WriteHeader(resp.StatusCode)
		if err := copyRuntimeLLMResponseBody(c.Response().Writer, resp); err != nil && !errors.Is(err, http.ErrAbortHandler) {
			return err
		}
		return nil
	}
	if runtimeLLMResponseShouldFlush(resp.Header) {
		return bridgeRuntimeLLMStreamResponse(c, resp, inboundProtocol, upstreamProtocol, normalizeLLMProviderType(target.Provider.ProviderType), target.Model.Name)
	}
	upstreamRespBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "read upstream llm response failed"})
	}
	clientBody, err := encodeRuntimeLLMClientResponse(inboundProtocol, upstreamProtocol, target, upstreamRespBody)
	if err != nil {
		raw, status := inboundAdapter.EncodeError(err)
		return writeRuntimeLLMEncodedError(c, raw, status)
	}
	copyRuntimeLLMResponseHeaders(c.Response().Header(), resp.Header)
	c.Response().Header().Set("Content-Type", "application/json")
	c.Response().Header().Del("Content-Length")
	c.Response().WriteHeader(resp.StatusCode)
	_, err = c.Response().Writer.Write(clientBody)
	return err
}

func (s *Service) proxyRuntimeLLMTransparent(c echo.Context, upstreamEndpoint string, body []byte, target LLMResolvedTarget, upstreamProtocol protocolbridge.Protocol) error {
	upstreamReq, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, upstreamEndpoint, bytes.NewReader(body))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create upstream llm request failed"})
	}
	copyRuntimeLLMHeaders(upstreamReq.Header, c.Request().Header)
	applyLLMForwardHeaders(upstreamReq.Header, target.Headers)
	resp, err := s.llm.HTTPClient().Do(upstreamReq)
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "call upstream llm failed"})
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && runtimeLLMUseGenericResponsesTextParts(target, upstreamProtocol) {
		if runtimeLLMResponseShouldFlush(resp.Header) {
			return bridgeRuntimeLLMStreamResponse(c, resp, protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIResponses, llmProviderFamilyOpenAI, target.Model.Name)
		}
		upstreamRespBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			return c.JSON(http.StatusBadGateway, map[string]string{"error": "read upstream llm response failed"})
		}
		clientBody, err := encodeRuntimeLLMClientResponse(protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIChat, target, upstreamRespBody)
		if err != nil {
			adapter := protocolbridge.NewOpenAIResponsesAdapter()
			raw, status := adapter.EncodeError(err)
			return writeRuntimeLLMEncodedError(c, raw, status)
		}
		copyRuntimeLLMResponseHeaders(c.Response().Header(), resp.Header)
		c.Response().Header().Set("Content-Type", "application/json")
		c.Response().Header().Del("Content-Length")
		c.Response().WriteHeader(resp.StatusCode)
		_, err = c.Response().Writer.Write(clientBody)
		return err
	}
	copyRuntimeLLMResponseHeaders(c.Response().Header(), resp.Header)
	c.Response().WriteHeader(resp.StatusCode)
	if err := copyRuntimeLLMResponseBody(c.Response().Writer, resp); err != nil && !errors.Is(err, http.ErrAbortHandler) {
		return err
	}
	return nil
}

func rewriteRuntimeLLMRequestForUpstream(body []byte, target LLMResolvedTarget, upstreamProtocol protocolbridge.Protocol) ([]byte, error) {
	model := strings.TrimSpace(target.Model.Name)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	changed := normalizeRuntimeLLMRawRequestForUpstream(payload, upstreamProtocol, runtimeLLMUseGenericResponsesTextParts(target, upstreamProtocol))
	var current string
	if model != "" {
		if err := json.Unmarshal(payload["model"], &current); err != nil || current != model {
			modelJSON, err := json.Marshal(model)
			if err != nil {
				return nil, err
			}
			payload["model"] = modelJSON
			changed = true
		}
	}
	if !changed {
		return body, nil
	}
	return json.Marshal(payload)
}

func runtimeLLMUseGenericResponsesTextParts(target LLMResolvedTarget, upstreamProtocol protocolbridge.Protocol) bool {
	if upstreamProtocol != protocolbridge.ProtocolOpenAIResponses {
		return false
	}
	return target.Provider.UseGenericResponsesTextParts
}

func normalizeRuntimeLLMRawRequestForUpstream(payload map[string]json.RawMessage, upstreamProtocol protocolbridge.Protocol, genericResponsesTextParts bool) bool {
	switch upstreamProtocol {
	case protocolbridge.ProtocolOpenAIResponses:
		return normalizeRuntimeLLMRawResponsesInput(payload, genericResponsesTextParts)
	case protocolbridge.ProtocolOpenAIChat:
		return normalizeRuntimeLLMRawRoleItems(payload, "messages")
	default:
		return false
	}
}

func normalizeRuntimeLLMRawResponsesInput(payload map[string]json.RawMessage, genericTextParts bool) bool {
	raw := payload["input"]
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return false
	}
	var changed bool
	defaultTextType := "input_text"
	if genericTextParts {
		defaultTextType = "text"
	}
	defaultTextTypeJSON, _ := json.Marshal(defaultTextType)
	genericTextTypeJSON, _ := json.Marshal("text")
	for _, item := range items {
		if normalizeRuntimeLLMRawResponsesContent(item, defaultTextTypeJSON, genericTextTypeJSON, genericTextParts) {
			changed = true
		}
	}
	if !changed {
		return false
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return false
	}
	payload["input"] = encoded
	return true
}

func normalizeRuntimeLLMRawResponsesContent(item map[string]json.RawMessage, defaultTextType, genericTextType []byte, genericTextParts bool) bool {
	raw := item["content"]
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return false
	}
	var changed bool
	for _, part := range parts {
		if len(part["text"]) == 0 || string(part["text"]) == "null" {
			continue
		}
		if len(part["type"]) == 0 || string(part["type"]) == "null" {
			part["type"] = defaultTextType
			changed = true
			continue
		}
		if genericTextParts && (string(part["type"]) == `"input_text"` || string(part["type"]) == `"output_text"`) {
			part["type"] = genericTextType
			changed = true
		}
	}
	if !changed {
		return false
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		return false
	}
	item["content"] = encoded
	return true
}

func normalizeRuntimeLLMRawRoleItems(payload map[string]json.RawMessage, field string) bool {
	raw := payload[field]
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return false
	}
	var changed bool
	systemRole, _ := json.Marshal(string(protocolbridge.RoleSystem))
	for _, item := range items {
		var role string
		if err := json.Unmarshal(item["role"], &role); err == nil && role == string(protocolbridge.RoleDeveloper) {
			item["role"] = systemRole
			changed = true
		}
	}
	if !changed {
		return false
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return false
	}
	payload[field] = encoded
	return true
}

func llmProtocolAdapter(protocol protocolbridge.Protocol) (protocolbridge.Adapter, error) {
	switch protocol {
	case protocolbridge.ProtocolOpenAIResponses:
		return protocolbridge.NewOpenAIResponsesAdapter(), nil
	case protocolbridge.ProtocolOpenAIChat:
		return protocolbridge.NewOpenAIChatAdapter(), nil
	case protocolbridge.ProtocolAnthropicMessages:
		return protocolbridge.NewAnthropicMessagesAdapter(), nil
	default:
		return nil, fmt.Errorf("unsupported llm protocol %q", protocol)
	}
}

func llmUpstreamProtocolAndEndpoint(target LLMResolvedTarget) (protocolbridge.Protocol, string, error) {
	switch normalizeLLMProviderType(target.Provider.ProviderType) {
	case llmProviderFamilyAnthropic:
		return protocolbridge.ProtocolAnthropicMessages, llmEndpointForProvider(target.Provider, llmAPIProtocolMessages), nil
	case llmProviderFamilyOpenAI:
		switch normalizeLLMWireAPI(target.WireAPI) {
		case llmAPIProtocolChatCompletions:
			return protocolbridge.ProtocolOpenAIChat, llmEndpointForProvider(target.Provider, llmAPIProtocolChatCompletions), nil
		case llmAPIProtocolResponses:
			return protocolbridge.ProtocolOpenAIResponses, llmEndpointForProvider(target.Provider, llmAPIProtocolResponses), nil
		default:
			return "", "", fmt.Errorf("unsupported openai wire api %q", target.WireAPI)
		}
	default:
		return "", "", fmt.Errorf("unsupported llm provider family %q", target.Provider.ProviderType)
	}
}

func encodeRuntimeLLMUpstreamRequest(inboundProtocol, upstreamProtocol protocolbridge.Protocol, target LLMResolvedTarget, req *protocolbridge.LLMRequest) ([]byte, error) {
	if inboundProtocol == upstreamProtocol {
		adapter, err := llmProtocolAdapter(upstreamProtocol)
		if err != nil {
			return nil, err
		}
		return adapter.EncodeRequest(normalizeRuntimeLLMRequestForUpstream(req, upstreamProtocol), protocolbridge.EncodeRequestOptions{Model: target.Model.Name})
	}
	if runtimeLLMProtocolsShareFamily(inboundProtocol, upstreamProtocol) {
		adapter, err := llmProtocolAdapter(upstreamProtocol)
		if err != nil {
			return nil, err
		}
		return adapter.EncodeRequest(normalizeRuntimeLLMRequestForUpstream(req, upstreamProtocol), protocolbridge.EncodeRequestOptions{Model: target.Model.Name})
	}
	bridge, ok := protocolbridge.NewCrossFamilyBridge(inboundProtocol, normalizeLLMProviderType(target.Provider.ProviderType))
	if !ok || bridge.UpstreamProtocol() != upstreamProtocol {
		return nil, fmt.Errorf("unsupported llm protocol bridge from %q to %q", inboundProtocol, upstreamProtocol)
	}
	return bridge.EncodeUpstreamRequest(req, protocolbridge.EncodeRequestOptions{Model: target.Model.Name})
}

func normalizeRuntimeLLMRequestForUpstream(req *protocolbridge.LLMRequest, upstreamProtocol protocolbridge.Protocol) *protocolbridge.LLMRequest {
	if req == nil || upstreamProtocol != protocolbridge.ProtocolOpenAIChat {
		return req
	}
	var changed bool
	prompt := make([]protocolbridge.Message, len(req.Prompt))
	copy(prompt, req.Prompt)
	for i := range prompt {
		if prompt[i].Role == protocolbridge.RoleDeveloper {
			prompt[i].Role = protocolbridge.RoleSystem
			changed = true
		}
	}
	if !changed {
		return req
	}
	normalized := *req
	normalized.Prompt = prompt
	return &normalized
}

func encodeRuntimeLLMClientResponse(inboundProtocol, upstreamProtocol protocolbridge.Protocol, target LLMResolvedTarget, upstreamBody []byte) ([]byte, error) {
	inboundAdapter, err := llmProtocolAdapter(inboundProtocol)
	if err != nil {
		return nil, err
	}
	var llmResp *protocolbridge.LLMResponse
	if inboundProtocol == upstreamProtocol {
		upstreamAdapter, err := llmProtocolAdapter(upstreamProtocol)
		if err != nil {
			return nil, err
		}
		llmResp, err = upstreamAdapter.DecodeResponse(upstreamBody)
		if err != nil {
			return nil, err
		}
	} else {
		if runtimeLLMProtocolsShareFamily(inboundProtocol, upstreamProtocol) {
			upstreamAdapter, err := llmProtocolAdapter(upstreamProtocol)
			if err != nil {
				return nil, err
			}
			llmResp, err = upstreamAdapter.DecodeResponse(upstreamBody)
			if err != nil {
				return nil, err
			}
		} else {
			bridge, ok := protocolbridge.NewCrossFamilyBridge(inboundProtocol, normalizeLLMProviderType(target.Provider.ProviderType))
			if !ok || bridge.UpstreamProtocol() != upstreamProtocol {
				return nil, fmt.Errorf("unsupported llm protocol bridge from %q to %q", inboundProtocol, upstreamProtocol)
			}
			llmResp, err = bridge.DecodeUpstreamResponse(upstreamBody)
			if err != nil {
				return nil, err
			}
		}
	}
	return inboundAdapter.EncodeResponse(llmResp, protocolbridge.EncodeResponseOptions{Model: target.Model.Name})
}

func runtimeLLMProtocolsShareFamily(left, right protocolbridge.Protocol) bool {
	return runtimeLLMProtocolFamily(left) != "" && runtimeLLMProtocolFamily(left) == runtimeLLMProtocolFamily(right)
}

func runtimeLLMProtocolFamily(protocol protocolbridge.Protocol) string {
	switch protocol {
	case protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIChat:
		return llmProviderFamilyOpenAI
	case protocolbridge.ProtocolAnthropicMessages:
		return llmProviderFamilyAnthropic
	default:
		return ""
	}
}

func writeRuntimeLLMEncodedError(c echo.Context, raw []byte, status int) error {
	if status == 0 {
		status = http.StatusBadRequest
	}
	return c.Blob(status, "application/json", raw)
}

func bridgeRuntimeLLMStreamResponse(c echo.Context, resp *http.Response, inboundProtocol, upstreamProtocol protocolbridge.Protocol, upstreamFamily, model string) error {
	decoder, encoder, err := runtimeLLMStreamBridge(inboundProtocol, upstreamProtocol, upstreamFamily, model)
	if err != nil {
		return err
	}
	copyRuntimeLLMResponseHeaders(c.Response().Header(), resp.Header)
	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Del("Content-Length")
	c.Response().Header().Del("Content-Encoding")
	c.Response().WriteHeader(resp.StatusCode)
	flusher, _ := c.Response().Writer.(http.Flusher)
	writeEvents := func(events []protocolbridge.RawStreamEvent) error {
		for _, event := range events {
			if err := writeRawSSEEvent(c.Response().Writer, event); err != nil {
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
	err = readRawSSEEvents(resp.Body, func(event protocolbridge.RawStreamEvent) error {
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

func runtimeLLMStreamBridge(inboundProtocol, upstreamProtocol protocolbridge.Protocol, upstreamFamily string, model string) (protocolbridge.StreamDecoder, protocolbridge.StreamEncoder, error) {
	if inboundProtocol == upstreamProtocol {
		adapter, err := llmProtocolAdapter(inboundProtocol)
		if err != nil {
			return nil, nil, err
		}
		decoder, err := adapter.NewStreamDecoder(protocolbridge.StreamDecodeOptions{})
		if err != nil {
			return nil, nil, err
		}
		encoder, err := adapter.NewStreamEncoder(protocolbridge.StreamEncodeOptions{Model: model})
		if err != nil {
			return nil, nil, err
		}
		return decoder, encoder, nil
	}
	if runtimeLLMProtocolsShareFamily(inboundProtocol, upstreamProtocol) {
		upstreamAdapter, err := llmProtocolAdapter(upstreamProtocol)
		if err != nil {
			return nil, nil, err
		}
		inboundAdapter, err := llmProtocolAdapter(inboundProtocol)
		if err != nil {
			return nil, nil, err
		}
		decoder, err := upstreamAdapter.NewStreamDecoder(protocolbridge.StreamDecodeOptions{})
		if err != nil {
			return nil, nil, err
		}
		encoder, err := inboundAdapter.NewStreamEncoder(protocolbridge.StreamEncodeOptions{Model: model})
		if err != nil {
			return nil, nil, err
		}
		return decoder, encoder, nil
	}
	bridge, ok := protocolbridge.NewCrossFamilyBridge(inboundProtocol, upstreamFamily)
	if !ok || bridge.UpstreamProtocol() != upstreamProtocol {
		return nil, nil, fmt.Errorf("unsupported llm stream bridge from %q to %q", inboundProtocol, upstreamProtocol)
	}
	decoder, err := bridge.NewStreamDecoder(protocolbridge.StreamDecodeOptions{})
	if err != nil {
		return nil, nil, err
	}
	encoder, err := bridge.NewStreamEncoder(protocolbridge.StreamEncodeOptions{Model: model})
	if err != nil {
		return nil, nil, err
	}
	return decoder, encoder, nil
}

func bearerToken(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 7 && strings.EqualFold(value[:7], "Bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func runtimeLLMFacadeToken(header http.Header) string {
	if token := bearerToken(header.Get("Authorization")); token != "" {
		return token
	}
	return strings.TrimSpace(header.Get("x-api-key"))
}

func copyRuntimeLLMHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		if forbiddenRuntimeLLMHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func copyRuntimeLLMResponseHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		if forbiddenRuntimeLLMResponseHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func forbiddenRuntimeLLMResponseHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "content-length", "content-encoding":
		return true
	default:
		return false
	}
}

func copyRuntimeLLMResponseBody(dst io.Writer, resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	if !runtimeLLMResponseShouldFlush(resp.Header) {
		_, err := io.Copy(dst, resp.Body)
		return err
	}
	flusher, ok := dst.(http.Flusher)
	if !ok {
		_, err := io.Copy(dst, resp.Body)
		return err
	}
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			flusher.Flush()
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func runtimeLLMResponseShouldFlush(header http.Header) bool {
	contentType := strings.ToLower(header.Get("Content-Type"))
	return strings.Contains(contentType, "text/event-stream")
}

func forbiddenRuntimeLLMHeader(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return true
	}
	if runtimeLLMHeaderNameContainsSensitiveToken(lower) {
		return true
	}
	switch lower {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "host", "content-length", "accept-encoding":
		return true
	default:
		return false
	}
}

func runtimeLLMHeaderNameContainsSensitiveToken(lower string) bool {
	parts := strings.FieldsFunc(lower, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	for _, part := range parts {
		switch part {
		case "token", "secret", "apikey", "auth":
			return true
		}
	}
	return strings.Contains(lower, "api-key")
}

func readRawSSEEvents(r io.Reader, handle func(protocolbridge.RawStreamEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var event protocolbridge.RawStreamEvent
	var data []string
	flush := func() error {
		if event.Event == "" && event.ID == "" && event.Retry == nil && len(data) == 0 {
			return nil
		}
		event.Data = []byte(strings.Join(data, "\n"))
		if err := handle(event); err != nil {
			return err
		}
		event = protocolbridge.RawStreamEvent{}
		data = nil
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if ok && strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		if !ok {
			field = line
			value = ""
		}
		switch field {
		case "event":
			event.Event = value
		case "data":
			data = append(data, value)
		case "id":
			event.ID = value
		case "retry":
			if retry, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				event.Retry = &retry
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func writeRawSSEEvent(w io.Writer, event protocolbridge.RawStreamEvent) error {
	if event.ID != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", event.ID); err != nil {
			return err
		}
	}
	if event.Event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event.Event); err != nil {
			return err
		}
	}
	for _, line := range strings.Split(string(event.Data), "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	if event.Retry != nil {
		if _, err := fmt.Fprintf(w, "retry: %d\n", *event.Retry); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}
