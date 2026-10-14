package llms

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
)

// Grok writes whole numbers in tool-call arguments as integral floats
// ("timeout_ms":66121.0, "yield_time_ms":2500.0). Codex deserializes those
// fields as integers and rejects the whole call. JSON has one number type,
// so a zero fraction is the same value as an integer. Drop that fraction
// from a Grok reply's function_call arguments before the guest sees them.
// Other fractions, exponents, strings, and other models stay as written.

func GrokModel(id string) bool {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return strings.HasPrefix(strings.ToLower(id), "grok")
}

// RewriteGrokIntegralToolArgs drops zero fractions from function_call
// arguments in a non-streaming Responses body. A body that is not a Grok
// reply, or that does not change, is returned as-is.
func RewriteGrokIntegralToolArgs(model string, body []byte) []byte {
	if !GrokModel(model) || !bytes.Contains(body, []byte("function_call")) || !bytes.Contains(body, []byte(".")) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var ev map[string]any
	if err := dec.Decode(&ev); err != nil || ev == nil {
		return body
	}
	if !rewriteResponsesIntegralArgs(ev) {
		return body
	}
	out, err := json.Marshal(ev)
	if err != nil {
		return body
	}
	return out
}

func rewriteResponsesIntegralArgs(ev map[string]any) bool {
	changed := false
	item := func(it map[string]any) {
		if it == nil || it["type"] != "function_call" {
			return
		}
		args, ok := it["arguments"].(string)
		if !ok {
			return
		}
		rewritten := WholeIntegralArgs(args)
		if rewritten == args {
			return
		}
		it["arguments"] = rewritten
		changed = true
	}
	if it, _ := ev["item"].(map[string]any); it != nil {
		item(it)
	}
	res, _ := ev["response"].(map[string]any)
	if res == nil && ev["object"] == "response" {
		res = ev
	}
	if res == nil {
		return changed
	}
	output, _ := res["output"].([]any)
	for _, entry := range output {
		itemMap, _ := entry.(map[string]any)
		item(itemMap)
	}
	return changed
}

// WholeIntegralArgs drops a trailing zero fraction from every JSON number
// in arguments. 66121.0 becomes 66121. 1.5 and 2.5e3 stay as written.
func WholeIntegralArgs(arguments string) string {
	if !strings.Contains(arguments, ".") {
		return arguments
	}
	var rewriter integralFloatRewriter
	return rewriter.feed(arguments)
}

// integralFloatRewriter walks JSON text and holds only a fraction of zeros
// until the number ends. A later digit or exponent writes the fraction back.
type integralFloatRewriter struct {
	inString bool
	escaped  bool
	inNumber bool
	keep     bool
	zeros    int
}

func (r *integralFloatRewriter) feed(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if r.inNumber {
			switch {
			case r.zeros > 0 && c == '0':
				r.zeros++
				continue
			case r.zeros > 0 && ((c >= '1' && c <= '9') || c == 'e' || c == 'E'):
				b.WriteByte('.')
				b.WriteString(strings.Repeat("0", r.zeros-1))
				r.zeros = 0
				r.keep = true
				b.WriteByte(c)
				continue
			case r.zeros == 0 && c == '.' && !r.keep:
				r.zeros = 1
				continue
			case r.zeros == 0 && ((c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-'):
				if c == 'e' || c == 'E' || c == '.' {
					r.keep = true
				}
				b.WriteByte(c)
				continue
			}
			r.inNumber = false
			r.keep = false
			r.zeros = 0
		}
		switch {
		case r.inString:
			if r.escaped {
				r.escaped = false
			} else if c == '\\' {
				r.escaped = true
			} else if c == '"' {
				r.inString = false
			}
		case c == '"':
			r.inString = true
		case c == '-' || (c >= '0' && c <= '9'):
			r.inNumber = true
		}
		b.WriteByte(c)
	}
	return b.String()
}

// CopyRuntimeResponseBodyForModel copies an upstream body to the guest.
// A streaming Grok Responses body has zero fractions removed from
// function_call argument deltas before each flush.
func CopyRuntimeResponseBodyForModel(dst io.Writer, resp *http.Response, model string) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	if !RuntimeResponseShouldFlush(resp.Header) || !GrokModel(model) {
		return CopyRuntimeResponseBody(dst, resp)
	}
	return copyGrokResponsesStream(dst, resp.Body)
}

func copyGrokResponsesStream(dst io.Writer, src io.Reader) error {
	flusher, _ := dst.(http.Flusher)
	stream := GrokResponsesStream(src)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// GrokResponsesStream reads a streaming Grok Responses body with zero
// fractions removed from its function_call arguments. The bridged proxy path
// reads through it before the protocol bridge decodes the stream, so a guest
// on any inbound protocol receives the same whole numbers a transparent
// Responses guest does.
func GrokResponsesStream(src io.Reader) io.Reader {
	return &grokResponsesStreamReader{src: src, buf: make([]byte, 32*1024)}
}

// grokResponsesStreamReader rewrites each complete SSE event as soon as it
// arrives and holds only the incomplete tail, so a reader is never kept
// waiting for an event the upstream has already finished.
type grokResponsesStreamReader struct {
	src     io.Reader
	buf     []byte
	pending []byte
	out     []byte
	calls   grokArgStreams
	err     error
}

func (r *grokResponsesStreamReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		n, err := r.src.Read(r.buf)
		if n > 0 {
			r.pending = append(r.pending, r.buf[:n]...)
			out, rest := r.calls.rewriteSSEPrefix(r.pending)
			r.out = out
			r.pending = append([]byte(nil), rest...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				r.out = append(r.out, r.calls.rewriteSSETail(r.pending)...)
				r.pending = nil
			}
			r.err = err
		}
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

// grokArgStreams keeps one number rewriter per streamed function call.
// A zero fraction split across deltas ("66121." then "0") stays held until
// the number ends, so the joined arguments are the same as a whole call.
type grokArgStreams struct {
	calls map[string]*integralFloatRewriter
}

func (s *grokArgStreams) rewriter(key string) *integralFloatRewriter {
	if s.calls == nil {
		s.calls = map[string]*integralFloatRewriter{}
	}
	rewriter := s.calls[key]
	if rewriter == nil {
		rewriter = &integralFloatRewriter{}
		s.calls[key] = rewriter
	}
	return rewriter
}

// rewriteSSEPrefix rewrites every complete SSE event in pending and
// returns the incomplete tail unchanged.
func (s *grokArgStreams) rewriteSSEPrefix(pending []byte) (out, rest []byte) {
	for {
		end := bytes.Index(pending, []byte("\n\n"))
		crlf := bytes.Index(pending, []byte("\r\n\r\n"))
		sepLen := 2
		if crlf >= 0 && (end < 0 || crlf < end) {
			end = crlf
			sepLen = 4
		}
		if end < 0 {
			return out, pending
		}
		event := pending[:end]
		pending = pending[end+sepLen:]
		out = append(out, s.rewriteSSEEvent(event)...)
		out = append(out, []byte("\n\n")...)
	}
}

func (s *grokArgStreams) rewriteSSETail(pending []byte) []byte {
	if len(bytes.TrimSpace(pending)) == 0 {
		return pending
	}
	rewritten := s.rewriteSSEEvent(bytes.TrimRight(pending, "\r\n"))
	if bytes.HasSuffix(pending, []byte("\n")) {
		return append(rewritten, '\n')
	}
	return rewritten
}

func (s *grokArgStreams) rewriteSSEEvent(event []byte) []byte {
	if !bytes.Contains(event, []byte("function_call")) {
		return event
	}
	lines := bytes.Split(event, []byte("\n"))
	changed := false
	for i, line := range lines {
		body := bytes.TrimRight(line, "\r")
		data, ok := bytes.CutPrefix(body, []byte("data:"))
		if !ok {
			continue
		}
		if len(data) > 0 && data[0] == ' ' {
			data = data[1:]
		}
		rewritten, ok := s.rewriteResponsesEventJSON(data)
		if !ok {
			continue
		}
		lines[i] = append([]byte("data: "), rewritten...)
		if bytes.HasSuffix(line, []byte("\r")) {
			lines[i] = append(lines[i], '\r')
		}
		changed = true
	}
	if !changed {
		return event
	}
	return bytes.Join(lines, []byte("\n"))
}

func (s *grokArgStreams) rewriteResponsesEventJSON(data []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var ev map[string]any
	if err := dec.Decode(&ev); err != nil || ev == nil {
		return nil, false
	}
	var changed bool
	switch ev["type"] {
	case "response.function_call_arguments.delta":
		delta, ok := ev["delta"].(string)
		if !ok {
			return nil, false
		}
		rewritten := s.rewriter(grokCallKey(ev)).feed(delta)
		if rewritten == delta {
			return nil, false
		}
		ev["delta"] = rewritten
		changed = true
	case "response.function_call_arguments.done":
		delete(s.calls, grokCallKey(ev))
		args, ok := ev["arguments"].(string)
		if !ok {
			return nil, false
		}
		rewritten := WholeIntegralArgs(args)
		if rewritten == args {
			return nil, false
		}
		ev["arguments"] = rewritten
		changed = true
	default:
		changed = rewriteResponsesIntegralArgs(ev)
	}
	if !changed {
		return nil, false
	}
	out, err := json.Marshal(ev)
	if err != nil {
		return nil, false
	}
	return out, true
}

func grokCallKey(ev map[string]any) string {
	if id, _ := ev["item_id"].(string); id != "" {
		return id
	}
	return fmt.Sprint(ev["output_index"])
}

// GrokResponsesModel is the model name to rewrite when a transparent
// Responses proxy is serving Grok. An empty result means leave the body
// unchanged.
func GrokResponsesModel(model string, upstreamProtocol protocolbridge.Protocol) string {
	if upstreamProtocol != protocolbridge.ProtocolOpenAIResponses || !GrokModel(model) {
		return ""
	}
	return model
}
