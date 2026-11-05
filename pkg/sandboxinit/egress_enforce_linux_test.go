//go:build linux

package sandboxinit

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/sys/unix"
)

// fakeEgressProxy is a host-local CONNECT proxy that lets the enforce tests
// exercise the real handshake without a daemon. It records every request so a
// test can assert init contacted it instead of the destination.
type fakeEgressProxy struct {
	listener net.Listener
	status   string
	echo     bool

	mu       sync.Mutex
	requests []string
}

func startFakeEgressProxy(t *testing.T, status string, echo bool) *fakeEgressProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &fakeEgressProxy{listener: listener, status: status, echo: echo}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go proxy.handle(conn)
		}
	}()
	return proxy
}

func (p *fakeEgressProxy) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	header, err := readProxyHeader(bufio.NewReader(conn))
	if err != nil {
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, header)
	p.mu.Unlock()
	if _, err := fmt.Fprintf(conn, "HTTP/1.1 %s\r\n\r\n", p.status); err != nil {
		return
	}
	if !strings.HasPrefix(p.status, "200") || !p.echo {
		return
	}
	payload := make([]byte, 4)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return
	}
	_, _ = conn.Write(payload)
}

func (p *fakeEgressProxy) url(user, password string) string {
	return fmt.Sprintf("http://%s:%s@%s", user, password, p.listener.Addr().String())
}

func (p *fakeEgressProxy) received() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

func readProxyHeader(reader *bufio.Reader) (string, error) {
	var builder strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		builder.WriteString(line)
		if strings.HasSuffix(builder.String(), "\r\n\r\n") {
			return builder.String(), nil
		}
		if builder.Len() > 8192 {
			return "", fmt.Errorf("proxy request header is too large")
		}
	}
}

func TestInitEgressEnforceTunnelsThroughProxy(t *testing.T) {
	if testing.CoverMode() != "" {
		// This is a host-level seccomp/helper integration test, not a unit
		// coverage test. It is run explicitly without coverage because the
		// helper's runtime and the coverage writer share the same process
		// boundary under the test harness.
		t.Skip("host seccomp helper is exercised outside the coverage shape")
	}
	requireEnforceSupport(t)
	proxy := startFakeEgressProxy(t, "200 Connection Established", true)
	t.Setenv("HTTP_PROXY", proxy.url("sandbox-1", "secret"))
	events := make(chan EgressAuditEvent, 4)
	_, socket := startTestInitWithOptions(t, Options{
		SandboxID:       "sandbox-1",
		MainShell:       "sleep 120",
		EgressMode:      EgressModeEnforce,
		EgressAuditSink: func(event EgressAuditEvent) { events <- event },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	output, err := runEgressHelperMode(ctx, socket, "echo", "203.0.113.9:443")
	if err != nil {
		t.Fatalf("run helper: %v", err)
	}
	if !strings.Contains(output, "tunnel-ok ping") {
		t.Fatalf("helper output = %q, want a tunneled echo", output)
	}
	requests := proxy.received()
	if len(requests) != 1 {
		t.Fatalf("proxy requests = %d, want 1:\n%s", len(requests), strings.Join(requests, "\n"))
	}
	request := requests[0]
	for _, want := range []string{
		"CONNECT 203.0.113.9:443 HTTP/1.1",
		"Proxy-Authorization: Basic ",
		"X-Agent-Compose-Sandbox: sandbox-1",
		"X-Agent-Compose-Session: ",
	} {
		if !strings.Contains(request, want) {
			t.Fatalf("CONNECT request does not contain %q:\n%s", want, request)
		}
	}
	select {
	case event := <-events:
		if event.Decision != "allow" || event.Host != "203.0.113.9" || event.Port != 443 {
			t.Fatalf("event = %+v, want an allowed 203.0.113.9:443", event)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no enforce event was reported")
	}
}

func TestInitEgressEnforceRemovesProxyEnvironment(t *testing.T) {
	requireEnforceSupport(t)
	proxy := startFakeEgressProxy(t, "200 Connection Established", false)
	t.Setenv("HTTP_PROXY", proxy.url("sandbox-1", "secret"))
	t.Setenv("NO_PROXY", "example.com")
	_, socket := startTestInitWithOptions(t, Options{
		SandboxID:  "sandbox-1",
		MainShell:  "sleep 120",
		EgressMode: EgressModeEnforce,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, err := runEgressHelperMode(ctx, socket, "env", "")
	if err != nil {
		t.Fatalf("run helper: %v", err)
	}
	for _, leaked := range []string{"http-proxy:http", "https-proxy:http", "no-proxy:example.com"} {
		if strings.Contains(output, leaked) {
			t.Fatalf("helper inherited proxy configuration %q:\n%s", leaked, output)
		}
	}
}

func TestInitEgressEnforceDeniesWhenProxyRefuses(t *testing.T) {
	requireEnforceSupport(t)
	proxy := startFakeEgressProxy(t, "403 Forbidden", false)
	t.Setenv("HTTP_PROXY", proxy.url("sandbox-1", "secret"))
	events := make(chan EgressAuditEvent, 4)
	_, socket := startTestInitWithOptions(t, Options{
		SandboxID:       "sandbox-1",
		MainShell:       "sleep 120",
		EgressMode:      EgressModeEnforce,
		EgressAuditSink: func(event EgressAuditEvent) { events <- event },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, err := runEgressHelperMode(ctx, socket, "dial", "203.0.113.9:443")
	if err == nil || !strings.Contains(output, "dial-error") {
		t.Fatalf("helper error = %v, output = %q; want a refused connect", err, output)
	}
	select {
	case event := <-events:
		if event.Decision != "deny" {
			t.Fatalf("event = %+v, want a denied connect", event)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no enforce event was reported")
	}
	if len(proxy.received()) != 1 {
		t.Fatalf("proxy requests = %d, want the refused CONNECT", len(proxy.received()))
	}
}

func TestInitEgressEnforceFailsClosedWhenProxyIsUnreachable(t *testing.T) {
	requireEnforceSupport(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	t.Setenv("HTTP_PROXY", "http://sandbox-1:secret@"+address)
	events := make(chan EgressAuditEvent, 4)
	_, socket := startTestInitWithOptions(t, Options{
		SandboxID:       "sandbox-1",
		MainShell:       "sleep 120",
		EgressMode:      EgressModeEnforce,
		EgressAuditSink: func(event EgressAuditEvent) { events <- event },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, err := runEgressHelperMode(ctx, socket, "dial", "203.0.113.9:443")
	if err == nil || !strings.Contains(output, "dial-error") {
		t.Fatalf("helper error = %v, output = %q; want a fail-closed refusal", err, output)
	}
	select {
	case event := <-events:
		if event.Decision != "deny" {
			t.Fatalf("event = %+v, want a denied connect", event)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no enforce event was reported")
	}
}

func TestInitEgressEnforceBypassesLoopback(t *testing.T) {
	requireEnforceSupport(t)
	proxy := startFakeEgressProxy(t, "200 Connection Established", true)
	t.Setenv("HTTP_PROXY", proxy.url("sandbox-1", "secret"))
	_, socket := startTestInitWithOptions(t, Options{
		SandboxID:  "sandbox-1",
		MainShell:  "sleep 120",
		EgressMode: EgressModeEnforce,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, err := runEgressHelperMode(ctx, socket, "dial", "127.0.0.1:1")
	if err == nil || !strings.Contains(output, "dial-error") {
		t.Fatalf("helper error = %v, output = %q; want the loopback connect to be attempted directly", err, output)
	}
	if requests := proxy.received(); len(requests) != 0 {
		t.Fatalf("proxy received %d requests for a loopback destination:\n%s", len(requests), strings.Join(requests, "\n"))
	}
}

// requireEnforceSupport skips when the host cannot run the mediated path. The
// Docker integration test exercises it inside a sandbox that has CAP_SYS_PTRACE.
func requireEnforceSupport(t *testing.T) {
	t.Helper()
	if err := egressInterceptionSupported(); err != nil {
		t.Skipf("seccomp user notification is unavailable: %v", err)
	}
	source, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	pidfd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		t.Skipf("pidfd_open is unavailable: %v", err)
	}
	defer func() { _ = unix.Close(pidfd) }()
	duplicate, err := unix.PidfdGetfd(pidfd, int(source.Fd()), 0)
	if err != nil {
		t.Skipf("pidfd_getfd is unavailable: %v", err)
	}
	_ = unix.Close(duplicate)
}

// TestDuplicateTargetFDFromNonLeaderThread is the regression test for seccomp
// notifications that report a pooled worker thread instead of the thread-group
// leader: pidfd_open rejects a non-leader TID, so the init must resolve the
// group ID before duplicating the workload socket.
func TestDuplicateTargetFDFromNonLeaderThread(t *testing.T) {
	requireEnforceSupport(t)
	// Pin the test goroutine to the leader thread so the helper below is
	// guaranteed to get a different TID.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	source, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()

	tids := make(chan int, 1)
	release := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		tids <- unix.Gettid()
		<-release
	}()
	tid := <-tids
	defer close(release)
	if tid == os.Getpid() {
		t.Skip("the test goroutine unexpectedly became the thread-group leader")
	}

	duplicate, err := duplicateTargetFD(tid, int(source.Fd()))
	if err != nil {
		t.Fatalf("duplicateTargetFD(thread %d) error = %v", tid, err)
	}
	_ = unix.Close(duplicate)
}

func TestInitEgressPolicyDNSAnswersSyntheticAddresses(t *testing.T) {
	requireEnforceSupport(t)
	t.Setenv("HTTP_PROXY", "http://sandbox-1:secret@127.0.0.1:1")
	_, socket := startTestInitWithOptions(t, Options{
		SandboxID:  "sandbox-1",
		MainShell:  "sleep 120",
		EgressMode: EgressModeEnforce,
	})
	_ = socket
	waitForDNSPort(t)

	answers := queryPolicyDNS(t, "allowed.example.", dnsmessage.TypeA)
	if len(answers) != 1 {
		t.Fatalf("A answers = %d, want one synthetic address", len(answers))
	}
	address, ok := answers[0].Body.(*dnsmessage.AResource)
	if !ok {
		t.Fatalf("answer body = %T, want A", answers[0].Body)
	}
	ip := net.IP(address.A[:])
	if ip[0] != 198 || (ip[1] != 18 && ip[1] != 19) {
		t.Fatalf("synthetic answer = %s, want a 198.18.0.0/15 address", ip)
	}
	if answers[0].Header.TTL != egressDNSRecordTTL {
		t.Fatalf("A TTL = %d, want %d", answers[0].Header.TTL, egressDNSRecordTTL)
	}

	aaaa := queryPolicyDNS(t, "allowed.example.", dnsmessage.TypeAAAA)
	if len(aaaa) != 0 {
		t.Fatalf("AAAA answers = %d, want an empty answer", len(aaaa))
	}
}

func TestInitEgressPolicyDNSRoutesHostnameThroughProxy(t *testing.T) {
	requireEnforceSupport(t)
	proxy := startFakeEgressProxy(t, "200 Connection Established", true)
	t.Setenv("HTTP_PROXY", proxy.url("sandbox-1", "secret"))
	_, socket := startTestInitWithOptions(t, Options{
		SandboxID:  "sandbox-1",
		MainShell:  "sleep 120",
		EgressMode: EgressModeEnforce,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	waitForDNSPort(t)

	output, err := runEgressHelperDNS(ctx, socket, "allowed.example")
	if err != nil {
		t.Fatalf("run helper: %v", err)
	}
	if !strings.Contains(output, "synthetic 198.1") {
		t.Fatalf("helper output = %q, want a synthetic DNS answer", output)
	}
	if !strings.Contains(output, "tunnel-ok ping") {
		t.Fatalf("helper output = %q, want the tunneled echo", output)
	}
	requests := proxy.received()
	if len(requests) != 1 || !strings.Contains(requests[0], "CONNECT allowed.example:443 HTTP/1.1") {
		t.Fatalf("proxy requests = %v, want CONNECT allowed.example:443", requests)
	}
}

func runEgressHelperDNS(ctx context.Context, socket, name string) (string, error) {
	return runEgressHelperRequest(ctx, socket, Request{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestEgressHelperProcess$"},
		Env: map[string]string{
			"GO_WANT_INIT_EGRESS_HELPER":  "1",
			"INIT_EGRESS_HELPER_MODE":     "dns-echo",
			"INIT_EGRESS_HELPER_DNS_NAME": name,
		},
	})
}

func TestInitEgressPolicyDNSPassesEngineHostThrough(t *testing.T) {
	requireEnforceSupport(t)
	t.Setenv("HTTP_PROXY", "http://sandbox-1:secret@127.0.0.1:1")
	t.Setenv("AGENT_COMPOSE_RUNTIME_BASE_URL", "http://localhost:7410")
	_, socket := startTestInitWithOptions(t, Options{
		SandboxID:  "sandbox-1",
		MainShell:  "sleep 120",
		EgressMode: EgressModeEnforce,
	})
	_ = socket
	waitForDNSPort(t)
	answers := queryPolicyDNS(t, "localhost.", dnsmessage.TypeA)
	if len(answers) == 0 {
		t.Fatal("engine hostname resolved to no address")
	}
	address, ok := answers[0].Body.(*dnsmessage.AResource)
	if !ok {
		t.Fatalf("answer body = %T, want A", answers[0].Body)
	}
	ip := net.IP(address.A[:])
	if !ip.IsLoopback() {
		t.Fatalf("engine hostname answer = %s, want the real loopback address, not a synthetic one", ip)
	}
}

func TestInitEgressPolicyDNSConnectedGetpeername(t *testing.T) {
	requireEnforceSupport(t)
	proxy := startFakeEgressProxy(t, "200 Connection Established", true)
	t.Setenv("HTTP_PROXY", proxy.url("sandbox-1", "secret"))
	_, socket := startTestInitWithOptions(t, Options{
		SandboxID:  "sandbox-1",
		MainShell:  "sleep 120",
		EgressMode: EgressModeEnforce,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	waitForDNSPort(t)
	output, err := runEgressHelperUDP(ctx, socket, "udp-connected-dns", "allowed.example")
	if err != nil {
		t.Fatalf("run helper: %v", err)
	}
	for _, want := range []string{"peer 8.8.8.8:53", "source 8.8.8.8:53", "synthetic 198.1"} {
		if !strings.Contains(output, want) {
			t.Fatalf("helper output = %q, want %q", output, want)
		}
	}
}

func waitForDNSPort(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("udp", egressDNSv4Address, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("sandbox policy DNS did not start")
}

func queryPolicyDNS(t *testing.T, name string, queryType dnsmessage.Type) []dnsmessage.Resource {
	t.Helper()
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, RecursionDesired: true})
	if err := builder.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := builder.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: queryType, Class: dnsmessage.ClassINET}); err != nil {
		t.Fatal(err)
	}
	query, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	var read int
	var response []byte
	for attempt := 0; attempt < 30; attempt++ {
		conn, err := net.DialTimeout("udp", egressDNSv4Address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
		_, _ = conn.Write(query)
		buffer := make([]byte, 4096)
		read, err = conn.Read(buffer)
		_ = conn.Close()
		if err == nil {
			response = buffer[:read]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if response == nil {
		t.Fatal("sandbox policy DNS did not answer")
	}
	var parser dnsmessage.Parser
	if _, err := parser.Start(response); err != nil {
		t.Fatal(err)
	}
	if err := parser.SkipAllQuestions(); err != nil {
		t.Fatal(err)
	}
	answers, err := parser.AllAnswers()
	if err != nil {
		t.Fatal(err)
	}
	return answers
}

func TestInitEgressPolicyDNSSendtoUnconnected(t *testing.T) {
	requireEnforceSupport(t)
	proxy := startFakeEgressProxy(t, "200 Connection Established", true)
	t.Setenv("HTTP_PROXY", proxy.url("sandbox-1", "secret"))
	_, socket := startTestInitWithOptions(t, Options{
		SandboxID:  "sandbox-1",
		MainShell:  "sleep 120",
		EgressMode: EgressModeEnforce,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	waitForDNSPort(t)
	output, err := runEgressHelperUDP(ctx, socket, "udp-dns", "allowed.example")
	if err != nil {
		t.Fatalf("run helper: %v", err)
	}
	if !strings.Contains(output, "synthetic 198.1") {
		t.Fatalf("helper output = %q, want a synthetic DNS answer", output)
	}
	if requests := proxy.received(); len(requests) != 0 {
		t.Fatalf("proxy received %d requests for a DNS datagram", len(requests))
	}
}

func TestInitEgressPolicyDNSSendmsgUnconnected(t *testing.T) {
	requireEnforceSupport(t)
	proxy := startFakeEgressProxy(t, "200 Connection Established", true)
	t.Setenv("HTTP_PROXY", proxy.url("sandbox-1", "secret"))
	_, socket := startTestInitWithOptions(t, Options{
		SandboxID:  "sandbox-1",
		MainShell:  "sleep 120",
		EgressMode: EgressModeEnforce,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	waitForDNSPort(t)
	output, err := runEgressHelperUDP(ctx, socket, "udp-sendmsg", "allowed.example")
	if err != nil {
		t.Fatalf("run helper: %v", err)
	}
	if !strings.Contains(output, "synthetic 198.1") {
		t.Fatalf("helper output = %q, want a synthetic DNS answer", output)
	}
	if requests := proxy.received(); len(requests) != 0 {
		t.Fatalf("proxy received %d requests for a DNS datagram", len(requests))
	}
}

func runEgressHelperUDP(ctx context.Context, socket, mode, name string) (string, error) {
	return runEgressHelperRequest(ctx, socket, Request{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestEgressHelperProcess$"},
		Env: map[string]string{
			"GO_WANT_INIT_EGRESS_HELPER":  "1",
			"INIT_EGRESS_HELPER_MODE":     mode,
			"INIT_EGRESS_HELPER_DNS_NAME": name,
		},
	})
}

func TestInitEgressEnforceHardeningDeniesSyscalls(t *testing.T) {
	requireEnforceSupport(t)
	proxy := startFakeEgressProxy(t, "200 Connection Established", true)
	t.Setenv("HTTP_PROXY", proxy.url("sandbox-1", "secret"))
	_, socket := startTestInitWithOptions(t, Options{
		SandboxID:  "sandbox-1",
		MainShell:  "sleep 120",
		EgressMode: EgressModeEnforce,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, err := runEgressHelperMode(ctx, socket, "hardening", "")
	if err != nil {
		t.Fatalf("run helper: %v", err)
	}
	for _, want := range []string{
		"unshare:operation not permitted",
		"mount:operation not permitted",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("helper output = %q, want %q", output, want)
		}
	}
}

func TestMarkedProcessesExistTracksDetachedProcesses(t *testing.T) {
	marker := fmt.Sprintf("test-marker-%d", time.Now().UnixNano())
	cmd := exec.Command("/bin/sleep", "30")
	cmd.Env = append(os.Environ(), markerEnv+"="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if !markedProcessesExist(marker) {
		t.Fatalf("markedProcessesExist(%q) = false while the marked process runs", marker)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if !markedProcessesExist(marker) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("markedProcessesExist(%q) = true after the marked process exited", marker)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEgressProxyConnectRequestCarriesTheBinaryHeader(t *testing.T) {
	target := &egressProxyTarget{authHeader: "Basic test"}
	request := string(target.connectRequest(EgressAuditEvent{
		SandboxID: "sandbox-1",
		Host:      "203.0.113.9",
		Port:      443,
		Binary:    "/usr/bin/python3",
	}))
	want := "X-Agent-Compose-Binary-B64: " + base64.StdEncoding.EncodeToString([]byte("/usr/bin/python3")) + "\r\n"
	if !strings.Contains(request, want) {
		t.Fatalf("CONNECT request does not carry %q:\n%s", want, request)
	}
}

func TestProcessBinaryReportsTheRunningExecutable(t *testing.T) {
	binary := processBinary(os.Getpid())
	if binary == "" || !strings.HasPrefix(binary, "/") {
		t.Fatalf("processBinary(self) = %q, want an absolute executable path", binary)
	}
	if strings.ContainsAny(binary, "\r\n") {
		t.Fatalf("processBinary(self) = %q, want no line break", binary)
	}
	if got := processBinary(0); got != "" {
		t.Fatalf("processBinary(0) = %q, want empty", got)
	}
}

func TestEgressBypassMatchesOnlyTheProxyEndpoint(t *testing.T) {
	s := &egressSupervisor{proxyHost: "10.0.0.1", proxyPort: 7410}
	if !s.isBypassed(EgressAuditEvent{Family: "inet", Host: "10.0.0.1", Port: 7410}) {
		t.Fatal("the proxy endpoint is not bypassed")
	}
	if s.isBypassed(EgressAuditEvent{Family: "inet", Host: "10.0.0.1", Port: 22}) {
		t.Fatal("another port on the proxy host is bypassed; that exposes host services to the workload")
	}
}
