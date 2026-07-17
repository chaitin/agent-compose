package main

import (
	"agent-compose/pkg/config"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck // Tests the daemon's required h2c transport compatibility.
)

func TestStatusCommandJSONPrintsRawDaemonResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Fatalf("request path = %q, want /api/version", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"err":null,"msg":"OK","data":{"timestamp":1783501631.2438176,"version":"json"}}`)
	}))
	defer server.Close()

	stdout, stderr, runCount, err := executeCommand("status", "--json", "--host", server.URL)
	if err != nil {
		t.Fatalf("status --json command returned error: %v", err)
	}
	if strings.TrimSpace(stdout) != `{"err":null,"msg":"OK","data":{"timestamp":1783501631.2438176,"version":"json"}}` {
		t.Fatalf("status --json stdout = %q, want raw daemon response", stdout)
	}
	if stderr != "" {
		t.Fatalf("status --json stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestStatusCommandUsesDefaultUnixSocket(t *testing.T) {
	testStatusCommandUsesDefaultUnixSocket(t)
}

func testStatusCommandUsesDefaultUnixSocket(t *testing.T) {
	t.Helper()
	runtimeDir := shortUnixSocketDir(t)
	socketPath := filepath.Join(runtimeDir, "agent-compose.sock")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("AGENT_COMPOSE_SOCKET", "")
	t.Setenv("AGENT_COMPOSE_HOST", "")

	app, cancel := newTestDaemonApp(t, "", nil)
	defer cancel()
	runCtx, stop := context.WithCancel(context.Background())
	errCh := runDaemonAppAsync(app, runCtx)
	waitForHTTPStatus(t, newUnixHTTPClient(socketPath), "http://agent-compose/api/version", http.StatusOK)

	stdout, stderr, runCount, err := executeCommand("status")
	stop()
	waitForDaemonExit(t, errCh)
	if err != nil {
		t.Fatalf("status command returned error: %v", err)
	}
	for _, want := range []string{"STATUS", "UPTIME", "VERSION", "OK", config.BuildVersion} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("status stdout = %q, want %q", stdout, want)
		}
	}
	if stderr != "" {
		t.Fatalf("status stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestStatusCommandReportsUnreadableDaemon(t *testing.T) {
	testStatusCommandReportsUnreadableDaemon(t)
}

func testStatusCommandReportsUnreadableDaemon(t *testing.T) {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "missing.sock")
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	t.Setenv("AGENT_COMPOSE_HOST", "")

	_, _, runCount, err := executeCommand("status")
	if err == nil {
		t.Fatal("status command returned nil error, want daemon connection error")
	}
	for _, part := range []string{"connect daemon via AGENT_COMPOSE_SOCKET", socketPath} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("error %q does not contain %q", err.Error(), part)
		}
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestDaemonHTTPClientUsesH2COnlyForAttachRPCs(t *testing.T) {
	seen := make(chan string, 2)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:staticcheck // Tests required h2c transport compatibility.
		seen <- r.URL.Path + " " + r.Proto
		w.WriteHeader(http.StatusOK)
	}), &http2.Server{}))
	defer server.Close()

	client := newDaemonHTTPClient(cliClientConfig{BaseURL: server.URL})
	for _, path := range []string{"/api/version", agentcomposev2connect.RunServiceRunAttachProcedure} {
		req, err := http.NewRequest(http.MethodPost, server.URL+path, nil)
		if err != nil {
			t.Fatalf("NewRequest(%q) error = %v", path, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do(%q) error = %v", path, err)
		}
		_ = resp.Body.Close()
	}

	if got, want := <-seen, "/api/version HTTP/1.1"; got != want {
		t.Fatalf("ordinary request protocol = %q, want %q", got, want)
	}
	if got, want := <-seen, agentcomposev2connect.RunServiceRunAttachProcedure+" HTTP/2.0"; got != want {
		t.Fatalf("attach request protocol = %q, want %q", got, want)
	}
}

func TestDaemonHTTPClientRunAttachBidiUsesH2C(t *testing.T) {
	seen := make(chan string, 1)
	mux := http.NewServeMux()
	path, handler := agentcomposev2connect.NewRunServiceHandler(runServiceStub{
		runAttach: func(_ context.Context, stream *connect.BidiStream[agentcomposev2.RunAttachRequest, agentcomposev2.RunAttachResponse]) error {
			req, err := stream.Receive()
			if err != nil {
				return err
			}
			if req.GetStart() == nil {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("start frame is required"))
			}
			return stream.Send(&agentcomposev2.RunAttachResponse{
				Frame: &agentcomposev2.RunAttachResponse_Result{Result: &agentcomposev2.AttachResult{Success: true}},
			})
		},
	})
	mux.Handle(path, handler)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:staticcheck // Tests required h2c transport compatibility.
		if r.URL.Path == agentcomposev2connect.RunServiceRunAttachProcedure {
			seen <- r.Proto
		}
		mux.ServeHTTP(w, r)
	}), &http2.Server{}))
	defer server.Close()

	client := agentcomposev2connect.NewRunServiceClient(newDaemonHTTPClient(cliClientConfig{BaseURL: server.URL}), server.URL)
	stream := client.RunAttach(context.Background())
	if err := stream.Send(&agentcomposev2.RunAttachRequest{
		Frame: &agentcomposev2.RunAttachRequest_Start{Start: &agentcomposev2.RunAttachStart{
			Request: &agentcomposev2.RunAgentRequest{ProjectId: "project-1", AgentName: "dialog", Command: "bash"},
			Mode:    agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND,
		}},
	}); err != nil {
		t.Fatalf("RunAttach Send() error = %v", err)
	}
	if err := stream.CloseRequest(); err != nil {
		t.Fatalf("RunAttach CloseRequest() error = %v", err)
	}
	resp, err := stream.Receive()
	if err != nil {
		t.Fatalf("RunAttach Receive() error = %v", err)
	}
	if !resp.GetResult().GetSuccess() {
		t.Fatalf("RunAttach result = %#v, want success", resp)
	}
	if got, want := <-seen, "HTTP/2.0"; got != want {
		t.Fatalf("RunAttach protocol = %q, want %q", got, want)
	}
}

func TestDaemonAttachHTTPClientHasNoRequestTimeout(t *testing.T) {
	regular := newDaemonHTTPClient(cliClientConfig{BaseURL: "http://127.0.0.1:7410"})
	if regular.Timeout != 10*time.Minute {
		t.Fatalf("regular daemon client timeout = %v, want 10m", regular.Timeout)
	}
	attach := newDaemonAttachHTTPClient(cliClientConfig{BaseURL: "http://127.0.0.1:7410"})
	if attach.Timeout != 0 {
		t.Fatalf("attach daemon client timeout = %v, want none", attach.Timeout)
	}
}

func TestBrowserBaseURLForCLIUnixSocketDoesNotGuessHTTPAddress(t *testing.T) {
	t.Setenv("AGENT_COMPOSE_HOST", "")
	t.Setenv("AGENT_COMPOSE_SOCKET", filepath.Join(t.TempDir(), "agent-compose.sock"))
	baseURL, err := browserBaseURLForCLI(cliOptions{})
	if err != nil || baseURL != "" {
		t.Fatalf("browser base URL = %q, err = %v; want empty Unix socket fallback", baseURL, err)
	}
	if got := joinBaseURLAndPath(baseURL, "/agent-compose/session/sandbox/lab?token=socket-token"); got != "/agent-compose/session/sandbox/lab?token=socket-token" {
		t.Fatalf("Unix socket notebook URL = %q, want relative URL with token", got)
	}
}
