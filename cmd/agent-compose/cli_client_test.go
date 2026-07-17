package main

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIClientConfigPriority(t *testing.T) {
	testCLIClientConfigPriority(t)
}

func testCLIClientConfigPriority(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	socketPath := filepath.Join(root, "agent-compose.sock")
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	t.Setenv("AGENT_COMPOSE_HOST", "https://env.example")

	clientConfig, err := resolveCLIClientConfig("https://flag.example/")
	if err != nil {
		t.Fatalf("resolveCLIClientConfig returned error: %v", err)
	}
	if clientConfig.Source != "--host" || clientConfig.BaseURL != "https://flag.example" || clientConfig.UseUnixSocket {
		t.Fatalf("flag client config = %#v", clientConfig)
	}

	clientConfig, err = resolveCLIClientConfig("")
	if err != nil {
		t.Fatalf("resolveCLIClientConfig returned error: %v", err)
	}
	if clientConfig.Source != "AGENT_COMPOSE_HOST" || clientConfig.BaseURL != "https://env.example" || clientConfig.UseUnixSocket {
		t.Fatalf("env client config = %#v", clientConfig)
	}

	t.Setenv("AGENT_COMPOSE_HOST", "")
	clientConfig, err = resolveCLIClientConfig("")
	if err != nil {
		t.Fatalf("resolveCLIClientConfig returned error: %v", err)
	}
	if clientConfig.Source != "AGENT_COMPOSE_SOCKET" || clientConfig.SocketPath != socketPath || !clientConfig.UseUnixSocket {
		t.Fatalf("socket client config = %#v", clientConfig)
	}
}

func TestCLIClientConfigRejectsInvalidHost(t *testing.T) {
	testCLIClientConfigRejectsInvalidHost(t)
}

func testCLIClientConfigRejectsInvalidHost(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name     string
		hostFlag string
		envHost  string
		want     string
	}{
		{name: "flag missing scheme", hostFlag: "127.0.0.1:7410", want: "--host"},
		{name: "env missing host", envHost: "https://", want: "AGENT_COMPOSE_HOST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENT_COMPOSE_HOST", tc.envHost)
			_, err := resolveCLIClientConfig(tc.hostFlag)
			if err == nil {
				t.Fatal("resolveCLIClientConfig returned nil error, want invalid host error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func newUnixHTTPClient(socketPath string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return &http.Client{Transport: transport}
}
