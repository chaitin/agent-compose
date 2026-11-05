package sandboxsupervisor

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestServerRequiresTheSandboxToken(t *testing.T) {
	proxy := startFakeConnectProxy(t, "200 Connection Established", true)
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	startSupervisor(t, ServerOptions{
		SocketPath: socket,
		Token:      testToken,
		SandboxID:  "sandbox-1",
		ProxyURL:   "http://sandbox-1:secret@" + proxy.address(),
	})

	good := &Client{SocketPath: socket, Token: testToken, SandboxID: "sandbox-1"}
	if err := good.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() with the sandbox token error = %v, want nil", err)
	}
	bad := &Client{SocketPath: socket, Token: "wrong-token", SandboxID: "sandbox-1"}
	if err := bad.Ping(context.Background()); !errors.Is(err, ErrSupervisorUnavailable) {
		t.Fatalf("Ping() with a wrong token error = %v, want ErrSupervisorUnavailable", err)
	}
}

func TestServerRejectsARequestForAnotherSandbox(t *testing.T) {
	proxy := startFakeConnectProxy(t, "200 Connection Established", true)
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	startSupervisor(t, ServerOptions{
		SocketPath: socket,
		Token:      testToken,
		SandboxID:  "sandbox-1",
		ProxyURL:   "http://sandbox-1:secret@" + proxy.address(),
	})

	client := &Client{SocketPath: socket, Token: testToken, SandboxID: "sandbox-2"}
	if err := client.Ping(context.Background()); !errors.Is(err, ErrSupervisorUnavailable) {
		t.Fatalf("Ping() for another sandbox error = %v, want ErrSupervisorUnavailable", err)
	}
}

func TestServerRelaysThroughTheDaemonProxyWithIdentity(t *testing.T) {
	proxy := startFakeConnectProxy(t, "200 Connection Established", true)
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	startSupervisor(t, ServerOptions{
		SocketPath: socket,
		Token:      testToken,
		SandboxID:  "sandbox-1",
		ProxyURL:   "http://sandbox-1:secret@" + proxy.address(),
	})

	client := &Client{SocketPath: socket, Token: testToken, SandboxID: "sandbox-1", Timeout: 2 * time.Second}
	conn, err := client.Connect(context.Background(), "allowed.example:443", Identity{
		SessionID: "session-1",
		Marker:    "marker-1",
		PID:       42,
		Binary:    "/usr/bin/curl",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v, want nil", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write([]byte("hello through the relay")); err != nil {
		t.Fatalf("write relayed bytes: %v", err)
	}
	buffer := make([]byte, len("hello through the relay"))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("read relayed bytes: %v", err)
	}
	if string(buffer) != "hello through the relay" {
		t.Fatalf("relayed bytes = %q, want the echoed payload", buffer)
	}

	request := proxy.lastRequest()
	for _, want := range []string{
		"CONNECT allowed.example:443 HTTP/1.1",
		"Proxy-Authorization: Basic ",
		"X-Agent-Compose-Sandbox: sandbox-1",
		"X-Agent-Compose-Session: session-1",
		"X-Agent-Compose-Marker: marker-1",
		"X-Agent-Compose-Pid: 42",
		"X-Agent-Compose-Binary-B64: ",
	} {
		if !strings.Contains(request, want) {
			t.Fatalf("proxy CONNECT request %q does not contain %q", request, want)
		}
	}
}

func TestServerFailsClosedWhenTheProxyRefuses(t *testing.T) {
	proxy := startFakeConnectProxy(t, "403 Forbidden", false)
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	startSupervisor(t, ServerOptions{
		SocketPath: socket,
		Token:      testToken,
		SandboxID:  "sandbox-1",
		ProxyURL:   "http://sandbox-1:secret@" + proxy.address(),
	})

	client := &Client{SocketPath: socket, Token: testToken, SandboxID: "sandbox-1", Timeout: 2 * time.Second}
	conn, err := client.Connect(context.Background(), "denied.example:443", Identity{})
	if err == nil {
		_ = conn.Close()
		t.Fatal("Connect() error = nil, want a refusal when the proxy denies the destination")
	}
	if !errors.Is(err, ErrSupervisorUnavailable) {
		t.Fatalf("Connect() error = %v, want ErrSupervisorUnavailable", err)
	}
}

func TestServerConnectsEngineEndpointsDirectly(t *testing.T) {
	proxy := startFakeConnectProxy(t, "200 Connection Established", false)
	echo := startEchoServer(t)
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	startSupervisor(t, ServerOptions{
		SocketPath:      socket,
		Token:           testToken,
		SandboxID:       "sandbox-1",
		ProxyURL:        "http://sandbox-1:secret@" + proxy.address(),
		DirectEndpoints: []string{echo},
	})

	client := &Client{SocketPath: socket, Token: testToken, SandboxID: "sandbox-1", Timeout: 2 * time.Second}
	conn, err := client.Connect(context.Background(), echo, Identity{})
	if err != nil {
		t.Fatalf("Connect() to a direct engine endpoint error = %v, want nil", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("engine")); err != nil {
		t.Fatalf("write engine bytes: %v", err)
	}
	buffer := make([]byte, len("engine"))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("read engine bytes: %v", err)
	}
	if string(buffer) != "engine" {
		t.Fatalf("engine echo = %q, want %q", buffer, "engine")
	}
	if proxy.requestCount() != 0 {
		t.Fatalf("proxy received %d requests for a direct engine endpoint, want 0", proxy.requestCount())
	}
}

func TestServerSocketIsPrivate(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "nested", "supervisor.sock")
	startSupervisor(t, ServerOptions{
		SocketPath: socket,
		Token:      testToken,
		SandboxID:  "sandbox-1",
		ProxyURL:   "http://sandbox-1:secret@127.0.0.1:1",
	})
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatalf("stat supervisor socket: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("supervisor socket mode = %o, want 600", mode)
	}
	dirInfo, err := os.Stat(filepath.Dir(socket))
	if err != nil {
		t.Fatalf("stat supervisor socket directory: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("supervisor socket directory mode = %o, want 700", mode)
	}
}

func TestReadRequestRejectsAnOversizedHandshake(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(strings.Repeat("a", MaxHandshakeBytes+10)))
	if _, err := ReadRequest(reader); !errors.Is(err, ErrHandshakeTooLarge) {
		t.Fatalf("ReadRequest() error = %v, want ErrHandshakeTooLarge", err)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := map[string]struct {
		raw     string
		want    string
		wantErr bool
	}{
		"host and port":       {raw: "Example.COM:443", want: "example.com:443"},
		"ipv4":                {raw: "10.0.0.1:80", want: "10.0.0.1:80"},
		"ipv6":                {raw: "[::1]:7410", want: "[::1]:7410"},
		"missing port":        {raw: "example.com", wantErr: true},
		"port zero":           {raw: "example.com:0", wantErr: true},
		"port out of range":   {raw: "example.com:70000", wantErr: true},
		"empty":               {raw: " ", wantErr: true},
		"control characters":  {raw: "exa\nmple.com:443", wantErr: true},
		"trailing whitespace": {raw: " example.com:443 ", want: "example.com:443"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := NormalizeEndpoint(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatalf("NormalizeEndpoint(%q) = %q, want an error", test.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeEndpoint(%q) error = %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("NormalizeEndpoint(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func startSupervisor(t *testing.T, options ServerOptions) {
	t.Helper()
	server, err := NewServer(options)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(options.SocketPath); err == nil {
			break
		}
		select {
		case err := <-done:
			cancel()
			t.Fatalf("Server.Run() exited before binding the socket: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("supervisor socket was not created within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("supervisor did not stop within 2s")
		}
	})
}

type fakeConnectProxy struct {
	listener net.Listener
	status   string
	echo     bool

	mu       sync.Mutex
	requests []string
}

func startFakeConnectProxy(t *testing.T, status string, echo bool) *fakeConnectProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake proxy: %v", err)
	}
	proxy := &fakeConnectProxy{listener: listener, status: status, echo: echo}
	go proxy.serve()
	t.Cleanup(func() { _ = listener.Close() })
	return proxy
}

func (p *fakeConnectProxy) address() string { return p.listener.Addr().String() }

func (p *fakeConnectProxy) serve() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.handle(conn)
	}
}

func (p *fakeConnectProxy) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	var request strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		request.WriteString(line)
		if strings.TrimRight(line, "\r\n") == "" {
			break
		}
	}
	p.mu.Lock()
	p.requests = append(p.requests, request.String())
	p.mu.Unlock()
	if _, err := conn.Write([]byte("HTTP/1.1 " + p.status + "\r\n\r\n")); err != nil {
		return
	}
	if !p.echo {
		return
	}
	buffer := make([]byte, 32*1024)
	for {
		read, err := reader.Read(buffer)
		if read > 0 {
			if _, writeErr := conn.Write(buffer[:read]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *fakeConnectProxy) lastRequest() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		return ""
	}
	return p.requests[len(p.requests)-1]
}

func (p *fakeConnectProxy) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func startEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo server: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}
