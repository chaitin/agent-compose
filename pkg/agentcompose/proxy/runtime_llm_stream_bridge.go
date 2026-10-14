package proxy

import (
	"net/http"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
	"github.com/labstack/echo/v4"

	"github.com/chaitin/agent-compose/pkg/llms"
)

// runtimeLLMStreamBridgeRequest bundles the protocol/model metadata
// BridgeRuntimeLLMStreamResponse needs to translate an upstream SSE stream
// into the inbound wire protocol.
type runtimeLLMStreamBridgeRequest struct {
	InboundProtocol  protocolbridge.Protocol
	UpstreamProtocol protocolbridge.Protocol
	Model            string
}

func BridgeRuntimeLLMStreamResponse(c echo.Context, resp *http.Response, req runtimeLLMStreamBridgeRequest) error {
	inboundProtocol := req.InboundProtocol
	decoder, encoder, err := llms.RuntimeStreamBridge(req.InboundProtocol, req.UpstreamProtocol, req.Model)
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
