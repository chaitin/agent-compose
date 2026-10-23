package proxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/egress"
)

const (
	testSandboxID  = "sandbox-1"
	testCredential = "ac_cred_test"
	testAllowedIP  = "93.184.216.34"
)

// proxyTestAuthorizer resolves one known credential and rejects everything
// else, which is what a store-backed authorizer does for an unknown or revoked
// credential.
type proxyTestAuthorizer struct {
	sandboxID  string
	credential string
	err        error
	// ignoreClaimed models an authorizer that resolves a credential without
	// checking the sandbox name the caller presented, so the proxy's own
	// identity cross-check stays covered.
	ignoreClaimed bool
}

func (a proxyTestAuthorizer) AuthorizeEgressProxy(_ context.Context, claimed, credential string) (string, error) {
	if a.err != nil {
		return "", a.err
	}
	if credential != a.credential {
		return "", errors.New("unknown credential")
	}
	if !a.ignoreClaimed && claimed != "" && claimed != a.sandboxID {
		return "", errors.New("credential belongs to another sandbox")
	}
	return a.sandboxID, nil
}

// proxyTestPolicies serves a sequence of policies, one per call, so a test can
// change the policy between two requests and observe that the proxy reads it
// per request rather than once per tunnel.
type proxyTestPolicies struct {
	mu       sync.Mutex
	policies []egress.Policy
	err      error
	calls    int
}

func (p *proxyTestPolicies) EgressProxyPolicy(_ context.Context, _ string) (egress.Policy, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return egress.Policy{}, p.err
	}
	if len(p.policies) == 0 {
		return egress.NewPolicy(egress.Allow), nil
	}
	index := p.calls - 1
	if index >= len(p.policies) {
		index = len(p.policies) - 1
	}
	return p.policies[index], nil
}

func (p *proxyTestPolicies) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// proxyTestRecorder collects the records the proxy emits.
type proxyTestRecorder struct {
	mu      sync.Mutex
	records []egress.Record
}

func (r *proxyTestRecorder) RecordEgressDecision(_ context.Context, record egress.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
}

func (r *proxyTestRecorder) collected() []egress.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]egress.Record(nil), r.records...)
}

func proxyBasicAuth(user, secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+secret))
}

// newTestEgressProxy builds a proxy whose resolution and dialing are injected,
// so a test decides exactly what the destination resolves to and what the
// upstream connection is.
func newTestEgressProxy(t *testing.T, policies EgressProxyPolicySource, options ...func(*EgressProxyOptions)) (*EgressProxy, *proxyTestRecorder) {
	t.Helper()
	recorder := &proxyTestRecorder{}
	proxyOptions := EgressProxyOptions{
		Authorizer: proxyTestAuthorizer{sandboxID: testSandboxID, credential: testCredential},
		Policies:   policies,
		Recorder:   recorder,
		LookupIP: func(_ context.Context, host string) ([]net.IP, error) {
			if host == "unresolvable.example" {
				return nil, errors.New("no such host")
			}
			return []net.IP{net.ParseIP(testAllowedIP)}, nil
		},
		Dial: func(_ context.Context, _, _ string) (net.Conn, error) {
			return nil, errors.New("no dialer configured for this test")
		},
	}
	for _, apply := range options {
		apply(&proxyOptions)
	}
	created, err := NewEgressProxy(proxyOptions)
	if err != nil {
		t.Fatalf("NewEgressProxy() error = %v", err)
	}
	return created, recorder
}

func allowEndpointPolicy(pattern string) egress.Policy {
	return egress.NewPolicy(egress.Deny, egress.Rule{
		ID: "network.allow", Match: egress.MatchEndpoint, Names: []string{pattern}, Action: egress.Allow,
	})
}

func TestEgressProxyTunnelsAnAllowedConnect(t *testing.T) {
	policies := &proxyTestPolicies{policies: []egress.Policy{allowEndpointPolicy("allowed.example:443/https")}}
	upstreamSide, upstream := net.Pipe()
	defer func() { _ = upstreamSide.Close() }()
	proxyServer, recorder := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.Dial = func(_ context.Context, _, address string) (net.Conn, error) {
			if address != net.JoinHostPort(testAllowedIP, "443") {
				return nil, fmt.Errorf("dialed %q, want the resolved address", address)
			}
			return upstream, nil
		}
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	connection := dialProxy(t, server)
	defer func() { _ = connection.Close() }()
	writeConnect(t, connection, "allowed.example:443", testSandboxID, testCredential)
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the tunnel response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", response.StatusCode)
	}
	// The tunnel carries bytes in both directions between the sandbox and the
	// upstream the proxy dialed.
	if _, err := connection.Write([]byte("ping")); err != nil {
		t.Fatalf("writing through the tunnel: %v", err)
	}
	received := make([]byte, 4)
	if _, err := io.ReadFull(upstreamSide, received); err != nil {
		t.Fatalf("reading the tunnelled bytes: %v", err)
	}
	if string(received) != "ping" {
		t.Fatalf("upstream received %q, want %q", received, "ping")
	}
	if _, err := upstreamSide.Write([]byte("pong")); err != nil {
		t.Fatalf("writing the upstream answer: %v", err)
	}
	answer := make([]byte, 4)
	if _, err := io.ReadFull(reader, answer); err != nil {
		t.Fatalf("reading the upstream answer: %v", err)
	}
	if string(answer) != "pong" {
		t.Fatalf("sandbox received %q, want %q", answer, "pong")
	}

	records := recorder.collected()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1: every decision is accounted for", len(records))
	}
	if !records[0].Allowed() {
		t.Fatalf("record = %+v, want an allow", records[0])
	}
	if records[0].Request.Consumer != testSandboxID || records[0].Request.Name != "allowed.example:443/https" {
		t.Fatalf("record request = %+v, want the sandbox and the canonical endpoint", records[0].Request)
	}
}

// TestEgressProxyTunnelsBytesSentWithTheConnectRequest covers the hijacked
// buffer: a client may pipeline its first tunnel bytes with the CONNECT request,
// and those bytes sit in the server's bufio reader once the connection is
// hijacked. Copying from the bare connection would leave them there and stall
// the client.
func TestEgressProxyTunnelsBytesSentWithTheConnectRequest(t *testing.T) {
	policies := &proxyTestPolicies{policies: []egress.Policy{allowEndpointPolicy("allowed.example:443/https")}}
	upstreamSide, upstream := net.Pipe()
	defer func() { _ = upstreamSide.Close() }()
	proxyServer, _ := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.Dial = func(context.Context, string, string) (net.Conn, error) { return upstream, nil }
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	connection := dialProxy(t, server)
	defer func() { _ = connection.Close() }()
	// One write, so the request and the first tunnel bytes reach the proxy
	// together and the bytes are buffered before it hijacks.
	request := "CONNECT allowed.example:443 HTTP/1.1\r\nHost: allowed.example:443\r\n" +
		"Proxy-Authorization: " + proxyBasicAuth(testSandboxID, testCredential) + "\r\n\r\nping"
	if _, err := connection.Write([]byte(request)); err != nil {
		t.Fatalf("writing CONNECT with pipelined bytes: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the tunnel response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", response.StatusCode)
	}
	if err := upstreamSide.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting the read deadline: %v", err)
	}
	received := make([]byte, 4)
	if _, err := io.ReadFull(upstreamSide, received); err != nil {
		t.Fatalf("reading the pipelined bytes: %v", err)
	}
	if string(received) != "ping" {
		t.Fatalf("upstream received %q, want %q", received, "ping")
	}
}

// TestEgressProxyBoundsAnUpstreamThatNeverAnswers pins the response-header
// bound: a dial that succeeds proves nothing about an upstream that accepts the
// connection and then stays silent, which would otherwise pin the handler
// goroutine, and its daemon connection, for as long as the guest keeps the
// request open.
func TestEgressProxyBoundsAnUpstreamThatNeverAnswers(t *testing.T) {
	policies := &proxyTestPolicies{policies: []egress.Policy{allowEndpointPolicy("allowed.example:80/http")}}
	upstreamSide, upstream := net.Pipe()
	defer func() { _ = upstreamSide.Close() }()
	// Drain the forwarded request so it is fully written, then stay silent: the
	// bound under test is the wait for response headers, not the write.
	go func() { _, _ = io.Copy(io.Discard, upstreamSide) }()
	proxyServer, _ := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.DialTimeout = 100 * time.Millisecond
		options.Dial = func(context.Context, string, string) (net.Conn, error) { return upstream, nil }
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	connection := dialProxy(t, server)
	defer func() { _ = connection.Close() }()
	request := "GET http://allowed.example/ HTTP/1.1\r\nHost: allowed.example\r\n" +
		"Proxy-Authorization: " + proxyBasicAuth(testSandboxID, testCredential) + "\r\n\r\n"
	if _, err := connection.Write([]byte(request)); err != nil {
		t.Fatalf("writing the request: %v", err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting the read deadline: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 after the response-header bound", response.StatusCode)
	}
}

func TestEgressProxyDeniesAnUndeclaredDestination(t *testing.T) {
	policies := &proxyTestPolicies{policies: []egress.Policy{allowEndpointPolicy("allowed.example:443/https")}}
	proxyServer, recorder := newTestEgressProxy(t, policies)
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	connection := dialProxy(t, server)
	defer func() { _ = connection.Close() }()
	writeConnect(t, connection, "blocked.example:443", testSandboxID, testCredential)
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the deny response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("CONNECT status = %d, want 403", response.StatusCode)
	}
	if rule := response.Header.Get("X-Agent-Compose-Egress-Rule"); rule != "" {
		t.Fatalf("deny rule = %q, want empty for a policy default deny", rule)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading the deny body: %v", err)
	}
	if !strings.Contains(string(body), "egress proxy") {
		t.Fatalf("deny body = %q, want the proxy reason", body)
	}
	records := recorder.collected()
	if len(records) != 1 || records[0].Allowed() {
		t.Fatalf("records = %+v, want one deny", records)
	}
	if records[0].Request.Name != "blocked.example:443/https" {
		t.Fatalf("record name = %q, want the canonical endpoint", records[0].Request.Name)
	}
}

func TestEgressProxyForwardsAnAllowedHTTPRequest(t *testing.T) {
	policies := &proxyTestPolicies{policies: []egress.Policy{allowEndpointPolicy("allowed.example:80/http")}}
	proxyServer, _ := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.Dial = func(_ context.Context, _, address string) (net.Conn, error) {
			if address != net.JoinHostPort(testAllowedIP, "80") {
				return nil, fmt.Errorf("dialed %q, want the resolved address", address)
			}
			serverSide, clientSide := net.Pipe()
			go func() {
				defer func() { _ = serverSide.Close() }()
				buffer := make([]byte, 4096)
				if _, err := serverSide.Read(buffer); err != nil {
					return
				}
				_, _ = serverSide.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
			}()
			return clientSide, nil
		}
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	proxyURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parsing the proxy URL: %v", err)
	}
	proxyURL.User = url.UserPassword(testSandboxID, testCredential)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	defer client.CloseIdleConnections()
	response, err := client.Get("http://allowed.example/")
	if err != nil {
		t.Fatalf("GET through the proxy: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading the forwarded body: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("forwarded response = %d %q, want 200 %q", response.StatusCode, body, "ok")
	}
}

func TestEgressProxyRefusesBlockedAddresses(t *testing.T) {
	tests := []struct {
		name    string
		address string
	}{
		{name: "cloud metadata", address: "169.254.169.254"},
		{name: "ipv4 loopback", address: "127.0.0.1"},
		{name: "ipv6 loopback", address: "::1"},
		{name: "unspecified", address: "0.0.0.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// The policy allows everything, so only the engine's own address
			// rule can produce this deny.
			policies := &proxyTestPolicies{policies: []egress.Policy{egress.NewPolicy(egress.Allow)}}
			proxyServer, recorder := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
				options.LookupIP = func(_ context.Context, _ string) ([]net.IP, error) {
					address := net.ParseIP(test.address)
					if address == nil {
						return nil, fmt.Errorf("bad test address %q", test.address)
					}
					return []net.IP{address}, nil
				}
			})
			server := httptest.NewServer(proxyServer)
			defer server.Close()

			connection := dialProxy(t, server)
			defer func() { _ = connection.Close() }()
			writeConnect(t, connection, "metadata.example:443", testSandboxID, testCredential)
			response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
			if err != nil {
				t.Fatalf("reading the deny response: %v", err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("CONNECT status = %d, want 403", response.StatusCode)
			}
			if rule := response.Header.Get("X-Agent-Compose-Egress-Rule"); rule != EgressRuleBlockedAddress {
				t.Fatalf("deny rule = %q, want %q", rule, EgressRuleBlockedAddress)
			}
			records := recorder.collected()
			if len(records) != 1 || records[0].Allowed() {
				t.Fatalf("records = %+v, want one deny", records)
			}
			if records[0].Result.RuleID != EgressRuleBlockedAddress {
				t.Fatalf("record rule = %q, want %q", records[0].Result.RuleID, EgressRuleBlockedAddress)
			}
		})
	}
}

// TestEgressProxyRefusesControlPlanePorts is the other unconditional deny: the
// policy allows everything, including the exact destination, and the proxy must
// still refuse a control-plane port. A declaration cannot buy a path to the
// platform's etcd or kubelet.
func TestEgressProxyRefusesControlPlanePorts(t *testing.T) {
	for port, role := range blockedEnginePorts {
		t.Run(fmt.Sprintf("%d_%s", port, strings.ReplaceAll(role, " ", "_")), func(t *testing.T) {
			destination := fmt.Sprintf("kube.internal:%d", port)
			policies := &proxyTestPolicies{policies: []egress.Policy{egress.NewPolicy(egress.Allow)}}
			proxyServer, recorder := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
				options.LookupIP = func(context.Context, string) ([]net.IP, error) {
					return []net.IP{net.ParseIP("203.0.113.10")}, nil
				}
			})
			server := httptest.NewServer(proxyServer)
			defer server.Close()

			connection := dialProxy(t, server)
			defer func() { _ = connection.Close() }()
			writeConnect(t, connection, destination, testSandboxID, testCredential)
			response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
			if err != nil {
				t.Fatalf("reading the deny response: %v", err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("CONNECT status = %d, want 403 even though the policy allows %s", response.StatusCode, destination)
			}
			if rule := response.Header.Get("X-Agent-Compose-Egress-Rule"); rule != EgressRuleBlockedPort {
				t.Fatalf("deny rule = %q, want %q", rule, EgressRuleBlockedPort)
			}
			if reason := response.Header.Get("X-Agent-Compose-Egress-Reason"); !strings.Contains(reason, role) {
				t.Fatalf("deny reason = %q, want it to name the %s role", reason, role)
			}
			records := recorder.collected()
			if len(records) != 1 || records[0].Allowed() || records[0].Result.RuleID != EgressRuleBlockedPort {
				t.Fatalf("records = %+v, want one %s deny", records, EgressRuleBlockedPort)
			}
		})
	}
}

func TestEgressProxyStillForwardsAnUnblockedPort(t *testing.T) {
	// The same host and policy, on a port no engine rule reserves, must be
	// decided by the policy rather than swallowed by the port rule.
	policies := &proxyTestPolicies{policies: []egress.Policy{egress.NewPolicy(egress.Allow)}}
	proxyServer, _ := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.LookupIP = func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		}
	})
	// A dial that fails still proves the decision was an allow: the request
	// reached the forwarding step instead of the port rule.
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	connection := dialProxy(t, server)
	defer func() { _ = connection.Close() }()
	writeConnect(t, connection, "kube.internal:6444", testSandboxID, testCredential)
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if rule := response.Header.Get("X-Agent-Compose-Egress-Rule"); rule == EgressRuleBlockedPort {
		t.Fatalf("port 6444 was refused by the control-plane rule")
	}
}

// TestEgressProxyAppliesADeclaredAddressRange is the end-to-end shape of
// allowed_ips: the same declaration and the same name, decided only by the
// address the name resolved to.
func TestEgressProxyAppliesADeclaredAddressRange(t *testing.T) {
	const allowedRange = "203.0.113.0/24"
	policy := egress.NewPolicy(egress.Deny, egress.Rule{
		ID:         "network.allow",
		Match:      egress.MatchEndpoint,
		Names:      []string{"api.example.com:443/https"},
		Action:     egress.Allow,
		AllowedIPs: []string{allowedRange},
	})

	tests := []struct {
		name       string
		resolved   string
		wantCode   int
		wantRule   string
		wantDialed bool
	}{
		{name: "an address inside the declared range is tunnelled", resolved: "203.0.113.10", wantCode: http.StatusOK, wantDialed: true},
		{name: "an address outside it is refused", resolved: "198.51.100.7", wantCode: http.StatusForbidden, wantRule: "network.allow"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policies := &proxyTestPolicies{policies: []egress.Policy{policy}}
			upstreamSide, upstream := net.Pipe()
			defer func() { _ = upstreamSide.Close() }()
			dialed := false
			proxyServer, recorder := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
				options.LookupIP = func(context.Context, string) ([]net.IP, error) {
					return []net.IP{net.ParseIP(test.resolved)}, nil
				}
				options.Dial = func(_ context.Context, _, address string) (net.Conn, error) {
					dialed = true
					if address != net.JoinHostPort(test.resolved, "443") {
						return nil, fmt.Errorf("dialed %q, want the address the decision used", address)
					}
					return upstream, nil
				}
			})
			server := httptest.NewServer(proxyServer)
			defer server.Close()

			connection := dialProxy(t, server)
			defer func() { _ = connection.Close() }()
			writeConnect(t, connection, "api.example.com:443", testSandboxID, testCredential)
			response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
			if err != nil {
				t.Fatalf("reading the response: %v", err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != test.wantCode {
				t.Fatalf("CONNECT status = %d, want %d", response.StatusCode, test.wantCode)
			}
			if test.wantRule != "" {
				if rule := response.Header.Get("X-Agent-Compose-Egress-Rule"); rule != test.wantRule {
					t.Fatalf("deny rule = %q, want %q", rule, test.wantRule)
				}
				if reason := response.Header.Get("X-Agent-Compose-Egress-Reason"); !strings.Contains(reason, test.resolved) {
					t.Fatalf("deny reason = %q, want it to name %s", reason, test.resolved)
				}
			}
			if dialed != test.wantDialed {
				t.Fatalf("dialed = %v, want %v", dialed, test.wantDialed)
			}
			records := recorder.collected()
			if len(records) != 1 {
				t.Fatalf("records = %+v, want exactly one decision", records)
			}
			if records[0].Allowed() != test.wantDialed {
				t.Fatalf("record = %+v, want allowed=%v", records[0], test.wantDialed)
			}
		})
	}
}

// TestEgressProxyForwardsAnIPv6OnlyDestination pins that the proxy and the
// canonical endpoint identifier carry IPv6 answers instead of refusing a name
// that has no IPv4 address.
func TestEgressProxyForwardsAnIPv6OnlyDestination(t *testing.T) {
	policies := &proxyTestPolicies{policies: []egress.Policy{egress.NewPolicy(egress.Allow)}}
	proxyServer, recorder := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.LookupIP = func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("2001:db8::1")}, nil
		}
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	connection := dialProxy(t, server)
	defer func() { _ = connection.Close() }()
	writeConnect(t, connection, "ipv6.example:443", testSandboxID, testCredential)
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the deny response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	// The test proxy has no dialer, so the upstream attempt fails after the
	// policy allows the IPv6 destination. A 502 proves the request got past the
	// policy instead of being refused for lacking an IPv4 answer.
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("CONNECT status = %d, want 502", response.StatusCode)
	}
	if records := recorder.collected(); len(records) != 1 || !records[0].Allowed() {
		t.Fatalf("records = %+v, want one allow", records)
	}
}

func TestEgressProxyRequiresASandboxCredential(t *testing.T) {
	policies := &proxyTestPolicies{}
	proxyServer, recorder := newTestEgressProxy(t, policies)
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	tests := []struct {
		name     string
		user     string
		secret   string
		auth     bool
		wantCode int
	}{
		{name: "no credential", wantCode: http.StatusProxyAuthRequired},
		{name: "unknown credential", auth: true, user: testSandboxID, secret: "ac_cred_wrong", wantCode: http.StatusProxyAuthRequired},
		{name: "credential presented as another sandbox", auth: true, user: "sandbox-2", secret: testCredential, wantCode: http.StatusProxyAuthRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection := dialProxy(t, server)
			defer func() { _ = connection.Close() }()
			request := "CONNECT allowed.example:443 HTTP/1.1\r\nHost: allowed.example:443\r\n"
			if test.auth {
				request += "Proxy-Authorization: " + proxyBasicAuth(test.user, test.secret) + "\r\n"
			}
			if _, err := connection.Write([]byte(request + "\r\n")); err != nil {
				t.Fatalf("writing the request: %v", err)
			}
			response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
			if err != nil {
				t.Fatalf("reading the response: %v", err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != test.wantCode {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantCode)
			}
			if test.wantCode == http.StatusProxyAuthRequired && response.Header.Get("Proxy-Authenticate") == "" {
				t.Fatal("407 without a Proxy-Authenticate challenge")
			}
		})
	}
	// An unauthenticated caller is never decided against a policy.
	if policies.callCount() != 0 {
		t.Fatalf("policy reads = %d, want 0 for unauthenticated requests", policies.callCount())
	}
	if len(recorder.collected()) != 0 {
		t.Fatal("an unauthenticated request produced a decision record")
	}
}

// TestEgressProxyRejectsAnIdentityMismatch is the second layer: even an
// authorizer that resolves a credential without checking the presented name
// cannot make the proxy decide for a sandbox the credential does not belong to.
func TestEgressProxyRejectsAnIdentityMismatch(t *testing.T) {
	policies := &proxyTestPolicies{}
	proxyServer, _ := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.Authorizer = proxyTestAuthorizer{sandboxID: testSandboxID, credential: testCredential, ignoreClaimed: true}
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	connection := dialProxy(t, server)
	defer func() { _ = connection.Close() }()
	writeConnect(t, connection, "allowed.example:443", "sandbox-2", testCredential)
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a credential presented as another sandbox", response.StatusCode)
	}
	if policies.callCount() != 0 {
		t.Fatalf("policy reads = %d, want 0: an unidentified caller is never decided", policies.callCount())
	}
}

func TestEgressProxyDeniesWhenThePolicyOrTheNameIsUnusable(t *testing.T) {
	tests := []struct {
		name     string
		policies EgressProxyPolicySource
		host     string
		wantRule string
	}{
		{
			name:     "policy unreadable",
			policies: &proxyTestPolicies{err: errors.New("store unavailable")},
			host:     "allowed.example:443",
			wantRule: EgressRulePolicyUnavailable,
		},
		{
			name:     "name does not resolve",
			policies: &proxyTestPolicies{policies: []egress.Policy{egress.NewPolicy(egress.Allow)}},
			host:     "unresolvable.example:443",
			wantRule: EgressRuleUnresolvedAddress,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proxyServer, recorder := newTestEgressProxy(t, test.policies)
			server := httptest.NewServer(proxyServer)
			defer server.Close()

			connection := dialProxy(t, server)
			defer func() { _ = connection.Close() }()
			writeConnect(t, connection, test.host, testSandboxID, testCredential)
			response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
			if err != nil {
				t.Fatalf("reading the deny response: %v", err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", response.StatusCode)
			}
			if rule := response.Header.Get("X-Agent-Compose-Egress-Rule"); rule != test.wantRule {
				t.Fatalf("deny rule = %q, want %q", rule, test.wantRule)
			}
			for _, record := range recorder.collected() {
				if record.Allowed() {
					t.Fatalf("record = %+v, want no allow", record)
				}
			}
		})
	}
}

// TestEgressProxyReadsThePolicyPerRequest is what makes a policy change reach a
// sandbox that is already running: the same credential is decided against a
// different policy on the second request, with no restart and no new tunnel
// state.
func TestEgressProxyReadsThePolicyPerRequest(t *testing.T) {
	policies := &proxyTestPolicies{policies: []egress.Policy{
		egress.NewPolicy(egress.Allow),
		allowEndpointPolicy("allowed.example:443/https"),
	}}
	upstreamSide, upstream := net.Pipe()
	defer func() { _ = upstreamSide.Close() }()
	proxyServer, _ := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.Dial = func(_ context.Context, _, _ string) (net.Conn, error) { return upstream, nil }
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	first := dialProxy(t, server)
	writeConnect(t, first, "blocked.example:443", testSandboxID, testCredential)
	firstReader := bufio.NewReader(first)
	firstResponse, err := http.ReadResponse(firstReader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the first response: %v", err)
	}
	defer func() { _ = firstResponse.Body.Close() }()
	if firstResponse.StatusCode != http.StatusOK {
		t.Fatalf("first CONNECT status = %d, want 200 under the permissive policy", firstResponse.StatusCode)
	}
	_ = first.Close()

	second := dialProxy(t, server)
	defer func() { _ = second.Close() }()
	writeConnect(t, second, "blocked.example:443", testSandboxID, testCredential)
	secondReader := bufio.NewReader(second)
	secondResponse, err := http.ReadResponse(secondReader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the second response: %v", err)
	}
	defer func() { _ = secondResponse.Body.Close() }()
	if secondResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("second CONNECT status = %d, want 403 after the policy changed", secondResponse.StatusCode)
	}
	if policies.callCount() != 2 {
		t.Fatalf("policy reads = %d, want one per request", policies.callCount())
	}
}

func TestEgressDestinationFromRequest(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		url     string
		host    string
		want    egressDestination
		wantErr bool
	}{
		{name: "connect to tls port", method: http.MethodConnect, host: "api.example:443", want: egressDestination{host: "api.example", port: 443, protocol: egress.ProtocolHTTPS}},
		{name: "connect to plain port", method: http.MethodConnect, host: "api.example:80", want: egressDestination{host: "api.example", port: 80, protocol: egress.ProtocolHTTP}},
		{name: "connect to an opaque port", method: http.MethodConnect, host: "db.example:5432", want: egressDestination{host: "db.example", port: 5432, protocol: egress.ProtocolTCP}},
		{name: "absolute form without a port", method: http.MethodGet, url: "http://api.example/path", want: egressDestination{host: "api.example", port: 80, protocol: egress.ProtocolHTTP}},
		{name: "absolute form with a port", method: http.MethodGet, url: "http://api.example:8080/path", want: egressDestination{host: "api.example", port: 8080, protocol: egress.ProtocolHTTP}},
		{name: "absolute form https without a port", method: http.MethodGet, url: "https://api.example/path", want: egressDestination{host: "api.example", port: 443, protocol: egress.ProtocolHTTPS}},
		{name: "absolute form https with a port", method: http.MethodGet, url: "https://api.example:8443/path", want: egressDestination{host: "api.example", port: 8443, protocol: egress.ProtocolHTTPS}},
		{name: "absolute form with an unsupported scheme", method: http.MethodGet, url: "ftp://api.example/path", wantErr: true},
		// An explicit zero is preserved so endpoint validation refuses it,
		// rather than silently becoming the protocol's default port.
		{name: "absolute form with an explicit zero port", method: http.MethodGet, url: "http://api.example:0/path", want: egressDestination{host: "api.example", port: 0, protocol: egress.ProtocolHTTP}},
		{name: "upper case host is normalized", method: http.MethodConnect, host: "API.Example:443", want: egressDestination{host: "api.example", port: 443, protocol: egress.ProtocolHTTPS}},
		{name: "connect without a port", method: http.MethodConnect, host: "api.example", wantErr: true},
		{name: "no destination at all", method: http.MethodGet, wantErr: true},
		{name: "port is not a number", method: http.MethodConnect, host: "api.example:https", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &http.Request{Method: test.method, Header: http.Header{}, Host: test.host}
			if test.url != "" {
				parsed, err := url.Parse(test.url)
				if err != nil {
					t.Fatalf("parsing %q: %v", test.url, err)
				}
				request.URL = parsed
			} else {
				request.URL = &url.URL{}
			}
			if test.host != "" {
				request.URL.Host = test.host
			}
			got, err := egressDestinationFromRequest(request)
			if test.wantErr {
				if err == nil {
					t.Fatalf("egressDestinationFromRequest() = %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("egressDestinationFromRequest() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("egressDestinationFromRequest() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestEgressDispatchHandlerRoutesOnlyProxyTraffic(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		target    string
		proxyAuth bool
		want      string
	}{
		{name: "absolute form goes to the proxy", method: http.MethodGet, target: "http://api.example/", want: "egress"},
		{name: "connect with a credential goes to the proxy", method: http.MethodConnect, target: "api.example:443", proxyAuth: true, want: "egress"},
		{name: "connect without a credential stays with the daemon", method: http.MethodConnect, target: "api.example:443", want: "daemon"},
		{name: "origin form stays with the daemon", method: http.MethodPost, target: "/agentcompose.v2.AgentService/GetVersion", want: "daemon"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			daemon := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("daemon")) })
			egressHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("egress")) })
			handler := EgressDispatchHandler(daemon, egressHandler)
			request := httptest.NewRequest(test.method, test.target, nil)
			if test.proxyAuth {
				request.Header.Set("Proxy-Authorization", proxyBasicAuth(testSandboxID, testCredential))
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if got := recorder.Body.String(); got != test.want {
				t.Fatalf("routed to %q, want %q", got, test.want)
			}
		})
	}
}

func dialProxy(t *testing.T, server *httptest.Server) net.Conn {
	t.Helper()
	connection, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), 5*time.Second)
	if err != nil {
		t.Fatalf("dialing the proxy: %v", err)
	}
	return connection
}

func writeConnect(t *testing.T, connection net.Conn, target, user, secret string) {
	t.Helper()
	request := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n" +
		"Proxy-Authorization: " + proxyBasicAuth(user, secret) + "\r\n\r\n"
	if _, err := connection.Write([]byte(request)); err != nil {
		t.Fatalf("writing CONNECT: %v", err)
	}
}

// TestEgressProxyPrefersIPv4ForADualStackDestination pins the compatibility
// choice: every answer is still recorded for policy matching and IPv6-only
// names work, but a dual-stack name is dialed over IPv4 when that answer exists
// so enabling IPv6 does not break a host with no IPv6 route.
func TestEgressProxyPrefersIPv4ForADualStackDestination(t *testing.T) {
	policies := &proxyTestPolicies{policies: []egress.Policy{egress.NewPolicy(egress.Allow)}}
	var dialed string
	proxyServer, _ := newTestEgressProxy(t, policies, func(options *EgressProxyOptions) {
		options.LookupIP = func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP("203.0.113.10")}, nil
		}
		options.Dial = func(_ context.Context, _, address string) (net.Conn, error) {
			dialed = address
			return nil, errors.New("no dialer configured for this test")
		}
	})
	server := httptest.NewServer(proxyServer)
	defer server.Close()

	connection := dialProxy(t, server)
	defer func() { _ = connection.Close() }()
	writeConnect(t, connection, "dual.example:443", testSandboxID, testCredential)
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("CONNECT status = %d, want 502 after the policy allowed the destination", response.StatusCode)
	}
	if dialed != "203.0.113.10:443" {
		t.Fatalf("dialed address = %q, want the IPv4 answer", dialed)
	}
}
