package driver

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

	networkapi "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// dockerFakeEgressProxy is a CONNECT proxy started by the test process on the
// host. Docker sandboxes reach it through the bridge gateway, which is exactly
// how init reaches a daemon-side proxy.
type dockerFakeEgressProxy struct {
	listener net.Listener
	status   string
	echo     bool

	mu       sync.Mutex
	requests []string
}

func startDockerFakeEgressProxy(t *testing.T, status string, echo bool) *dockerFakeEgressProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &dockerFakeEgressProxy{listener: listener, status: status, echo: echo}
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

func (p *dockerFakeEgressProxy) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	header, err := readDockerProxyHeader(bufio.NewReader(conn))
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

func (p *dockerFakeEgressProxy) port() int {
	return p.listener.Addr().(*net.TCPAddr).Port
}

func (p *dockerFakeEgressProxy) received() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

func readDockerProxyHeader(reader *bufio.Reader) (string, error) {
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

// TestDockerSandboxInitEgressEnforceIntegration is opt-in: it needs a Docker
// daemon and a guest image with the enforce-aware ac-init. It proves a
// proxy-unaware client is redirected through the proxy, that the proxy
// credential is gone from the workload environment, and that a refusal is
// returned to the client instead of a direct connection.
func TestDockerSandboxInitEgressEnforceIntegration(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE"))
	if image == "" {
		t.Skip("set AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE to a guest image with the enforce-aware ac-init")
	}
	hostTmp := hostVisibleTestDir(t)
	gateway := dockerBridgeGatewayIP(t)
	proxy := startDockerFakeEgressProxy(t, "200 Connection Established", true)
	proxyURL := fmt.Sprintf("http://sandbox-1:secret@%s:%d", gateway, proxy.port())
	runtime, sandbox := startDockerInitSandboxForTest(t, hostTmp, image, "enforce",
		SandboxEnvVar{Name: "HTTP_PROXY", Value: proxyURL},
		SandboxEnvVar{Name: "HTTPS_PROXY", Value: proxyURL},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	connectCommand := `python3 -c 'import os, socket; print("proxy-env", os.environ.get("HTTP_PROXY")); s=socket.create_connection(("203.0.113.9",443),5); s.sendall(b"ping"); print("tunnel-ok", s.recv(4).decode()); s.close()'`
	result, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
		Command: "/bin/sh",
		Args:    []string{"-c", connectCommand},
		Cwd:     "/",
	})
	if err != nil {
		t.Fatalf("Exec(mediated connect) error = %v; stderr: %s", err, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "proxy-env None") {
		t.Fatalf("workload output = %q, want no inherited HTTP_PROXY", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "tunnel-ok ping") {
		t.Fatalf("workload output = %q, want the tunneled echo", result.Stdout)
	}
	requests := proxy.received()
	if len(requests) != 1 {
		t.Fatalf("proxy requests = %d, want 1:\n%s", len(requests), strings.Join(requests, "\n"))
	}
	for _, want := range []string{
		"CONNECT 203.0.113.9:443 HTTP/1.1",
		"Proxy-Authorization: Basic ",
		"X-Agent-Compose-Sandbox: " + sandbox.Summary.ID,
	} {
		if !strings.Contains(requests[0], want) {
			t.Fatalf("CONNECT request does not contain %q:\n%s", want, requests[0])
		}
	}
}

func TestDockerSandboxInitEgressEnforceDenyIntegration(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE"))
	if image == "" {
		t.Skip("set AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE to a guest image with the enforce-aware ac-init")
	}
	hostTmp := hostVisibleTestDir(t)
	gateway := dockerBridgeGatewayIP(t)
	proxy := startDockerFakeEgressProxy(t, "403 Forbidden", false)
	proxyURL := fmt.Sprintf("http://sandbox-1:secret@%s:%d", gateway, proxy.port())
	runtime, sandbox := startDockerInitSandboxForTest(t, hostTmp, image, "enforce",
		SandboxEnvVar{Name: "HTTP_PROXY", Value: proxyURL},
		SandboxEnvVar{Name: "HTTPS_PROXY", Value: proxyURL},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	connectCommand := `python3 -c 'import socket
try:
    socket.create_connection(("203.0.113.9",443),5)
    print("unexpected-success")
except ConnectionRefusedError:
    print("denied")'`
	result, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
		Command: "/bin/sh",
		Args:    []string{"-c", connectCommand},
		Cwd:     "/",
	})
	if err != nil {
		t.Fatalf("Exec(denied connect) error = %v; stderr: %s", err, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "denied") {
		t.Fatalf("workload output = %q, want a refused connect", result.Stdout)
	}
	if len(proxy.received()) != 1 {
		t.Fatalf("proxy requests = %d, want the refused CONNECT", len(proxy.received()))
	}
}

// dockerBridgeGatewayIP returns the address a Docker sandbox uses to reach the
// host. The sandbox network falls back to the default bridge when the daemon is
// not itself containerized, which is the case for these host integration tests.
func dockerBridgeGatewayIP(t *testing.T) string {
	t.Helper()
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	defer func() { _ = dockerClient.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	network, err := dockerClient.NetworkInspect(ctx, "bridge", networkapi.InspectOptions{})
	if err == nil && len(network.IPAM.Config) > 0 && strings.TrimSpace(network.IPAM.Config[0].Gateway) != "" {
		return strings.TrimSpace(network.IPAM.Config[0].Gateway)
	}
	return "172.17.0.1"
}

// TestDockerSandboxInitEgressPolicyDNSIntegration proves a proxy-unaware client
// resolves a policy name through the sandbox DNS stub, connects to the
// synthetic address, and reaches the proxy with the original hostname.
func TestDockerSandboxInitEgressPolicyDNSIntegration(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE"))
	if image == "" {
		t.Skip("set AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE to a guest image with the enforce-aware ac-init")
	}
	hostTmp := hostVisibleTestDir(t)
	gateway := dockerBridgeGatewayIP(t)
	proxy := startDockerFakeEgressProxy(t, "200 Connection Established", true)
	proxyURL := fmt.Sprintf("http://sandbox-1:secret@%s:%d", gateway, proxy.port())
	runtime, sandbox := startDockerInitSandboxForTest(t, hostTmp, image, "enforce",
		SandboxEnvVar{Name: "HTTP_PROXY", Value: proxyURL},
		SandboxEnvVar{Name: "HTTPS_PROXY", Value: proxyURL},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	command := `python3 -c 'import socket
infos = socket.getaddrinfo("allowed.example", 443, 0, socket.SOCK_STREAM)
address = infos[0][4][0]
print("synthetic", address)
s = socket.create_connection((address, 443), 5)
s.sendall(b"ping")
print("tunnel-ok", s.recv(4).decode())
s.close()'`
	result, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
		Command: "/bin/sh",
		Args:    []string{"-c", command},
		Cwd:     "/",
	})
	if err != nil {
		t.Fatalf("Exec(DNS + mediated connect) error = %v; stderr: %s", err, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "synthetic 198.1") {
		t.Fatalf("workload output = %q, want a synthetic 198.18.0.0/15 answer", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "tunnel-ok ping") {
		t.Fatalf("workload output = %q, want the tunneled echo", result.Stdout)
	}
	requests := proxy.received()
	if len(requests) != 1 || !strings.Contains(requests[0], "CONNECT allowed.example:443 HTTP/1.1") {
		t.Fatalf("proxy requests = %v, want CONNECT allowed.example:443", requests)
	}
}

// TestDockerSandboxInitEgressPolicyDNSDatagramIntegration exercises resolvers
// that use unconnected UDP sendto/sendmsg instead of connect.
func TestDockerSandboxInitEgressPolicyDNSDatagramIntegration(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE"))
	if image == "" {
		t.Skip("set AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE to a guest image with the enforce-aware ac-init")
	}
	hostTmp := hostVisibleTestDir(t)
	gateway := dockerBridgeGatewayIP(t)
	proxy := startDockerFakeEgressProxy(t, "200 Connection Established", true)
	proxyURL := fmt.Sprintf("http://sandbox-1:secret@%s:%d", gateway, proxy.port())
	runtime, sandbox := startDockerInitSandboxForTest(t, hostTmp, image, "enforce",
		SandboxEnvVar{Name: "HTTP_PROXY", Value: proxyURL},
		SandboxEnvVar{Name: "HTTPS_PROXY", Value: proxyURL},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	command := `python3 -c 'import struct, socket
def query(name):
    header = struct.pack("!HHHHHH", 0x1234, 0x0100, 1, 0, 0, 0)
    qname = b"".join(bytes([len(p)]) + p.encode() for p in name.split(".")) + b"\x00"
    return header + qname + struct.pack("!HH", 1, 1)
def skip_name(data, off):
    while True:
        length = data[off]
        if length == 0:
            return off + 1
        if length & 0xC0 == 0xC0:
            return off + 2
        off += 1 + length
def first_ip(data):
    off = skip_name(data, 12) + 4
    off = skip_name(data, off) + 10
    return socket.inet_ntoa(data[off:off+4])
message = query("allowed.example")
a = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
a.settimeout(5)
a.sendto(message, ("8.8.8.8", 53))
data, _ = a.recvfrom(4096)
print("sendto", first_ip(data))
b = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
b.settimeout(5)
b.sendmsg([message], [], 0, ("8.8.8.8", 53))
data, _ = b.recvfrom(4096)
print("sendmsg", first_ip(data))'`
	result, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
		Command: "/bin/sh",
		Args:    []string{"-c", command},
		Cwd:     "/",
	})
	if err != nil {
		t.Fatalf("Exec(UDP DNS) error = %v; stderr: %s", err, result.Stderr)
	}
	for _, want := range []string{"sendto 198.1", "sendmsg 198.1"} {
		if !strings.Contains(result.Stdout, want) {
			t.Fatalf("workload output = %q, want %q", result.Stdout, want)
		}
	}
}

// TestDockerSandboxInitEgressPolicyDNSNodeResolverIntegration exercises the
// c-ares resolver Node uses for dns.resolve4, which is a different syscall path
// from the libc getaddrinfo resolver.
func TestDockerSandboxInitEgressPolicyDNSNodeResolverIntegration(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE"))
	if image == "" {
		t.Skip("set AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE to a guest image with the enforce-aware ac-init")
	}
	hostTmp := hostVisibleTestDir(t)
	gateway := dockerBridgeGatewayIP(t)
	proxy := startDockerFakeEgressProxy(t, "200 Connection Established", true)
	proxyURL := fmt.Sprintf("http://sandbox-1:secret@%s:%d", gateway, proxy.port())
	runtime, sandbox := startDockerInitSandboxForTest(t, hostTmp, image, "enforce",
		SandboxEnvVar{Name: "HTTP_PROXY", Value: proxyURL},
		SandboxEnvVar{Name: "HTTPS_PROXY", Value: proxyURL},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	command := `node -e 'require("dns").resolve4("allowed.example", function (error, addresses) { if (error) { console.log("node-resolve", error.code); return; } console.log("node-resolve", addresses.join(",")); })'`
	result, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
		Command: "/bin/sh",
		Args:    []string{"-c", command},
		Cwd:     "/",
	})
	if err != nil {
		t.Fatalf("Exec(node DNS) error = %v; stderr: %s", err, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "node-resolve 198.1") {
		t.Fatalf("workload output = %q, want a synthetic c-ares answer; init logs:\n%s", result.Stdout, sandboxInitLogs(t, runtime, sandbox, ctx))
	}
}

// sandboxInitLogs returns the init stderr tail for failure diagnostics.
func sandboxInitLogs(t *testing.T, runtime *dockerRuntime, sandbox *Sandbox, ctx context.Context) string {
	t.Helper()
	client, err := runtime.newClient()
	if err != nil {
		return "newClient: " + err.Error()
	}
	defer func() { _ = client.Close() }()
	info, ok, err := runtime.findContainer(ctx, client, sandbox, VMState{})
	if err != nil || !ok {
		return "findContainer failed"
	}
	logs, err := runtime.readContainerLogs(ctx, client, info.ID)
	if err != nil {
		return "read logs: " + err.Error()
	}
	return logs
}
