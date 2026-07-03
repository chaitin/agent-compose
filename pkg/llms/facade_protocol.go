package llms

import (
	"fmt"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
)

func ProtocolAdapter(protocol protocolbridge.Protocol) (protocolbridge.Adapter, error) {
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

func UpstreamProtocolAndEndpoint(target ResolvedTarget) (protocolbridge.Protocol, string, error) {
	switch NormalizeProviderType(target.Provider.ProviderType) {
	case ProviderFamilyAnthropic:
		return protocolbridge.ProtocolAnthropicMessages, EndpointForProvider(target.Provider, APIProtocolMessages), nil
	case ProviderFamilyOpenAI:
		switch NormalizeWireAPI(target.WireAPI) {
		case APIProtocolChatCompletions:
			return protocolbridge.ProtocolOpenAIChat, EndpointForProvider(target.Provider, APIProtocolChatCompletions), nil
		case APIProtocolResponses:
			return protocolbridge.ProtocolOpenAIResponses, EndpointForProvider(target.Provider, APIProtocolResponses), nil
		default:
			return "", "", fmt.Errorf("unsupported openai wire api %q", target.WireAPI)
		}
	default:
		return "", "", fmt.Errorf("unsupported llm provider family %q", target.Provider.ProviderType)
	}
}

func UseGenericResponsesTextParts(target ResolvedTarget, upstreamProtocol protocolbridge.Protocol) bool {
	if upstreamProtocol != protocolbridge.ProtocolOpenAIResponses {
		return false
	}
	return target.Provider.UseGenericResponsesTextParts
}

func ProtocolsShareFamily(left, right protocolbridge.Protocol) bool {
	return ProtocolFamily(left) != "" && ProtocolFamily(left) == ProtocolFamily(right)
}

func ProtocolFamily(protocol protocolbridge.Protocol) string {
	switch protocol {
	case protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIChat:
		return ProviderFamilyOpenAI
	case protocolbridge.ProtocolAnthropicMessages:
		return ProviderFamilyAnthropic
	default:
		return ""
	}
}
