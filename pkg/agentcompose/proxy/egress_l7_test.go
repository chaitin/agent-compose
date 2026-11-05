package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/egress"
)

func l7AllowPolicy(pattern, method, path string, enforcement egress.Enforcement) egress.Policy {
	return egress.NewPolicy(egress.Deny, egress.Rule{
		ID:          "network.allow",
		Match:       egress.MatchEndpoint,
		Names:       []string{pattern},
		Action:      egress.Allow,
		L7:          egress.L7Constraint{Methods: []string{method}, Paths: []string{path}},
		Enforcement: enforcement,
	})
}

func proxyHTTPClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	proxyURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parsing the proxy URL: %v", err)
	}
	proxyURL.User = url.UserPassword(testSandboxID, testCredential)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func dialUpstreamFunc(upstream net.Addr) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, upstream.String())
	}
}

func TestEgressProxyAllowsAnL7HTTPRequest(t *testing.T) {
	var methods, paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.EscapedPath())
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	policies := &proxyTestPolicies{policies: []egress.Policy{l7AllowPolicy("allowed.example:80/http", "GET", "/allowed/*", egress.EnforcementEnforce)}}
	proxyServer, recorder := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.Dial = dialUpstreamFunc(upstream.Listener.Addr())
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	response, err := proxyHTTPClient(t, server).Get("http://allowed.example/allowed/items")
	if err != nil {
		t.Fatalf("GET through the proxy: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading the forwarded body: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("response = %d %q, want 200 %q", response.StatusCode, body, "ok")
	}
	if len(methods) != 1 || methods[0] != "GET" || paths[0] != "/allowed/items" {
		t.Fatalf("upstream saw methods=%v paths=%v, want one GET /allowed/items", methods, paths)
	}
	records := recorder.collected()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if !record.Allowed() || record.Request.Layer != egress.LayerL7 {
		t.Fatalf("record = %+v, want an allowed L7 decision", record)
	}
	if record.Request.Method != "GET" || record.Request.Path != "/allowed/items" {
		t.Fatalf("record method/path = %q %q, want GET /allowed/items", record.Request.Method, record.Request.Path)
	}
	if record.Status != http.StatusOK {
		t.Fatalf("record status = %d, want 200", record.Status)
	}
}

func TestEgressProxyDeniesAnL7HTTPRequestByMethodAndPath(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "denied method", method: http.MethodPost, path: "/allowed/items"},
		{name: "denied path", method: http.MethodGet, path: "/denied/items"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var hits int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&hits, 1)
				_, _ = io.WriteString(w, "should not be reached")
			}))
			defer upstream.Close()

			policies := &proxyTestPolicies{policies: []egress.Policy{l7AllowPolicy("allowed.example:80/http", "GET", "/allowed/*", egress.EnforcementEnforce)}}
			proxyServer, recorder := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
				options.Dial = dialUpstreamFunc(upstream.Listener.Addr())
			})
			server := httptest.NewServer(proxyServer)
			defer server.Close()

			request, err := http.NewRequest(test.method, "http://allowed.example"+test.path, nil)
			if err != nil {
				t.Fatalf("building the request: %v", err)
			}
			response, err := proxyHTTPClient(t, server).Do(request)
			if err != nil {
				t.Fatalf("proxy request: %v", err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", response.StatusCode)
			}
			if got := atomic.LoadInt32(&hits); got != 0 {
				t.Fatalf("upstream hits = %d, want 0", got)
			}
			records := recorder.collected()
			if len(records) != 1 || records[0].Allowed() {
				t.Fatalf("records = %+v, want one deny", records)
			}
			record := records[0]
			if record.Request.Method != test.method || record.Request.Path != test.path {
				t.Fatalf("record method/path = %q %q, want %q %q", record.Request.Method, record.Request.Path, test.method, test.path)
			}
			if record.Status != http.StatusForbidden {
				t.Fatalf("record status = %d, want 403", record.Status)
			}
		})
	}
}

func TestEgressProxyAuditsAnL7MismatchWithoutDenying(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	policies := &proxyTestPolicies{policies: []egress.Policy{l7AllowPolicy("allowed.example:80/http", "GET", "/allowed/*", egress.EnforcementAudit)}}
	proxyServer, recorder := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.Dial = dialUpstreamFunc(upstream.Listener.Addr())
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	response, err := proxyHTTPClient(t, server).Post("http://allowed.example/allowed/items", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("POST through the proxy: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the audit-only allowance to pass", response.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("upstream hits = %d, want 1", got)
	}
	records := recorder.collected()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if !record.Allowed() || record.Result.Enforcement != egress.EnforcementAudit {
		t.Fatalf("record = %+v, want an audit-only allow", record)
	}
	if !strings.Contains(record.Result.Reason, "audit") {
		t.Fatalf("reason = %q, want it to name the audit mode", record.Result.Reason)
	}
}

func TestEgressProxyFailsClosedForL7HTTPSWithoutInterception(t *testing.T) {
	policies := &proxyTestPolicies{policies: []egress.Policy{l7AllowPolicy("api.example:443/https", "GET", "/v1/*", egress.EnforcementEnforce)}}
	proxyServer, recorder := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.Dial = func(context.Context, string, string) (net.Conn, error) {
			return nil, fmt.Errorf("dial must not happen for an L7 declaration the proxy cannot inspect")
		}
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	connection := dialProxy(t, server)
	defer func() { _ = connection.Close() }()
	writeConnect(t, connection, "api.example:443", testSandboxID, testCredential)
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the CONNECT response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("CONNECT status = %d, want 403", response.StatusCode)
	}
	if got := response.Header.Get("X-Agent-Compose-Egress-Rule"); got != EgressRuleL7Unavailable {
		t.Fatalf("rule header = %q, want %q", got, EgressRuleL7Unavailable)
	}
	records := recorder.collected()
	if len(records) != 1 || records[0].Allowed() || records[0].Result.RuleID != EgressRuleL7Unavailable {
		t.Fatalf("records = %+v, want one deny under the unavailable-L7 rule", records)
	}
}

func TestEgressProxyLeavesANonL7HTTPSEndpointOpaqueWithInterceptionEnabled(t *testing.T) {
	policies := &proxyTestPolicies{policies: []egress.Policy{allowEndpointPolicy("allowed.example:443/https")}}
	upstreamSide, upstream := net.Pipe()
	defer func() { _ = upstreamSide.Close() }()
	proxyServer, _ := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.TLSMITM = true
		options.Dial = func(context.Context, string, string) (net.Conn, error) { return upstream, nil }
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	connection := dialProxy(t, server)
	defer func() { _ = connection.Close() }()
	writeConnect(t, connection, "allowed.example:443", testSandboxID, testCredential)
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the CONNECT response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", response.StatusCode)
	}
	if _, err := connection.Write([]byte("opaque")); err != nil {
		t.Fatalf("writing opaque tunnel bytes: %v", err)
	}
	received := make([]byte, len("opaque"))
	if _, err := io.ReadFull(upstreamSide, received); err != nil {
		t.Fatalf("reading opaque bytes upstream: %v", err)
	}
	if string(received) != "opaque" {
		t.Fatalf("upstream received %q, want the opaque tunnel bytes", received)
	}
}

// testMITMUpstream serves TLS with a leaf for upstream.example signed by a
// throwaway CA, and returns the server plus the roots the proxy must trust.
func testMITMUpstream(t *testing.T, handler http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	authority, err := newEgressMITMCA(nil, nil)
	if err != nil {
		t.Fatalf("generating the upstream CA: %v", err)
	}
	leaf, err := authority.certificateFor("upstream.example")
	if err != nil {
		t.Fatalf("issuing the upstream leaf: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{*leaf}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(authority.CACertificatePEM()) {
		t.Fatal("appending the upstream CA failed")
	}
	return server, roots
}

func connectInterceptedTunnel(t *testing.T, proxyServer *httptest.Server, proxyRoots *x509.CertPool, serverName string) (*tls.Conn, *bufio.Reader) {
	t.Helper()
	connection := dialProxy(t, proxyServer)
	writeConnect(t, connection, "upstream.example:443", testSandboxID, testCredential)
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the CONNECT response: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_ = connection.Close()
		t.Fatalf("CONNECT status = %d, want 200", response.StatusCode)
	}
	terminated := tls.Client(&egressBufferedConn{Conn: connection, reader: reader}, &tls.Config{
		ServerName: serverName,
		RootCAs:    proxyRoots,
		MinVersion: tls.VersionTLS12,
	})
	if err := terminated.Handshake(); err != nil {
		_ = connection.Close()
		t.Fatalf("TLS handshake with the intercepted proxy: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return terminated, bufio.NewReader(terminated)
}

func TestEgressProxyInterceptsTLSForAnL7Endpoint(t *testing.T) {
	var upstreamMethod, upstreamPath string
	upstream, upstreamRoots := testMITMUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamMethod = r.Method
		upstreamPath = r.URL.EscapedPath()
		_, _ = io.WriteString(w, "ok")
	}))
	_ = upstream

	policies := &proxyTestPolicies{policies: []egress.Policy{l7AllowPolicy("upstream.example:443/https", "GET", "/allowed/*", egress.EnforcementEnforce)}}
	proxyServer, recorder := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.TLSMITM = true
		options.Dial = dialUpstreamFunc(upstream.Listener.Addr())
		options.UpstreamTLSConfig = &tls.Config{RootCAs: upstreamRoots, MinVersion: tls.VersionTLS12}
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()
	proxyRoots := x509.NewCertPool()
	if !proxyRoots.AppendCertsFromPEM(proxyServer.mitm.CACertificatePEM()) {
		t.Fatal("appending the proxy CA failed")
	}

	terminated, reader := connectInterceptedTunnel(t, server, proxyRoots, "upstream.example")
	defer func() { _ = terminated.Close() }()
	if _, err := io.WriteString(terminated, "GET /allowed/items HTTP/1.1\r\nHost: upstream.example\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("writing the intercepted request: %v", err)
	}
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("reading the intercepted response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("response = %d %q, want 200 %q", response.StatusCode, body, "ok")
	}
	if upstreamMethod != "GET" || upstreamPath != "/allowed/items" {
		t.Fatalf("upstream saw %q %q, want GET /allowed/items", upstreamMethod, upstreamPath)
	}
	records := recorder.collected()
	if len(records) != 2 {
		t.Fatalf("records = %d, want the CONNECT L4 allow and the L7 allow: %+v", len(records), records)
	}
	if records[0].Request.Layer != egress.LayerL4 || !records[0].Allowed() {
		t.Fatalf("first record = %+v, want the L4 CONNECT allow", records[0])
	}
	l7 := records[1]
	if l7.Request.Layer != egress.LayerL7 || l7.Request.Method != "GET" || l7.Request.Path != "/allowed/items" {
		t.Fatalf("second record = %+v, want the L7 GET decision", l7)
	}
	if !l7.Allowed() || l7.Status != http.StatusOK {
		t.Fatalf("second record = %+v, want an allowed 200 decision", l7)
	}
	if len(l7.Request.ResolvedIPs) != 1 || l7.Request.ResolvedIPs[0] != testAllowedIP {
		t.Fatalf("resolved IPs = %v, want the L4-pinned address %s", l7.Request.ResolvedIPs, testAllowedIP)
	}
}

func TestEgressProxyDeniesAnL7TLSRequest(t *testing.T) {
	var hits int32
	upstream, upstreamRoots := testMITMUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, "should not be reached")
	}))
	_ = upstream

	policies := &proxyTestPolicies{policies: []egress.Policy{l7AllowPolicy("upstream.example:443/https", "GET", "/allowed/*", egress.EnforcementEnforce)}}
	proxyServer, recorder := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.TLSMITM = true
		options.Dial = dialUpstreamFunc(upstream.Listener.Addr())
		options.UpstreamTLSConfig = &tls.Config{RootCAs: upstreamRoots, MinVersion: tls.VersionTLS12}
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()
	proxyRoots := x509.NewCertPool()
	if !proxyRoots.AppendCertsFromPEM(proxyServer.mitm.CACertificatePEM()) {
		t.Fatal("appending the proxy CA failed")
	}

	terminated, reader := connectInterceptedTunnel(t, server, proxyRoots, "upstream.example")
	defer func() { _ = terminated.Close() }()
	if _, err := io.WriteString(terminated, "POST /allowed/items HTTP/1.1\r\nHost: upstream.example\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("writing the intercepted request: %v", err)
	}
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("reading the intercepted response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("upstream hits = %d, want 0", got)
	}
	records := recorder.collected()
	if len(records) != 2 {
		t.Fatalf("records = %d, want the CONNECT allow and the L7 deny: %+v", len(records), records)
	}
	denied := records[1]
	if denied.Allowed() || denied.Request.Method != "POST" || denied.Request.Path != "/allowed/items" {
		t.Fatalf("second record = %+v, want a denied L7 POST", denied)
	}
	if denied.Status != http.StatusForbidden {
		t.Fatalf("denied status = %d, want 403", denied.Status)
	}
}

func TestEgressProxyRefusesAMismatchedSNI(t *testing.T) {
	upstream, upstreamRoots := testMITMUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "unexpected")
	}))
	_ = upstream

	policies := &proxyTestPolicies{policies: []egress.Policy{l7AllowPolicy("upstream.example:443/https", "GET", "/allowed/*", egress.EnforcementEnforce)}}
	proxyServer, _ := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.TLSMITM = true
		options.Dial = dialUpstreamFunc(upstream.Listener.Addr())
		options.UpstreamTLSConfig = &tls.Config{RootCAs: upstreamRoots, MinVersion: tls.VersionTLS12}
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()
	proxyRoots := x509.NewCertPool()
	if !proxyRoots.AppendCertsFromPEM(proxyServer.mitm.CACertificatePEM()) {
		t.Fatal("appending the proxy CA failed")
	}

	connection := dialProxy(t, server)
	defer func() { _ = connection.Close() }()
	writeConnect(t, connection, "upstream.example:443", testSandboxID, testCredential)
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the CONNECT response: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", response.StatusCode)
	}
	terminated := tls.Client(&egressBufferedConn{Conn: connection, reader: reader}, &tls.Config{
		ServerName: "evil.example",
		RootCAs:    proxyRoots,
		MinVersion: tls.VersionTLS12,
	})
	if err := terminated.Handshake(); err == nil {
		_ = terminated.Close()
		t.Fatal("the intercepted proxy completed a handshake for an SNI that does not match the CONNECT destination")
	}
}
