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
// bridge decodes it. A buffered body needs nothing, because the bridge parses
// its arguments and re-encodes the numbers whole. model is the one requested,
// as on the transparent path.
func rewriteGrokResponsesStream(resp *http.Response, model string, upstreamProtocol protocolbridge.Protocol) {
	if llms.GrokResponsesModel(model, upstreamProtocol) == "" {
		return
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{llms.GrokResponsesStream(resp.Body), resp.Body}
}
