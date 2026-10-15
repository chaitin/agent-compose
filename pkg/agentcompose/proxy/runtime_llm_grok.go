package proxy

import (
	"io"
	"net/http"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"

	"github.com/chaitin/agent-compose/pkg/llms"
)

// rewriteGrokResponsesStream makes a streamed Grok Responses body read with
// zero fractions removed from its tool-call arguments. It is for every path
// that hands the stream to the protocol bridge: the bridge relays argument
// deltas as written, so the fix has to happen in Grok's own format before the
// bridge decodes it. A buffered body is rewritten before the bridge converts
// it (see rewriteGrokBufferedBody), because the bridge preserves a tool call's
// numbers as written. model is the one requested, as on the transparent path.
func rewriteGrokResponsesStream(resp *http.Response, model string, upstreamProtocol protocolbridge.Protocol) {
	if llms.GrokResponsesModel(model, upstreamProtocol) == "" {
		return
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{llms.GrokResponsesStream(resp.Body), resp.Body}
}

// rewriteGrokBufferedBody strips zero fractions from a buffered Grok Responses
// body before the protocol bridge re-encodes it for the guest. The bridge
// preserves a tool call's numbers as written, so every buffered path that
// hands the body to EncodeRuntimeClientResponse needs this rewrite; the
// streamed and transparent paths apply their own. A body that is not a Grok
// Responses reply is returned unchanged.
func rewriteGrokBufferedBody(model string, upstreamProtocol protocolbridge.Protocol, body []byte) []byte {
	if grokModel := llms.GrokResponsesModel(model, upstreamProtocol); grokModel != "" {
		return llms.RewriteGrokIntegralToolArgs(grokModel, body)
	}
	return body
}
