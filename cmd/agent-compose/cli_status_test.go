package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusCommandUsesHostFlagBeforeEnvironment(t *testing.T) {
	testStatusCommandUsesHostFlagBeforeEnvironment(t)
}

func testStatusCommandUsesHostFlagBeforeEnvironment(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Fatalf("request path = %q, want /api/version", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"err":null,"msg":"OK","data":{"timestamp":1783501631.2438176,"timezone":"CST","timezone_offset":28800,"version":"flag"}}`)
	}))
	defer server.Close()
	t.Setenv("AGENT_COMPOSE_HOST", "http://127.0.0.1:1")

	stdout, stderr, runCount, err := executeCommand("status", "--host", server.URL)
	if err != nil {
		t.Fatalf("status command returned error: %v", err)
	}
	for _, want := range []string{"STATUS", "UPTIME", "VERSION", "OK", "2026-07-08 17:07:11 CST +0800", "flag"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("status stdout = %q, want %q", stdout, want)
		}
	}
	if strings.Contains(stdout, `"version"`) {
		t.Fatalf("status stdout = %q, want text output", stdout)
	}
	if stderr != "" {
		t.Fatalf("status stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestStatusCommandUsesEnvironmentHost(t *testing.T) {
	testStatusCommandUsesEnvironmentHost(t)
}

func testStatusCommandUsesEnvironmentHost(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Fatalf("request path = %q, want /api/version", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"err":null,"msg":"OK","data":{"timestamp":1783501631.2438176,"timezone":"CST","timezone_offset":28800,"version":"env"}}`)
	}))
	defer server.Close()
	t.Setenv("AGENT_COMPOSE_HOST", server.URL)

	stdout, _, runCount, err := executeCommand("status")
	if err != nil {
		t.Fatalf("status command returned error: %v", err)
	}
	for _, want := range []string{"STATUS", "UPTIME", "VERSION", "OK", "2026-07-08 17:07:11 CST +0800", "env"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("status stdout = %q, want %q", stdout, want)
		}
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestStatusCommandUnavailableWritesStderrAndExitCode(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "missing.sock")
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	t.Setenv("AGENT_COMPOSE_HOST", "")

	stdout, stderr, runCount, exitCode := executeCLICommand("status")
	if exitCode != exitCodeUnavailable {
		t.Fatalf("status unavailable exit code = %d, want %d", exitCode, exitCodeUnavailable)
	}
	if stdout != "" {
		t.Fatalf("status unavailable stdout = %q, want empty", stdout)
	}
	for _, want := range []string{"connect daemon via AGENT_COMPOSE_SOCKET", socketPath} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("status unavailable stderr %q does not contain %q", stderr, want)
		}
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestInvalidHostWritesStderrAndUsageExitCode(t *testing.T) {
	stdout, stderr, runCount, exitCode := executeCLICommand("status", "--host", "127.0.0.1:7410")
	if exitCode != exitCodeUsage {
		t.Fatalf("invalid host exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" {
		t.Fatalf("invalid host stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--host") || !strings.Contains(stderr, "127.0.0.1:7410") {
		t.Fatalf("invalid host stderr = %q", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}
