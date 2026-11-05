package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/egress"
	"golang.org/x/net/http2"
)

// forwardL7 handles a CONNECT tunnel whose destination allowance declared L7
// constraints. It inspects the first byte: a TLS record takes the existing
// interception path, while a plain HTTP request is decided on its request line
// without requiring a CA. An L7-constrained HTTPS destination with no configured
// interception fails closed rather than being forwarded uninspected.
//
//nolint:revive // the L7 relay threads the request identity and destination through the call boundary
func (p *EgressProxy) forwardL7(w http.ResponseWriter, r *http.Request, sandboxID string, identity egressIdentity, destination egressDestination, target net.IP) error {
	if r.ProtoMajor != 1 {
		http.Error(w, "agent-compose egress proxy: CONNECT requires HTTP/1.1", http.StatusHTTPVersionNotSupported)
		return nil
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "agent-compose egress proxy: this listener cannot inspect CONNECT", http.StatusInternalServerError)
		return errors.New("response writer does not support hijacking")
	}
	client, buffer, err := hijacker.Hijack()
	if err != nil {
		return fmt.Errorf("hijack client connection for L7 inspection: %w", err)
	}
	defer func() { _ = client.Close() }()
	if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return fmt.Errorf("write tunnel response: %w", err)
	}
	if err := buffer.Flush(); err != nil {
		return fmt.Errorf("flush tunnel response: %w", err)
	}
	conn := &egressBufferedConn{Conn: client, reader: buffer.Reader}
	first, err := conn.reader.Peek(1)
	if err != nil {
		return fmt.Errorf("read the start of the L7 tunnel: %w", err)
	}
	if first[0] == 0x16 {
		if p.mitm == nil {
			return errors.New("the destination declares L7 constraints but TLS interception is not enabled")
		}
		terminated := tls.Server(conn, p.mitmServerConfig(destination))
		handshakeContext, cancel := context.WithTimeout(r.Context(), p.dialTimeout)
		defer cancel()
		if err := terminated.HandshakeContext(handshakeContext); err != nil {
			return fmt.Errorf("terminate client TLS for %s: %w", destination.host, err)
		}
		_ = terminated.SetDeadline(time.Time{})
		if terminated.ConnectionState().NegotiatedProtocol == "h2" {
			return p.serveL7HTTP2(r.Context(), terminated, sandboxID, identity, destination, target)
		}
		return p.serveL7Tunnel(r.Context(), terminated, sandboxID, identity, destination, target, "https")
	}
	return p.serveL7Tunnel(r.Context(), conn, sandboxID, identity, destination, target, "http")
}

// egressBufferedConn reads buffered bytes before the connection so a ClientHello
// pipelined with the CONNECT request is not lost when the server is hijacked.
type egressBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *egressBufferedConn) Read(data []byte) (int, error) {
	if c.reader != nil && c.reader.Buffered() > 0 {
		return c.reader.Read(data)
	}
	return c.Conn.Read(data)
}

// mitmServerConfig builds the server TLS configuration for one destination. The
// certificate is issued for the destination the policy approved, not for a name
// the guest invents; a ClientHello whose SNI differs is refused, because that is
// exactly domain fronting.
func (p *EgressProxy) mitmServerConfig(destination egressDestination) *tls.Config {
	expectedHost := normalizeMITMHost(destination.host)
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			requested := normalizeMITMHost(hello.ServerName)
			if requested == "" {
				// A client that sends no SNI (for example one connecting to an IP
				// literal) still reaches the destination the CONNECT named.
				requested = expectedHost
			}
			if requested != expectedHost {
				return nil, fmt.Errorf("the TLS server name %q does not match the CONNECT destination %q", hello.ServerName, expectedHost)
			}
			leaf, err := p.mitm.certificateFor(expectedHost)
			if err != nil {
				return nil, err
			}
			return &tls.Config{
				MinVersion:   tls.VersionTLS12,
				NextProtos:   []string{"h2", "http/1.1"},
				Certificates: []tls.Certificate{*leaf},
			}, nil
		},
	}
}

// serveL7Tunnel reads HTTP/1.1 requests off the terminated connection and
// decides each one against the policy in force now. Denying one request closes
// the tunnel: the request body has not necessarily been drained, so continuing
// would risk parsing body bytes as the next request line.
//
//nolint:revive // the L7 relay threads the request identity and destination through the call boundary
func (p *EgressProxy) serveL7Tunnel(ctx context.Context, conn net.Conn, sandboxID string, identity egressIdentity, destination egressDestination, target net.IP, scheme string) error {
	reader := bufio.NewReader(conn)
	for {
		request, err := http.ReadRequest(reader)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("read an intercepted request: %w", err)
		}
		keepAlive, err := p.decideAndForwardL7(ctx, conn, request, sandboxID, identity, destination, target, scheme)
		if err != nil {
			return err
		}
		if !keepAlive {
			return nil
		}
	}
}

//nolint:revive // the L7 relay threads the request identity and destination through the call boundary
func (p *EgressProxy) decideAndForwardL7(ctx context.Context, conn net.Conn, request *http.Request, sandboxID string, identity egressIdentity, destination egressDestination, target net.IP, scheme string) (bool, error) {
	networkRequest, err := interceptedNetworkRequest(sandboxID, identity, destination, target, request)
	if err != nil {
		return false, err
	}
	policy, err := p.egressPolicy(ctx, sandboxID, identity)
	if err != nil {
		decision := egress.Result{
			Action: egress.Deny,
			RuleID: EgressRulePolicyUnavailable,
			Reason: "the sandbox network policy could not be read",
		}
		p.record(ctx, networkRequest, decision, http.StatusForbidden)
		return false, writeL7Error(conn, request, http.StatusForbidden, decision.RuleID, decision.Reason)
	}
	decision := egress.Decide(policy, networkRequest)
	if !decision.Allowed() {
		p.record(ctx, networkRequest, decision, http.StatusForbidden)
		return false, writeL7Error(conn, request, http.StatusForbidden, decision.RuleID, decision.Reason)
	}
	// Resolve injected credentials before recording success. A missing secret
	// must not turn into an unauthenticated upstream request.
	injected, err := expandEgressInjectedHeaders(decision.InjectHeaders)
	if err != nil {
		denial := egress.Result{
			Action:     egress.Deny,
			RuleID:     EgressRuleCredentialUnavailable,
			Reason:     err.Error(),
			Generation: decision.Generation,
		}
		p.record(ctx, networkRequest, denial, http.StatusForbidden)
		return false, writeL7Error(conn, request, http.StatusForbidden, denial.RuleID, denial.Reason)
	}
	if isWebSocketUpgrade(request) {
		return false, p.relayL7Upgrade(ctx, conn, request, destination, target, scheme, injected, networkRequest, decision)
	}
	// The status records the proxy decision, which is the same meaning it has
	// for an L4 tunnel; a later upstream failure is a transport outcome, not a
	// second policy decision.
	p.record(ctx, networkRequest, decision, http.StatusOK)

	keepAlive, err := p.relayL7Request(ctx, conn, request, destination, target, scheme, injected)
	if err != nil {
		return false, err
	}
	return keepAlive, nil
}

// serveL7HTTP2 handles an intercepted HTTP/2 connection. Each stream is
// decided on its own method/path and forwarded through the pinned upstream
// address; response bodies are streamed without buffering.
//
//nolint:revive // the L7 relay threads the request identity and destination through the call boundary
func (p *EgressProxy) serveL7HTTP2(ctx context.Context, conn net.Conn, sandboxID string, identity egressIdentity, destination egressDestination, target net.IP) error {
	server := &http2.Server{}
	server.ServeConn(conn, &http2.ServeConnOpts{
		Context: ctx,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			p.handleL7HTTP2Request(w, request, sandboxID, identity, destination, target)
		}),
	})
	return nil
}

//nolint:revive // the L7 relay threads the request identity and destination through the call boundary
func (p *EgressProxy) handleL7HTTP2Request(w http.ResponseWriter, request *http.Request, sandboxID string, identity egressIdentity, destination egressDestination, target net.IP) {
	networkRequest, err := interceptedNetworkRequest(sandboxID, identity, destination, target, request)
	if err != nil {
		writeL7HTTPError(w, http.StatusBadRequest, "engine.l7-request", err.Error())
		return
	}
	policy, err := p.egressPolicy(request.Context(), sandboxID, identity)
	if err != nil {
		denial := egress.Result{Action: egress.Deny, RuleID: EgressRulePolicyUnavailable, Reason: "the sandbox network policy could not be read"}
		p.record(request.Context(), networkRequest, denial, http.StatusForbidden)
		writeL7HTTPError(w, http.StatusForbidden, denial.RuleID, denial.Reason)
		return
	}
	decision := egress.Decide(policy, networkRequest)
	if !decision.Allowed() {
		p.record(request.Context(), networkRequest, decision, http.StatusForbidden)
		writeL7HTTPError(w, http.StatusForbidden, decision.RuleID, decision.Reason)
		return
	}
	injected, err := expandEgressInjectedHeaders(decision.InjectHeaders)
	if err != nil {
		denial := egress.Result{Action: egress.Deny, RuleID: EgressRuleCredentialUnavailable, Reason: err.Error(), Generation: decision.Generation}
		p.record(request.Context(), networkRequest, denial, http.StatusForbidden)
		writeL7HTTPError(w, http.StatusForbidden, denial.RuleID, denial.Reason)
		return
	}
	p.record(request.Context(), networkRequest, decision, http.StatusOK)
	if err := p.relayL7HTTP2(w, request, destination, target, injected); err != nil {
		writeL7HTTPError(w, http.StatusBadGateway, "engine.upstream-http2", "the upstream request failed")
	}
}

func (p *EgressProxy) relayL7HTTP2(w http.ResponseWriter, request *http.Request, destination egressDestination, target net.IP, injected map[string]string) error {
	outbound := request.Clone(request.Context())
	outbound.RequestURI = ""
	outbound.URL.Scheme = "https"
	outbound.URL.Host = net.JoinHostPort(destination.host, strconv.Itoa(destination.port))
	if strings.TrimSpace(outbound.Host) == "" {
		outbound.Host = destination.host
	}
	outbound.Header = request.Header.Clone()
	outbound.Header.Del("Proxy-Authorization")
	outbound.Header.Del("Proxy-Connection")
	for name, value := range injected {
		outbound.Header.Del(name)
		outbound.Header.Set(name, value)
	}
	removeHopByHopHeaders(outbound.Header)
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return p.dial(ctx, network, net.JoinHostPort(target.String(), strconv.Itoa(destination.port)))
		},
		TLSClientConfig:       p.upstreamTLSConfigFor(destination.host),
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: p.dialTimeout,
		DisableKeepAlives:     true,
	}
	defer transport.CloseIdleConnections()
	response, err := transport.RoundTrip(outbound)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	removeHopByHopHeaders(response.Header)
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
	return nil
}

// writeL7HTTPError writes a refusal through the net/http response writer, which
// works for HTTP/2 streams as well as an HTTP/1 response.
func writeL7HTTPError(w http.ResponseWriter, status int, rule, reason string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Agent-Compose-Egress-Rule", rule)
	w.Header().Set("X-Agent-Compose-Egress-Reason", reason)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, fmt.Sprintf("agent-compose egress proxy: %s (rule %s)\n", reason, rule))
}

// interceptedNetworkRequest rebuilds the decision request for one HTTP request
// read off a terminated tunnel. The addresses are the ones the L4 decision
// resolved and pinned, not a fresh lookup of a name the guest could re-point.
func interceptedNetworkRequest(sandboxID string, identity egressIdentity, destination egressDestination, target net.IP, request *http.Request) (egress.Request, error) {
	endpoint, err := egress.NewEndpoint(destination.host, destination.port, destination.protocol)
	if err != nil {
		return egress.Request{}, fmt.Errorf("rebuild the intercepted endpoint: %w", err)
	}
	networkRequest := egress.NetworkRequest(sandboxID, endpoint)
	networkRequest.SessionID = identity.SessionID
	networkRequest.Marker = identity.Marker
	networkRequest.ProcessID = identity.ProcessID
	networkRequest.Layer = egress.LayerL7
	networkRequest.Method = request.Method
	networkRequest.Path = egressRequestPath(request)
	networkRequest.ResolvedIPs = []string{target.String()}
	return networkRequest, nil
}

// relayL7Request forwards one allowed intercepted request to the upstream and
// writes the answer back over the terminated connection.
//
//nolint:revive // the L7 relay threads the request identity and destination through the call boundary
func (p *EgressProxy) relayL7Request(ctx context.Context, conn net.Conn, request *http.Request, destination egressDestination, target net.IP, scheme string, injected map[string]string) (bool, error) {
	outbound := request.Clone(ctx)
	outbound.RequestURI = ""
	outbound.Header = request.Header.Clone()
	outbound.Header.Del("Proxy-Authorization")
	outbound.Header.Del("Proxy-Connection")
	for name, value := range injected {
		outbound.Header.Del(name)
		outbound.Header.Set(name, value)
	}
	removeHopByHopHeaders(outbound.Header)
	if outbound.URL == nil {
		return false, errors.New("the intercepted request has no URL")
	}
	outbound.URL.Scheme = scheme
	outbound.URL.Host = net.JoinHostPort(destination.host, strconv.Itoa(destination.port))
	if strings.TrimSpace(outbound.Host) == "" {
		outbound.Host = destination.host
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return p.dial(ctx, network, net.JoinHostPort(target.String(), strconv.Itoa(destination.port)))
		},
		ResponseHeaderTimeout: p.dialTimeout,
		DisableKeepAlives:     true,
	}
	if scheme == "https" {
		transport.TLSClientConfig = p.upstreamTLSConfigFor(destination.host)
	}
	defer transport.CloseIdleConnections()
	response, err := transport.RoundTrip(outbound)
	if err != nil {
		return false, writeL7Error(conn, request, http.StatusBadGateway, "engine.upstream-tls", "the upstream TLS request failed")
	}
	defer func() { _ = response.Body.Close() }()
	removeHopByHopHeaders(response.Header)
	response.Close = response.Close || request.Close
	if err := response.Write(conn); err != nil {
		return false, fmt.Errorf("write the intercepted response: %w", err)
	}
	return !response.Close, nil
}

// expandEgressInjectedHeaders resolves ${ENV} credential templates from the
// daemon environment. Values remain in memory and are never logged or exposed
// to the guest.
func expandEgressInjectedHeaders(templates map[string]string) (map[string]string, error) {
	return egress.ExpandInjectHeaders(templates, os.LookupEnv)
}

// isWebSocketUpgrade reports whether this request asks to switch to a raw
// WebSocket stream. L7 policy is evaluated on the establishment request; after
// a 101 response the bytes are relayed without further HTTP parsing, which is
// required for Jupyter and other long-lived upgrade protocols.
func isWebSocketUpgrade(request *http.Request) bool {
	if request == nil || !strings.EqualFold(strings.TrimSpace(request.Header.Get("Upgrade")), "websocket") {
		return false
	}
	for _, token := range strings.Split(request.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}

// relayL7Upgrade forwards a WebSocket (or other HTTP Upgrade) establishment
// request and then relays the raw connection. The upstream is pinned to the
// address the L4 decision resolved; the injected credentials are applied only
// to the handshake.
//
//nolint:revive // the L7 relay threads the request identity and destination through the call boundary
func (p *EgressProxy) relayL7Upgrade(ctx context.Context, conn net.Conn, request *http.Request, destination egressDestination, target net.IP, scheme string, injected map[string]string, networkRequest egress.Request, decision egress.Result) error {
	_, err := p.relayUpgrade(ctx, conn, request, destination, target, scheme, injected, func(status int) {
		p.record(ctx, networkRequest, decision, status)
	})
	return err
}

// relayUpgrade forwards one HTTP Upgrade handshake, then relays raw bytes. The
// policy decision happens before this call; after a 101 the proxy deliberately
// stops parsing HTTP, which is what makes Jupyter and WebSocket traffic work.
//
//nolint:revive // the upgrade relay threads the request identity and destination through the call boundary
func (p *EgressProxy) relayUpgrade(ctx context.Context, conn net.Conn, request *http.Request, destination egressDestination, target net.IP, scheme string, injected map[string]string, recordStatus func(int)) (int, error) {
	upstream, err := p.dialUpgrade(ctx, destination, target, scheme)
	if err != nil {
		if writeErr := writeL7Error(conn, request, http.StatusBadGateway, "engine.upstream-upgrade", "the upstream upgrade connection failed"); writeErr != nil {
			return http.StatusBadGateway, writeErr
		}
		return http.StatusBadGateway, err
	}
	defer func() { _ = upstream.Close() }()
	outbound := request.Clone(ctx)
	outbound.RequestURI = ""
	outbound.Header = request.Header.Clone()
	outbound.Header.Del("Proxy-Authorization")
	outbound.Header.Del("Proxy-Connection")
	for name, value := range injected {
		outbound.Header.Del(name)
		outbound.Header.Set(name, value)
	}
	if err := outbound.Write(upstream); err != nil {
		return http.StatusBadGateway, fmt.Errorf("write upgrade request upstream: %w", err)
	}
	reader := bufio.NewReader(upstream)
	response, err := http.ReadResponse(reader, outbound)
	if err != nil {
		return http.StatusBadGateway, fmt.Errorf("read upgrade response: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	status := response.StatusCode
	recordStatus(status)
	if err := response.Write(conn); err != nil {
		return status, fmt.Errorf("write upgrade response to client: %w", err)
	}
	if status != http.StatusSwitchingProtocols {
		return status, nil
	}
	errCh := make(chan error, 2)
	go func() {
		_, copyErr := io.Copy(upstream, conn)
		errCh <- copyErr
	}()
	go func() {
		_, copyErr := io.Copy(conn, io.MultiReader(reader, upstream))
		errCh <- copyErr
	}()
	<-errCh
	return status, nil
}

func (p *EgressProxy) dialUpgrade(ctx context.Context, destination egressDestination, target net.IP, scheme string) (net.Conn, error) {
	raw, err := p.dial(ctx, "tcp", net.JoinHostPort(target.String(), strconv.Itoa(destination.port)))
	if err != nil {
		return nil, err
	}
	if scheme != "https" {
		return raw, nil
	}
	terminated := tls.Client(raw, p.upstreamTLSConfigFor(destination.host))
	handshakeContext, cancel := context.WithTimeout(ctx, p.dialTimeout)
	defer cancel()
	if err := terminated.HandshakeContext(handshakeContext); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return terminated, nil
}

// upstreamTLSConfigFor builds the TLS configuration the proxy presents to the
// real upstream. The server name is the CONNECT destination, and the connection
// itself is pinned by the transport's dialer.
func (p *EgressProxy) upstreamTLSConfigFor(host string) *tls.Config {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if p.upstreamTLSConfig != nil {
		config = p.upstreamTLSConfig.Clone()
		config.MinVersion = tls.VersionTLS12
		if config.ServerName == "" {
			config.ServerName = host
		}
	}
	return config
}

// writeL7Error answers one intercepted request with a proxy refusal. The tunnel
// is closed afterwards, so the response carries its own framing and a
// Connection: close header.
func writeL7Error(conn io.Writer, request *http.Request, status int, rule, reason string) error {
	body := fmt.Sprintf("agent-compose egress proxy: %s (rule %s)\n", reason, rule)
	header := make(http.Header)
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("X-Agent-Compose-Egress-Rule", rule)
	header.Set("X-Agent-Compose-Egress-Reason", reason)
	header.Set("Connection", "close")
	response := &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Close:         true,
		Request:       request,
	}
	return response.Write(conn)
}
