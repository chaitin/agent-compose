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
	defaultTextType := "input_text"
	if genericTextParts {
		defaultTextType = "text"
	}
	textTypeJSON, _ := json.Marshal(defaultTextType)
	for _, item := range items {
		if normalizeRuntimeRawResponsesContent(item, textTypeJSON) {
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

func normalizeRuntimeRawResponsesContent(item map[string]json.RawMessage, textType []byte) bool {
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
			part["type"] = textType
			changed = true
			continue
		}
		if string(part["type"]) == `"output_text"` || (string(textType) == `"text"` && string(part["type"]) == `"input_text"`) {
			part["type"] = textType
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
