package chat

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/net/http2"
)

const (
	runService      = "/agentcompose.v2.RunService/"
	protocolVersion = "1"
	unaryMediaType  = "application/json"
	streamMediaType = "application/connect+json"

	// flagEndStream marks the envelope that closes a Connect stream. Its
	// payload carries the terminating error, if any.
	flagEndStream = 0b0000_0010
	// maxEnvelope bounds a single frame so a corrupt length cannot make the
	// client allocate without limit.
	maxEnvelope = 32 << 20
	// maxErrorBody bounds how much of a non-Connect error page (a proxy's HTML,
	// say) reaches an Error message.
	maxErrorBody = 4 << 10
)

type transport struct {
	baseURL   string
	token     TokenSource
	client    *http.Client
	userAgent string
}

// wireError is the Connect JSON error shape, used both for a unary response
// body and inside a stream's end-of-stream envelope.
type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type endStream struct {
	Error *wireError `json:"error"`
}

func (t *transport) request(ctx context.Context, op, procedure, mediaType string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+runService+procedure, body)
	if err != nil {
		return nil, &Error{Op: op, Message: err.Error()}
	}
	request.Header.Set("Content-Type", mediaType)
	request.Header.Set("Connect-Protocol-Version", protocolVersion)
	request.Header.Set("User-Agent", t.userAgent)
	if t.token != nil {
		token, tokenErr := t.token(ctx)
		if tokenErr != nil {
			return nil, &Error{Op: op, Message: "resolve token: " + tokenErr.Error()}
		}
		if token = strings.TrimSpace(token); token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}
	return request, nil
}

// unary sends one Connect JSON request and decodes its response.
func (t *transport) unary(ctx context.Context, op, procedure string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return &Error{Op: op, Message: "encode request: " + err.Error()}
	}
	request, err := t.request(ctx, op, procedure, unaryMediaType, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	response, err := t.client.Do(request)
	if err != nil {
		return unavailable(op, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxErrorBody))
		_ = response.Body.Close()
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return httpError(op, response)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return &Error{Op: op, Status: response.StatusCode, Message: "decode response: " + err.Error()}
	}
	return nil
}

func httpError(op string, response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	failure := &Error{Op: op, Status: response.StatusCode}
	var decoded wireError
	if json.Unmarshal(body, &decoded) == nil && strings.TrimSpace(decoded.Message) != "" {
		failure.Code = decoded.Code
		failure.Message = decoded.Message
		return failure
	}
	failure.Message = cmp.Or(strings.TrimSpace(string(body)), http.StatusText(response.StatusCode))
	return failure
}

// stream is one Connect bidirectional stream. Send is safe to call
// concurrently with recv; concurrent sends serialize.
type stream struct {
	op   string
	body *io.PipeWriter

	// ready closes once the response is known. The request is dispatched in
	// the background because a Connect handler does not write response headers
	// until it has received the first client message: waiting for headers
	// before sending the opening frame deadlocks both sides.
	ready    chan struct{}
	response *http.Response
	openErr  error

	sendMu sync.Mutex
	sent   bool

	header [5]byte
}

// openStream starts a bidirectional Connect stream. It returns before the
// server has responded; recv reports any failure to open. Connect requires
// HTTP/2 for bidirectional streams.
func (t *transport) openStream(ctx context.Context, op, procedure string) (*stream, error) {
	reader, writer := io.Pipe()
	request, err := t.request(ctx, op, procedure, streamMediaType, reader)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	opened := &stream{op: op, body: writer, ready: make(chan struct{})}
	go func() {
		defer close(opened.ready)
		// Any failure is also pushed into the pipe, so a send blocked writing
		// the opening frame fails instead of waiting for a reader that will
		// never arrive.
		fail := func(err error) {
			opened.openErr = err
			_ = writer.CloseWithError(err)
		}
		response, err := t.client.Do(request)
		if err != nil {
			fail(unavailable(op, err))
			return
		}
		switch {
		case response.StatusCode < 200 || response.StatusCode >= 300:
			failure := httpError(op, response)
			_ = response.Body.Close()
			fail(failure)
		case response.ProtoMajor < 2:
			_ = response.Body.Close()
			fail(&Error{
				Op:      op,
				Code:    "unavailable",
				Message: "conversations need HTTP/2; the daemon answered HTTP/1, which an intermediate proxy usually causes",
			})
		default:
			opened.response = response
		}
	}()
	return opened, nil
}

// send writes one message envelope.
func (s *stream) send(message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return &Error{Op: s.op, Message: "encode frame: " + err.Error()}
	}
	var header [5]byte
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))

	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sent {
		return ErrClosed
	}
	if _, err := s.body.Write(header[:]); err != nil {
		return unavailable(s.op, err)
	}
	if _, err := s.body.Write(payload); err != nil {
		return unavailable(s.op, err)
	}
	return nil
}

// closeSend half-closes the request body, telling the server no more frames
// are coming.
func (s *stream) closeSend() error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sent {
		return nil
	}
	s.sent = true
	return s.body.Close()
}

// recv decodes the next message envelope. It returns io.EOF once the server
// ends the stream without an error, and an [Error] when it ends with one or
// when the stream could not be opened.
func (s *stream) recv(out any) error {
	<-s.ready
	if s.openErr != nil {
		return s.openErr
	}
	for {
		if _, err := io.ReadFull(s.response.Body, s.header[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return io.EOF
			}
			return unavailable(s.op, err)
		}
		size := binary.BigEndian.Uint32(s.header[1:])
		if size > maxEnvelope {
			return &Error{Op: s.op, Message: fmt.Sprintf("frame of %d bytes exceeds the %d byte limit", size, maxEnvelope)}
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(s.response.Body, payload); err != nil {
			return unavailable(s.op, err)
		}
		if s.header[0]&flagEndStream != 0 {
			var terminal endStream
			if err := json.Unmarshal(payload, &terminal); err == nil && terminal.Error != nil {
				return &Error{Op: s.op, Code: terminal.Error.Code, Message: terminal.Error.Message}
			}
			return io.EOF
		}
		if s.header[0] != 0 {
			// A compressed frame; this client advertises no compression, so the
			// server should never send one. Skip rather than fail the turn.
			continue
		}
		if err := json.Unmarshal(payload, out); err != nil {
			return &Error{Op: s.op, Message: "decode frame: " + err.Error()}
		}
		return nil
	}
}

// close abandons the stream in both directions. It does not wait for the
// server, so a daemon that has stopped answering cannot hold Close up.
func (s *stream) close() error {
	err := s.closeSend()
	go func() {
		<-s.ready
		if s.response != nil {
			_ = s.response.Body.Close()
		}
	}()
	return err
}

// defaultHTTPClient returns a client able to carry Connect bidirectional
// streams to baseURL: plaintext bases need h2c, since Go's standard transport
// only negotiates HTTP/2 over TLS.
func defaultHTTPClient(base *url.URL) *http.Client {
	if base.Scheme == "https" {
		return &http.Client{Transport: &http.Transport{ForceAttemptHTTP2: true}}
	}
	return &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
}
