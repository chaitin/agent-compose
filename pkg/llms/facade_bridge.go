package llms

import (
	"encoding/json"
	"fmt"
	"strings"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
)

func RewriteRuntimeRequestForUpstream(body []byte, target ResolvedTarget, upstreamProtocol protocolbridge.Protocol) ([]byte, error) {
	model := strings.TrimSpace(target.Model.Name)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	changed := normalizeRuntimeRawRequestForUpstream(payload, upstreamProtocol, UseGenericResponsesTextParts(target, upstreamProtocol))
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

func normalizeRuntimeRawRequestForUpstream(payload map[string]json.RawMessage, upstreamProtocol protocolbridge.Protocol, genericResponsesTextParts bool) bool {
	switch upstreamProtocol {
	case protocolbridge.ProtocolOpenAIResponses:
		return normalizeRuntimeRawResponsesInput(payload, genericResponsesTextParts)
	case protocolbridge.ProtocolOpenAIChat:
		return normalizeRuntimeRawRoleItems(payload, "messages")
	default:
		return false
	}
}

func normalizeRuntimeRawResponsesInput(payload map[string]json.RawMessage, genericTextParts bool) bool {
	raw := payload["input"]
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return false
	}
	var changed bool
	for _, item := range items {
		if normalizeRuntimeRawResponsesContent(item, genericTextParts) {
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

func normalizeRuntimeRawResponsesContent(item map[string]json.RawMessage, genericTextParts bool) bool {
	raw := item["content"]
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return false
	}
	textType := "input_text"
	if genericTextParts {
		textType = "text"
	} else {
		var role string
		if err := json.Unmarshal(item["role"], &role); err == nil && role == string(protocolbridge.RoleAssistant) {
			textType = "output_text"
		}
	}
	textTypeJSON, err := json.Marshal(textType)
	if err != nil {
		return false
	}
	var changed bool
	for _, part := range parts {
		if len(part["text"]) == 0 || string(part["text"]) == "null" {
			continue
		}
		if len(part["type"]) == 0 || string(part["type"]) == "null" {
			part["type"] = textTypeJSON
			changed = true
			continue
		}
		var partType string
		if err := json.Unmarshal(part["type"], &partType); err == nil &&
			(partType == "input_text" || partType == "output_text") && partType != textType {
			part["type"] = textTypeJSON
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

func normalizeRuntimeRawRoleItems(payload map[string]json.RawMessage, field string) bool {
	raw := payload[field]
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return false
	}
	var changed bool
	systemRole, err := json.Marshal(string(protocolbridge.RoleSystem))
	if err != nil {
		return false
	}
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

func EncodeRuntimeUpstreamRequest(inboundProtocol, upstreamProtocol protocolbridge.Protocol, target ResolvedTarget, req *protocolbridge.LLMRequest) ([]byte, error) {
	if inboundProtocol == upstreamProtocol || ProtocolsShareFamily(inboundProtocol, upstreamProtocol) {
		adapter, err := ProtocolAdapter(upstreamProtocol)
		if err != nil {
			return nil, err
		}
		return adapter.EncodeRequest(normalizeRuntimeRequestForUpstream(req, upstreamProtocol), protocolbridge.EncodeRequestOptions{Model: target.Model.Name})
	}
	bridge, ok := protocolbridge.NewCrossFamilyBridge(inboundProtocol, NormalizeProviderType(target.Provider.ProviderType))
	if !ok || bridge.UpstreamProtocol() != upstreamProtocol {
		return nil, fmt.Errorf("unsupported llm protocol bridge from %q to %q", inboundProtocol, upstreamProtocol)
	}
	return bridge.EncodeUpstreamRequest(req, protocolbridge.EncodeRequestOptions{Model: target.Model.Name})
}

func normalizeRuntimeRequestForUpstream(req *protocolbridge.LLMRequest, upstreamProtocol protocolbridge.Protocol) *protocolbridge.LLMRequest {
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

func EncodeRuntimeClientResponse(inboundProtocol, upstreamProtocol protocolbridge.Protocol, target ResolvedTarget, upstreamBody []byte) ([]byte, error) {
	inboundAdapter, err := ProtocolAdapter(inboundProtocol)
	if err != nil {
		return nil, err
	}
	var llmResp *protocolbridge.LLMResponse
	if inboundProtocol == upstreamProtocol || ProtocolsShareFamily(inboundProtocol, upstreamProtocol) {
		upstreamAdapter, err := ProtocolAdapter(upstreamProtocol)
		if err != nil {
			return nil, err
		}
		llmResp, err = upstreamAdapter.DecodeResponse(upstreamBody)
		if err != nil {
			return nil, err
		}
	} else {
		bridge, ok := protocolbridge.NewCrossFamilyBridge(inboundProtocol, NormalizeProviderType(target.Provider.ProviderType))
		if !ok || bridge.UpstreamProtocol() != upstreamProtocol {
			return nil, fmt.Errorf("unsupported llm protocol bridge from %q to %q", inboundProtocol, upstreamProtocol)
		}
		llmResp, err = bridge.DecodeUpstreamResponse(upstreamBody)
		if err != nil {
			return nil, err
		}
	}
	return inboundAdapter.EncodeResponse(llmResp, protocolbridge.EncodeResponseOptions{Model: target.Model.Name})
}

func RuntimeStreamBridge(inboundProtocol, upstreamProtocol protocolbridge.Protocol, upstreamFamily string, model string) (protocolbridge.StreamDecoder, protocolbridge.StreamEncoder, error) {
	if inboundProtocol == upstreamProtocol {
		adapter, err := ProtocolAdapter(inboundProtocol)
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
	if ProtocolsShareFamily(inboundProtocol, upstreamProtocol) {
		upstreamAdapter, err := ProtocolAdapter(upstreamProtocol)
		if err != nil {
			return nil, nil, err
		}
		inboundAdapter, err := ProtocolAdapter(inboundProtocol)
		if err != nil {
			return nil, nil, err
		}
		decoder, err := upstreamAdapter.NewStreamDecoder(protocolbridge.StreamDecodeOptions{})
		if err != nil {
			return nil, nil, err
		}
		if requiresCompletedChatToolCalls(inboundProtocol, upstreamProtocol) {
			decoder = newCompletedToolCallStreamDecoder(decoder)
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
	if requiresCompletedChatToolCalls(inboundProtocol, upstreamProtocol) {
		decoder = newCompletedToolCallStreamDecoder(decoder)
	}
	encoder, err := bridge.NewStreamEncoder(protocolbridge.StreamEncodeOptions{Model: model})
	if err != nil {
		return nil, nil, err
	}
	return decoder, encoder, nil
}

func requiresCompletedChatToolCalls(inboundProtocol, upstreamProtocol protocolbridge.Protocol) bool {
	return inboundProtocol == protocolbridge.ProtocolOpenAIResponses && upstreamProtocol == protocolbridge.ProtocolOpenAIChat
}

// completedToolCallStreamDecoder buffers Chat tool-call fragments until the
// stream finishes. The Responses encoder needs a complete tool call to emit
// both function_call_arguments.done and output_item.done; Chat streams only
// provide finish_reason=tool_calls and do not send a separate tool-input-end
// event.
type completedToolCallStreamDecoder struct {
	inner   protocolbridge.StreamDecoder
	order   []string
	pending map[string]*completedToolCall
}

type completedToolCall struct {
	id         string
	toolCallID string
	toolName   string
	arguments  strings.Builder
}

func newCompletedToolCallStreamDecoder(inner protocolbridge.StreamDecoder) protocolbridge.StreamDecoder {
	return &completedToolCallStreamDecoder{
		inner:   inner,
		pending: map[string]*completedToolCall{},
	}
}

func (d *completedToolCallStreamDecoder) Decode(event protocolbridge.RawStreamEvent) ([]protocolbridge.StreamPart, error) {
	parts, err := d.inner.Decode(event)
	if err != nil {
		return nil, err
	}
	out := make([]protocolbridge.StreamPart, 0, len(parts)+1)
	for _, part := range parts {
		switch part.Type {
		case protocolbridge.StreamToolInputStart:
			d.start(part)
		case protocolbridge.StreamToolInputDelta:
			d.delta(part)
		case protocolbridge.StreamToolInputEnd:
			if toolCall := d.take(toolCallKey(part)); toolCall != nil {
				out = append(out, d.completedPart(toolCall))
			}
		case protocolbridge.StreamFinish:
			out = append(out, d.flush()...)
			out = append(out, part)
		default:
			out = append(out, part)
		}
	}
	return out, nil
}

func (d *completedToolCallStreamDecoder) Close() ([]protocolbridge.StreamPart, error) {
	parts, err := d.inner.Close()
	if err != nil {
		return nil, err
	}
	out := d.flush()
	out = append(out, parts...)
	return out, nil
}

func (d *completedToolCallStreamDecoder) start(part protocolbridge.StreamPart) {
	key := toolCallKey(part)
	if key == "" {
		return
	}
	toolCall, ok := d.pending[key]
	if !ok {
		toolCall = &completedToolCall{}
		d.pending[key] = toolCall
		d.order = append(d.order, key)
	}
	toolCall.id = firstNonEmptyString(part.ID, toolCall.id)
	toolCall.toolCallID = firstNonEmptyString(part.ToolCallID, toolCall.toolCallID)
	toolCall.toolName = firstNonEmptyString(part.ToolName, toolCall.toolName)
}

func (d *completedToolCallStreamDecoder) delta(part protocolbridge.StreamPart) {
	key := toolCallKey(part)
	if key == "" {
		return
	}
	toolCall, ok := d.pending[key]
	if !ok {
		toolCall = &completedToolCall{}
		d.pending[key] = toolCall
		d.order = append(d.order, key)
	}
	toolCall.id = firstNonEmptyString(part.ID, toolCall.id)
	toolCall.toolCallID = firstNonEmptyString(part.ToolCallID, toolCall.toolCallID)
	toolCall.toolName = firstNonEmptyString(part.ToolName, toolCall.toolName)
	toolCall.arguments.WriteString(part.Delta)
}

func (d *completedToolCallStreamDecoder) take(key string) *completedToolCall {
	if key == "" {
		return nil
	}
	toolCall := d.pending[key]
	if toolCall == nil {
		return nil
	}
	delete(d.pending, key)
	for index, current := range d.order {
		if current == key {
			d.order = append(d.order[:index], d.order[index+1:]...)
			break
		}
	}
	return toolCall
}

func (d *completedToolCallStreamDecoder) flush() []protocolbridge.StreamPart {
	parts := make([]protocolbridge.StreamPart, 0, len(d.order))
	for _, key := range d.order {
		if toolCall := d.pending[key]; toolCall != nil {
			parts = append(parts, d.completedPart(toolCall))
		}
	}
	d.order = nil
	d.pending = map[string]*completedToolCall{}
	return parts
}

func (d *completedToolCallStreamDecoder) completedPart(toolCall *completedToolCall) protocolbridge.StreamPart {
	arguments := toolCall.arguments.String()
	var input any
	if strings.TrimSpace(arguments) == "" {
		input = map[string]any{}
	} else if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		input = map[string]any{"_raw": arguments}
	}
	return protocolbridge.StreamPart{
		Type:       protocolbridge.StreamToolCall,
		ID:         toolCall.id,
		ToolCallID: toolCall.toolCallID,
		ToolName:   toolCall.toolName,
		Input:      input,
	}
}

func toolCallKey(part protocolbridge.StreamPart) string {
	return firstNonEmptyString(part.ID, part.ToolCallID)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
