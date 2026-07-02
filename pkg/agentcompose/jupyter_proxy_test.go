package agentcompose

import (
	driverpkg "agent-compose/internal/driver"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestJupyterConnectTargetUsesGuestHostWhenRoutable(t *testing.T) {
	proxyState := ProxyState{
		ProxyPath: "/agent-compose/session/session-1/lab",
		GuestHost: "agent-compose-session-1",
		HostPort:  39000,
		GuestPort: 8888,
		Token:     "secret token",
	}

	host, port := driverpkg.JupyterConnectTarget(toDriverProxyState(proxyState))
	if host != "agent-compose-session-1" || port != 8888 {
		t.Fatalf("jupyterConnectTarget = %s:%d, want agent-compose-session-1:8888", host, port)
	}
	if got := driverpkg.JupyterKernelspecsURL(toDriverProxyState(proxyState)); got != "http://agent-compose-session-1:8888/agent-compose/session/session-1/api/kernelspecs?token=secret+token" {
		t.Fatalf("jupyterKernelspecsURL = %q", got)
	}
}

func TestJupyterConnectTargetFallsBackToHostPortForLoopbackGuestHost(t *testing.T) {
	proxyState := ProxyState{
		ProxyPath: "/agent-compose/session/session-1/lab",
		GuestHost: "127.0.0.1",
		HostPort:  39000,
		GuestPort: 8888,
		Token:     "secret",
	}

	host, port := driverpkg.JupyterConnectTarget(toDriverProxyState(proxyState))
	if host != "127.0.0.1" || port != 39000 {
		t.Fatalf("jupyterConnectTarget = %s:%d, want 127.0.0.1:39000", host, port)
	}
}

func TestWaitForJupyterProxyUsesGuestHostTarget(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent-compose/session/session-1/api/kernelspecs" {
			t.Fatalf("backend path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("token") != "secret token" {
			t.Fatalf("backend token = %q", r.URL.Query().Get("token"))
		}
		_, _ = w.Write([]byte(`{"kernelspecs":{"python3":{},"javascript":{}}}`))
	}))
	t.Cleanup(backend.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := driverpkg.WaitForJupyterProxy(ctx, toDriverProxyState(ProxyState{
		ProxyPath: "/agent-compose/session/session-1/lab",
		GuestHost: "localhost",
		HostPort:  1,
		GuestPort: httptestServerPort(t, backend.URL),
		Token:     "secret token",
	}))
	if err != nil {
		t.Fatalf("waitForJupyterProxy returned error: %v", err)
	}
}

func TestJupyterTargetReachableUsesGuestHostTarget(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(backend.Close)

	reachable := jupyterTargetReachable(ProxyState{
		GuestHost: "localhost",
		HostPort:  1,
		GuestPort: httptestServerPort(t, backend.URL),
	}, time.Second)
	if !reachable {
		t.Fatalf("jupyterTargetReachable returned false for reachable guest target")
	}

	unreachable := jupyterTargetReachable(ProxyState{
		GuestHost: "localhost",
		HostPort:  httptestServerPort(t, backend.URL),
		GuestPort: unusedLocalTCPPort(t),
	}, time.Second)
	if unreachable {
		t.Fatalf("jupyterTargetReachable returned true for unreachable guest target")
	}
}

func unusedLocalTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on ephemeral port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close ephemeral listener: %v", err)
	}
	return port
}
