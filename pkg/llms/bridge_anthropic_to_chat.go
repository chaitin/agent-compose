package llms

import (
	"fmt"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
)

// anthropicToChatBridge serves an Anthropic-messages agent against an OpenAI
// chat-completions upstream.
//
// This is the daemon's composition of a pairing the protocol library cannot
// express. NewCrossFamilyBridge selects by upstream *family*, and an Anthropic
// inbound has two possible OpenAI targets, so its family lookup always returns
// the Responses bridge. The library's CrossFamilyBridge interface is public
// precisely so a caller that knows its upstream's protocol can supply the
// pairing it needs, which is what this does.
//
// Everything here delegates to the library's own adapters. The inbound request
// has already been decoded into the neutral LLMRequest by the agent's dialect,
// and the chat adapter is the authority on the chat wire format, so this adds no
// second codec — only the pairing, which is the daemon's own routing decision.
// See docs/design/llm_model_routing_redesign.md, phase P0, for the exit path:
// when the library publishes a protocol-precise constructor of its own, delete
// this file and call that instead.
type anthropicToChatBridge struct {
	upstream protocolbridge.OpenAIChatAdapter
}

func (b anthropicToChatBridge) InboundProtocol() protocolbridge.Protocol {
	return protocolbridge.ProtocolAnthropicMessages
}

func (b anthropicToChatBridge) UpstreamProtocol() protocolbridge.Protocol {
	return protocolbridge.ProtocolOpenAIChat
}

func (b anthropicToChatBridge) EncodeUpstreamRequest(req *protocolbridge.LLMRequest, opts protocolbridge.EncodeRequestOptions) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("encode anthropic to openai chat request: nil request")
	}
	return b.upstream.EncodeRequest(req, opts)
}

func (b anthropicToChatBridge) DecodeUpstreamResponse(raw []byte) (*protocolbridge.LLMResponse, error) {
	resp, err := b.upstream.DecodeResponse(raw)
	if err != nil {
		return nil, err
	}
	resp.Protocol = protocolbridge.ProtocolOpenAIChat
	return resp, nil
}

func (b anthropicToChatBridge) NewStreamDecoder(opts protocolbridge.StreamDecodeOptions) (protocolbridge.StreamDecoder, error) {
	return b.upstream.NewStreamDecoder(opts)
}

func (b anthropicToChatBridge) NewStreamEncoder(opts protocolbridge.StreamEncodeOptions) (protocolbridge.StreamEncoder, error) {
	encoder, err := protocolbridge.NewAnthropicMessagesAdapter().NewStreamEncoder(opts)
	if err != nil {
		return nil, err
	}
	return &anthropicStreamEncoderForChatUpstream{inbound: encoder}, nil
}

// anthropicStreamEncoderForChatUpstream writes neutral stream parts as Anthropic
// SSE for a chat-completions upstream.
//
// The usage has to be rebased on the way out, and it is the one piece of
// protocol knowledge that cannot be delegated. The chat decoder reports prompt
// tokens the way OpenAI does — a total, with the cached portion broken out
// separately — while Anthropic expects input_tokens to exclude what was served
// from cache and reports the cached count in cache_read_input_tokens. Passing
// the chat numbers through unchanged would count every cached token twice.
//
// The library applies the mirror of this rule in its own Responses bridges; it
// keeps the helper unexported, so the arithmetic is restated here rather than
// shared. That duplication disappears with the rest of this file once the
// library exposes the pairing.
type anthropicStreamEncoderForChatUpstream struct {
	inbound protocolbridge.StreamEncoder
}

func (e *anthropicStreamEncoderForChatUpstream) Encode(part protocolbridge.StreamPart) ([]protocolbridge.RawStreamEvent, error) {
	switch part.Type {
	case protocolbridge.StreamStart, protocolbridge.StreamFinish, protocolbridge.StreamResponseMetadata:
		part.Usage = usageForAnthropicInbound(part.Usage)
	}
	return e.inbound.Encode(part)
}

func (e *anthropicStreamEncoderForChatUpstream) Close() ([]protocolbridge.RawStreamEvent, error) {
	return e.inbound.Close()
}

func (e *anthropicStreamEncoderForChatUpstream) EncodeError(err error) []protocolbridge.RawStreamEvent {
	return e.inbound.EncodeError(err)
}

// usageForAnthropicInbound converts OpenAI's token accounting into Anthropic's:
// input_tokens counts only what was not served from cache, and the cached count
// moves to cache_read_input_tokens.
func usageForAnthropicInbound(usage protocolbridge.Usage) protocolbridge.Usage {
	cached := tokenValue(usage.CachedInputTokens)
	cacheWrite := tokenValue(usage.CacheCreationInputTokens)
	input := tokenValue(usage.InputTokens) - cached - cacheWrite
	if input < 0 {
		input = 0
	}
	converted := usage
	converted.InputTokens = &input
	converted.CacheReadInputTokens = usage.CachedInputTokens
	return converted
}

func tokenValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
