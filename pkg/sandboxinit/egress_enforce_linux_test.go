//go:build linux

package sandboxinit

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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
