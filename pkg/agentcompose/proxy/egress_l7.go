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
	"strconv"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/egress"
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
		NextProtos: []string{"http/1.1"},
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
				NextProtos:   []string{"http/1.1"},
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
	// The status records the proxy decision, which is the same meaning it has
	// for an L4 tunnel; a later upstream failure is a transport outcome, not a
	// second policy decision.
	p.record(ctx, networkRequest, decision, http.StatusOK)

	keepAlive, err := p.relayL7Request(ctx, conn, request, destination, target, scheme)
	if err != nil {
		return false, err
	}
	return keepAlive, nil
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
func (p *EgressProxy) relayL7Request(ctx context.Context, conn net.Conn, request *http.Request, destination egressDestination, target net.IP, scheme string) (bool, error) {
	outbound := request.Clone(ctx)
	outbound.RequestURI = ""
	outbound.Header = request.Header.Clone()
	outbound.Header.Del("Proxy-Authorization")
	outbound.Header.Del("Proxy-Connection")
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
