package proxy

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// EgressProxyAuthorizer resolves the sandbox a proxy credential belongs to. It
// receives the sandbox name the caller claimed so an implementation can verify
// the credential against that identity rather than trusting either half alone. A
// credential that names no sandbox (unknown, revoked, expired, malformed) is an
// error: the proxy never decides for an unidentified caller.
type EgressProxyAuthorizer interface {
	AuthorizeEgressProxy(ctx context.Context, claimedSandboxID, credential string) (sandboxID string, err error)
}

// EgressProxyPolicySource returns the egress policy that currently governs one
// sandbox. The proxy reads it per request rather than caching it for the life
// of a tunnel, which is what makes a policy change take effect on a sandbox
// that is already running.
type EgressProxyPolicySource interface {
	EgressProxyPolicy(ctx context.Context, sandboxID string) (egress.Policy, error)
}

// EgressProxyRecorder receives one account of every decision. It is optional: a
// composition without one still enforces, it just does not persist the record.
type EgressProxyRecorder interface {
	RecordEgressDecision(ctx context.Context, record egress.Record)
}

// EgressProxyOptions configures an EgressProxy. Authorizer and Policies are
// required; the remaining fields have production defaults.
type EgressProxyOptions struct {
	Authorizer EgressProxyAuthorizer
	Policies   EgressProxyPolicySource
	Recorder   EgressProxyRecorder
	Logger     *slog.Logger
	// LookupIP resolves the destination of a request. It defaults to the
	// process resolver; a test injects one so a decision never depends on real
	// name resolution.
	LookupIP func(ctx context.Context, host string) ([]net.IP, error)
	// DialTimeout bounds the connection this proxy opens on the sandbox's
	// behalf. Zero uses DefaultEgressDialTimeout.
	DialTimeout time.Duration
	// Dial opens the upstream connection. It is nil in production, where the
	// proxy uses its own dialer; a test injects one to exercise the decision
	// and forwarding paths without reaching a real network.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// DefaultEgressDialTimeout bounds how long the proxy waits for an upstream
// connection it opened for a sandbox.
const DefaultEgressDialTimeout = 30 * time.Second

// Engine-owned rule IDs. They are stable so a decision record names the rule
// that produced it, and a deny reason never depends on a project's wording.
const (
	// EgressRuleBlockedAddress denies a destination that resolves to an
	// address the engine never forwards to, whatever the policy says. It is
	// what keeps the proxy from becoming a new way to reach the daemon's own
	// loopback services or a cloud metadata endpoint.
	EgressRuleBlockedAddress = "engine.blocked-address"
	// EgressRulePolicyUnavailable denies when the sandbox's policy cannot be
	// read. An unreadable declaration is not an absent one, so the proxy denies
	// rather than forwarding on a policy it could not evaluate.
	EgressRulePolicyUnavailable = "engine.policy-unavailable"
	// EgressRuleUnresolvedAddress denies when the destination does not resolve.
	EgressRuleUnresolvedAddress = "engine.unresolved-address"
	// EgressRuleBlockedPort denies a control-plane port the engine never
	// forwards to, whatever the policy says. A sandbox that could reach a
	// cluster's etcd or kubelet through the proxy would have a path to the
	// credentials and the workloads of the platform hosting it, so the
	// declaration cannot buy that path back.
	EgressRuleBlockedPort = "engine.blocked-port"
)

// blockedEnginePorts is the set of control-plane ports the proxy refuses
// unconditionally, with the reason recorded in the decision. They are named by
// role rather than by address because the address is often dynamic, which is
// exactly why an allow list cannot be trusted to exclude them.
var blockedEnginePorts = map[int]string{
	2379:  "etcd client",
	2380:  "etcd peer",
	6443:  "Kubernetes API server",
	10250: "kubelet API",
	10255: "kubelet read-only API",
}

// EgressProxy is the daemon-side forward proxy a sandbox's outbound traffic
// passes through. It authenticates the sandbox, decides the destination against
// that sandbox's current policy, and only then opens the connection, so the
// declared policy is enforced by the engine instead of by a driver feature.
//
// It is an HTTP forward proxy: it accepts absolute-form requests and CONNECT
// tunnels. It is not an open proxy — a request without a sandbox credential is
// answered 407 and never decided.
type EgressProxy struct {
	authorizer  EgressProxyAuthorizer
	policies    EgressProxyPolicySource
	recorder    EgressProxyRecorder
	logger      *slog.Logger
	lookupIP    func(ctx context.Context, host string) ([]net.IP, error)
	dial        func(ctx context.Context, network, address string) (net.Conn, error)
	dialTimeout time.Duration
}

// NewEgressProxy validates the required dependencies and returns the proxy.
func NewEgressProxy(options EgressProxyOptions) (*EgressProxy, error) {
	if options.Authorizer == nil {
		return nil, errors.New("egress proxy requires an authorizer")
	}
	if options.Policies == nil {
		return nil, errors.New("egress proxy requires a policy source")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	lookupIP := options.LookupIP
	if lookupIP == nil {
		lookupIP = lookupHostAddresses
	}
	dialTimeout := options.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = DefaultEgressDialTimeout
	}
	dial := options.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: dialTimeout}
		dial = dialer.DialContext
	}
	return &EgressProxy{
		authorizer:  options.Authorizer,
		policies:    options.Policies,
		recorder:    options.Recorder,
		logger:      logger,
		lookupIP:    lookupIP,
		dial:        dial,
		dialTimeout: dialTimeout,
	}, nil
}

// egressDestination is the destination a proxied request asked for, before it
// is resolved or decided.
type egressDestination struct {
	host     string
	port     int
	protocol egress.Protocol
}

// ServeHTTP implements the forward-proxy surface. Every decision it makes —
// including a refusal that comes from the engine rather than from the declared
// policy — is accounted for through the recorder, because a blocked attempt is
// exactly what an operator needs to see.
func (p *EgressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sandboxID, ok := p.authenticate(w, r)
	if !ok {
		return
	}
	destination, err := egressDestinationFromRequest(r)
	if err != nil {
		http.Error(w, "agent-compose egress proxy: "+err.Error(), http.StatusBadRequest)
		return
	}
	endpoint, err := egress.NewEndpoint(destination.host, destination.port, destination.protocol)
	if err != nil {
		http.Error(w, "agent-compose egress proxy: "+err.Error(), http.StatusBadRequest)
		return
	}
	request := egress.NetworkRequest(sandboxID, endpoint)

	policy, err := p.policies.EgressProxyPolicy(r.Context(), sandboxID)
	if err != nil {
		// The policy could not be read, so nothing may be forwarded: an
		// unreadable declaration is not an absent one.
		p.deny(w, r, egressDenial{sandboxID: sandboxID, destination: destination, request: request, result: egress.Result{
			Action: egress.Deny,
			RuleID: EgressRulePolicyUnavailable,
			Reason: "the sandbox network policy could not be read",
		}})
		p.logger.WarnContext(r.Context(), "egress proxy denied a request because the sandbox policy is unavailable",
			"sandbox_id", sandboxID, "host", destination.host, "port", destination.port, "error", err)
		return
	}

	addresses, err := p.resolve(r.Context(), destination.host)
	if err != nil {
		reason := "the destination name did not resolve"
		rule := EgressRuleUnresolvedAddress
		p.deny(w, r, egressDenial{sandboxID: sandboxID, destination: destination, request: request, result: egress.Result{
			Action:     egress.Deny,
			RuleID:     rule,
			Reason:     reason,
			Generation: policy.Generation(),
		}})
		p.logger.WarnContext(r.Context(), "egress proxy could not resolve a destination",
			"sandbox_id", sandboxID, "host", destination.host, "error", err)
		return
	}
	if role, blocked := blockedEnginePorts[destination.port]; blocked {
		p.deny(w, r, egressDenial{sandboxID: sandboxID, destination: destination, request: request, result: egress.Result{
			Action:     egress.Deny,
			RuleID:     EgressRuleBlockedPort,
			Reason:     fmt.Sprintf("port %d is the %s control-plane port, which the engine never forwards to", destination.port, role),
			Generation: policy.Generation(),
		}})
		return
	}
	if blocked := firstBlockedAddress(addresses); blocked != nil {
		p.deny(w, r, egressDenial{sandboxID: sandboxID, destination: destination, request: request, result: egress.Result{
			Action:     egress.Deny,
			RuleID:     EgressRuleBlockedAddress,
			Reason:     fmt.Sprintf("the destination resolves to %s, an address the engine never forwards to", blocked),
			Generation: policy.Generation(),
		}})
		return
	}

	// The addresses are the ones the rule constrains and the ones the proxy will
	// dial, so the decision and the connection cannot disagree about either.
	request.ResolvedIPs = make([]string, 0, len(addresses))
	for _, address := range addresses {
		request.ResolvedIPs = append(request.ResolvedIPs, address.String())
	}
	result := egress.Decide(policy, request)
	if !result.Allowed() {
		p.deny(w, r, egressDenial{sandboxID: sandboxID, destination: destination, request: request, result: result})
		return
	}
	p.record(r.Context(), request, result)
	target := addresses[0]
	if err := p.forward(w, r, destination, target); err != nil {
		p.logger.WarnContext(r.Context(), "egress proxy forward failed",
			"sandbox_id", sandboxID, "host", destination.host, "port", destination.port,
			"address", target.String(), "rule", result.RuleID, "error", err)
	}
}

// authenticate resolves the calling sandbox from the proxy credential. It
// writes the response and reports false when the caller is not an authenticated
// sandbox. The user part of Basic credentials names the sandbox and is checked
// against the resolved identity, so a credential cannot be replayed under
// another sandbox's name.
func (p *EgressProxy) authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	claimed, credential, ok := parseProxyCredential(r.Header.Get("Proxy-Authorization"))
	if !ok {
		w.Header().Set("Proxy-Authenticate", `Basic realm="agent-compose-egress"`)
		http.Error(w, "agent-compose egress proxy: a sandbox credential is required", http.StatusProxyAuthRequired)
		return "", false
	}
	sandboxID, err := p.authorizer.AuthorizeEgressProxy(r.Context(), claimed, credential)
	if err != nil || strings.TrimSpace(sandboxID) == "" {
		w.Header().Set("Proxy-Authenticate", `Basic realm="agent-compose-egress"`)
		http.Error(w, "agent-compose egress proxy: the sandbox credential is not valid", http.StatusProxyAuthRequired)
		if err != nil {
			p.logger.WarnContext(r.Context(), "egress proxy rejected a credential", "error", err)
		}
		return "", false
	}
	if claimed != "" && subtle.ConstantTimeCompare([]byte(claimed), []byte(sandboxID)) != 1 {
		http.Error(w, "agent-compose egress proxy: the credential does not belong to the named sandbox", http.StatusForbidden)
		return "", false
	}
	return sandboxID, true
}

// resolve returns every address a destination has, IPv4 and IPv6 alike. The
// proxy records all of them for policy matching and dials the first one, with
// IPv4 answers placed first: a dual-stack name keeps the pre-IPv6 behavior when
// IPv4 is available, while an IPv6-only destination is reachable wherever the
// runtime network supports it instead of being rejected for lacking an IPv4
// answer.
func (p *EgressProxy) resolve(ctx context.Context, host string) ([]net.IP, error) {
	if literal := net.ParseIP(strings.Trim(host, "[]")); literal != nil {
		return []net.IP{literal}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, p.dialTimeout)
	defer cancel()
	ips, err := p.lookupIP(ctx, host)
	if err != nil {
		return nil, err
	}
	ipv4 := make([]net.IP, 0, len(ips))
	ipv6 := make([]net.IP, 0, len(ips))
	for _, address := range ips {
		if address == nil {
			continue
		}
		if four := address.To4(); four != nil {
			ipv4 = append(ipv4, four)
			continue
		}
		ipv6 = append(ipv6, address)
	}
	addresses := append(ipv4, ipv6...)
	if len(addresses) == 0 {
		return nil, fmt.Errorf("no address for %q", host)
	}
	return addresses, nil
}

// lookupHostAddresses is the production resolver: the process resolver, read
// through the same shape tests inject.
func lookupHostAddresses(ctx context.Context, host string) ([]net.IP, error) {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ips = append(ips, address.IP)
	}
	return ips, nil
}

// egressDenial is one refused attempt: who asked, for what, and the decision
// that refused it. It is bundled because a refusal and its record are one fact,
// and every refusal path must produce both.
type egressDenial struct {
	sandboxID   string
	destination egressDestination
	request     egress.Request
	result      egress.Result
}

// deny writes the refusal and records the decision behind it.
func (p *EgressProxy) deny(w http.ResponseWriter, r *http.Request, denial egressDenial) {
	p.record(r.Context(), denial.request, denial.result)
	reason := strings.TrimSpace(denial.result.Reason)
	if reason == "" {
		reason = "the sandbox network policy does not allow this destination"
	}
	w.Header().Set("X-Agent-Compose-Egress-Rule", denial.result.RuleID)
	w.Header().Set("X-Agent-Compose-Egress-Reason", reason)
	http.Error(w, fmt.Sprintf("agent-compose egress proxy: %s (rule %s)", reason, denial.result.RuleID), http.StatusForbidden)
	p.logger.InfoContext(r.Context(), "egress proxy denied a request",
		"sandbox_id", denial.sandboxID, "host", denial.destination.host, "port", denial.destination.port,
		"protocol", string(denial.destination.protocol), "rule", denial.result.RuleID, "reason", reason)
}

func (p *EgressProxy) record(ctx context.Context, request egress.Request, result egress.Result) {
	if p.recorder == nil {
		return
	}
	p.recorder.RecordEgressDecision(ctx, egress.NewRecord(request, result))
}

// forward delivers an allowed request. A CONNECT tunnel is established against
// the address the proxy resolved, so the sandbox cannot re-resolve the name to
// a different address after the decision; an absolute-form HTTP request is
// forwarded over a connection pinned the same way.
func (p *EgressProxy) forward(w http.ResponseWriter, r *http.Request, destination egressDestination, target net.IP) error {
	if r.Method == http.MethodConnect {
		return p.tunnel(w, r, destination, target)
	}
	return p.forwardHTTP(w, r, destination, target)
}

func (p *EgressProxy) tunnel(w http.ResponseWriter, r *http.Request, destination egressDestination, target net.IP) error {
	if r.ProtoMajor != 1 {
		http.Error(w, "agent-compose egress proxy: CONNECT requires HTTP/1.1", http.StatusHTTPVersionNotSupported)
		return nil
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "agent-compose egress proxy: this listener cannot tunnel", http.StatusInternalServerError)
		return errors.New("response writer does not support hijacking")
	}
	upstream, err := p.dial(r.Context(), "tcp", net.JoinHostPort(target.String(), fmt.Sprint(destination.port)))
	if err != nil {
		http.Error(w, "agent-compose egress proxy: upstream connection failed", http.StatusBadGateway)
		return err
	}
	client, buffer, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return fmt.Errorf("hijack client connection: %w", err)
	}
	defer func() { _ = client.Close() }()
	defer func() { _ = upstream.Close() }()
	if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return fmt.Errorf("write tunnel response: %w", err)
	}
	if err := buffer.Flush(); err != nil {
		return fmt.Errorf("flush tunnel response: %w", err)
	}
	copyDone := make(chan struct{}, 2)
	copyOne := func(dst io.Writer, src io.Reader) {
		_, _ = io.Copy(dst, src)
		copyDone <- struct{}{}
	}
	// The client-to-upstream direction reads through the hijacked buffer, not
	// the bare connection: bytes the HTTP server already read into its bufio
	// reader would otherwise be skipped, stalling a client that sends data
	// immediately after CONNECT.
	go copyOne(upstream, buffer)
	go copyOne(client, upstream)
	<-copyDone
	return nil
}

// forwardHTTP relays one absolute-form request. The connection is pinned to the
// resolved address, and the transport never consults the environment's proxy
// settings, so a proxied request cannot loop back into this proxy.
func (p *EgressProxy) forwardHTTP(w http.ResponseWriter, r *http.Request, destination egressDestination, target net.IP) error {
	outbound := r.Clone(r.Context())
	outbound.RequestURI = ""
	outbound.Header = r.Header.Clone()
	outbound.Header.Del("Proxy-Authorization")
	outbound.Header.Del("Proxy-Connection")
	removeHopByHopHeaders(outbound.Header)
	address := net.JoinHostPort(target.String(), fmt.Sprint(destination.port))
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return p.dial(ctx, network, address)
		},
		// The dial bound only covers connecting. An upstream that accepts the
		// connection and then never answers would pin this handler goroutine, and
		// its daemon connection, for as long as the guest keeps the request open,
		// so the response has its own bound derived from the same dial budget.
		ResponseHeaderTimeout: p.dialTimeout,
		DisableKeepAlives:     true,
	}
	defer transport.CloseIdleConnections()
	response, err := transport.RoundTrip(outbound)
	if err != nil {
		http.Error(w, "agent-compose egress proxy: upstream request failed", http.StatusBadGateway)
		return err
	}
	defer func() { _ = response.Body.Close() }()
	copyHeader(w.Header(), response.Header)
	removeHopByHopHeaders(w.Header())
	w.WriteHeader(response.StatusCode)
	_, err = io.Copy(w, response.Body)
	return err
}

// egressDestinationFromRequest extracts the destination of a forward-proxy
// request. The default protocol follows the port, so a tunnel to :443 is
// decided as https and one to :80 as http; any other port is decided as tcp.
func egressDestinationFromRequest(r *http.Request) (egressDestination, error) {
	host := r.URL.Host
	if r.Method == http.MethodConnect {
		host = r.Host
	}
	if strings.TrimSpace(host) == "" {
		return egressDestination{}, errors.New("the request names no destination")
	}
	hostname := host
	port := 0
	portPresent := false
	if parsedHost, parsedPort, err := net.SplitHostPort(host); err == nil {
		hostname = parsedHost
		port, err = parsePort(parsedPort)
		if err != nil {
			return egressDestination{}, err
		}
		portPresent = true
	}
	hostname = strings.ToLower(strings.Trim(strings.TrimSpace(hostname), "[]"))
	if hostname == "" {
		return egressDestination{}, errors.New("the request names no destination host")
	}
	// Every branch below assigns the protocol or returns, so it is declared
	// without a default rather than initialized and immediately overwritten.
	var protocol egress.Protocol
	switch {
	case r.Method == http.MethodConnect:
		protocol = egress.ProtocolTCP
		switch port {
		case 443:
			protocol = egress.ProtocolHTTPS
		case 80:
			protocol = egress.ProtocolHTTP
		}
	case r.URL == nil || strings.EqualFold(r.URL.Scheme, "http"):
		protocol = egress.ProtocolHTTP
	case strings.EqualFold(r.URL.Scheme, "https"):
		// An absolute-form request names its scheme, and ignoring it would dial
		// an https request to :80 in plaintext and record it under a protocol the
		// declaration never named.
		protocol = egress.ProtocolHTTPS
	default:
		return egressDestination{}, fmt.Errorf("the request names unsupported scheme %q", r.URL.Scheme)
	}
	// A port that was present is honored even when it is invalid, so an explicit
	// :0 reaches endpoint validation and is refused instead of silently
	// becoming the protocol's default port.
	if !portPresent {
		switch protocol {
		case egress.ProtocolHTTP:
			port = 80
		case egress.ProtocolHTTPS:
			port = 443
		default:
			return egressDestination{}, errors.New("the request names no destination port")
		}
	}
	return egressDestination{host: hostname, port: port, protocol: protocol}, nil
}

func parsePort(raw string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("destination port %q is not a number", raw)
	}
	return port, nil
}

// parseProxyCredential reads a Basic proxy credential as "sandbox:secret". The
// claimed sandbox may be empty, in which case only the secret identifies the
// caller.
func parseProxyCredential(header string) (claimed, credential string, ok bool) {
	const prefix = "Basic "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	user, secret, found := strings.Cut(string(decoded), ":")
	if !found || secret == "" {
		return "", "", false
	}
	return strings.TrimSpace(user), secret, true
}

// firstBlockedAddress returns the first address the engine never forwards to.
// Loopback, link-local (which includes the cloud metadata address), and
// unspecified or multicast destinations are refused whatever the policy says,
// because the proxy would otherwise be a new path from a sandbox to the
// daemon's own services and to instance metadata.
func firstBlockedAddress(addresses []net.IP) net.IP {
	for _, address := range addresses {
		if address == nil {
			continue
		}
		switch {
		case address.IsLoopback(),
			address.IsLinkLocalUnicast(),
			address.IsLinkLocalMulticast(),
			address.IsInterfaceLocalMulticast(),
			address.IsMulticast(),
			address.IsUnspecified():
			return address
		}
	}
	return nil
}

var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func removeHopByHopHeaders(header http.Header) {
	for _, name := range hopByHopHeaders {
		header.Del(name)
	}
}

func copyHeader(destination, source http.Header) {
	for name, values := range source {
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}
