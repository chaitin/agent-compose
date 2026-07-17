package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
)

func waitForHTTPStatus(t *testing.T, client *http.Client, url string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			if closeErr := resp.Body.Close(); closeErr != nil {
				t.Fatalf("close response body: %v", closeErr)
			}
			if resp.StatusCode == want {
				return
			}
			lastErr = fmt.Errorf("status = %d, want %d", resp.StatusCode, want)
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("GET %s did not return status %d: %v", url, want, lastErr)
}

func freeTCPListenAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free tcp port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close free tcp port: %v", err)
	}
	return addr
}

func (s resourceServiceStub) ResolveID(ctx context.Context, req *connect.Request[agentcomposev2.ResolveResourceIDRequest]) (*connect.Response[agentcomposev2.ResolveResourceIDResponse], error) {
	if s.resolveID == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ResolveID stub is not configured"))
	}
	return s.resolveID(ctx, req)
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func stringSliceContainsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

func assertLocalPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertLocalPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be missing, stat err=%v", path, err)
	}
}

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}

func hasRoute(app *DaemonApp, method, path string) bool {
	for _, route := range app.Echo.Routes() {
		if route.Method != method {
			continue
		}
		if route.Path == path {
			return true
		}
	}
	return false
}
