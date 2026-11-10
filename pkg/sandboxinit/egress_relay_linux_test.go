//go:build linux

package sandboxinit

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/sandboxsupervisor"
	"golang.org/x/sys/unix"
)

func TestLoadEgressRelayTargetRemovesItsEnvironment(t *testing.T) {
	t.Setenv(EgressRelaySocketEnvVar, "/run/agent-compose/supervisor.sock")
	t.Setenv(EgressEngineHostsEnvVar, `{"agent-compose":["10.0.0.2"]}`)

	relay, passthrough, err := loadEgressRelayTarget("sandbox-1")
	if err != nil {
		t.Fatalf("loadEgressRelayTarget() error = %v", err)
	}
	if relay.socketPath != "/run/agent-compose/supervisor.sock" || relay.sandboxID != "sandbox-1" {
		t.Fatalf("relay target = %+v, want the configured socket and sandbox", relay)
	}
	if relay.token != "" {
		t.Fatalf("relay token = %q, want it absent until the daemon configures the control socket", relay.token)
	}
	if got := passthrough["agent-compose"]; len(got) != 1 || got[0].String() != "10.0.0.2" {
		t.Fatalf("engine passthrough = %v, want agent-compose -> 10.0.0.2", passthrough)
	}
	for _, name := range []string{EgressRelaySocketEnvVar, EgressEngineHostsEnvVar} {
		if value := os.Getenv(name); value != "" {
			t.Fatalf("%s = %q after loading the relay, want it removed from init's environment", name, value)
		}
	}
}

func TestLoadEgressRelayTargetRequiresASocket(t *testing.T) {
	t.Setenv(EgressRelaySocketEnvVar, "")
	t.Setenv(EgressEngineHostsEnvVar, "")
	if _, _, err := loadEgressRelayTarget("sandbox-1"); err == nil {
		t.Fatal("loadEgressRelayTarget() accepted an empty relay socket")
	}
}

func TestConfigureRelayRequiresTheTokenAndWaits(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	startRelaySupervisor(t, sandboxsupervisor.ServerOptions{
		SocketPath: socket,
		Token:      "relay-token",
		SandboxID:  "sandbox-1",
		ProxyURL:   "http://sandbox-1:secret@127.0.0.1:1",
	})
	supervisor := &egressSupervisor{
		relay:      &egressRelayTarget{socketPath: socket, sandboxID: "sandbox-1", timeout: time.Second},
		relayReady: make(chan struct{}),
	}
	if err := supervisor.configureRelay(""); err == nil {
		t.Fatal("configureRelay() accepted an empty token")
	}
	if err := supervisor.configureRelay("wrong-token"); err == nil {
		t.Fatal("configureRelay() accepted a token the supervisor does not know")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- supervisor.waitForRelay(ctx, 2*time.Second) }()
	if err := supervisor.configureRelay("relay-token"); err != nil {
		t.Fatalf("configureRelay() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("waitForRelay() error = %v, want nil after configuration", err)
	}
	if got := supervisor.relayToken(); got != "relay-token" {
		t.Fatalf("relayToken() = %q, want relay-token", got)
	}
	if err := supervisor.configureRelay("different-token"); err == nil {
		t.Fatal("configureRelay() replaced an existing token")
	}
}

func TestParseEgressEngineHostsRejectsMalformedInput(t *testing.T) {
	for _, raw := range []string{`{"host":`, `{"host":["not-an-ip"]}`, `{"":["10.0.0.1"]}`} {
		if _, err := parseEgressEngineHosts(raw); err == nil {
			t.Fatalf("parseEgressEngineHosts(%q) error = nil, want a refusal", raw)
		}
	}
}

func TestRelayBridgesTheDuplicatedSocketToTheSupervisor(t *testing.T) {
	echo := startRelayEchoServer(t)
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	startRelaySupervisor(t, sandboxsupervisor.ServerOptions{
		SocketPath:      socket,
		Token:           "relay-token",
		SandboxID:       "sandbox-1",
		ProxyURL:        "http://sandbox-1:secret@127.0.0.1:1",
		DirectEndpoints: []string{echo},
	})

	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("create the workload socket: %v", err)
	}
	defer func() { _ = unix.Close(fd) }()
	host, port, err := net.SplitHostPort(echo)
	if err != nil {
		t.Fatalf("split echo address: %v", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse echo port: %v", err)
	}

	relay := &egressRelayTarget{socketPath: socket, token: "relay-token", sandboxID: "sandbox-1", timeout: 2 * time.Second}
	if err := relay.relay(fd, unix.AF_INET, EgressAuditEvent{Host: host, Port: portNumber, SessionID: "session-1"}, "relay-token"); err != nil {
		t.Fatalf("relay() error = %v", err)
	}

	conn := relayedTestConn(t, fd)
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("hello supervisor")); err != nil {
		t.Fatalf("write through the relay: %v", err)
	}
	buffer := make([]byte, len("hello supervisor"))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("read through the relay: %v", err)
	}
	if string(buffer) != "hello supervisor" {
		t.Fatalf("relayed payload = %q, want the echoed bytes", buffer)
	}
}

func TestAcceptRelayedPeerIgnoresAnotherLoopbackClient(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	wrong, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial the wrong client: %v", err)
	}
	defer func() { _ = wrong.Close() }()
	// Source port 0 can never match a real bound socket port, so the relay must
	// close the wrong connection and keep waiting until the deadline.
	_, err = acceptRelayedPeer(listener, 1, 100*time.Millisecond)
	if err == nil {
		t.Fatal("acceptRelayedPeer() accepted a connection from an unrelated source port")
	}
}

func relayedTestConn(t *testing.T, fd int) net.Conn {
	t.Helper()
	duplicate, err := unix.Dup(fd)
	if err != nil {
		t.Fatalf("duplicate the workload socket: %v", err)
	}
	file := os.NewFile(uintptr(duplicate), "relayed-socket")
	conn, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		t.Fatalf("open the relayed socket: %v", err)
	}
	return conn
}

func startRelayEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
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

func startRelaySupervisor(t *testing.T, options sandboxsupervisor.ServerOptions) {
	t.Helper()
	server, err := sandboxsupervisor.NewServer(options)
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
