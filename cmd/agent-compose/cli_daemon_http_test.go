package main

import (
	"agent-compose/pkg/config"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"fmt"
	"io"
	"net"
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
	for _, path := range []string{"/api/version", agentcomposev2connect.RunServiceAttachAgentRunProcedure} {
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
	if got, want := <-seen, agentcomposev2connect.RunServiceAttachAgentRunProcedure+" HTTP/2.0"; got != want {
		t.Fatalf("attach request protocol = %q, want %q", got, want)
	}
}

func TestDaemonHTTPClientAttachAgentRunBidiUsesH2C(t *testing.T) {
	seen := make(chan string, 1)
	mux := http.NewServeMux()
	path, handler := agentcomposev2connect.NewRunServiceHandler(runServiceStub{
		runAttach: func(_ context.Context, stream *connect.BidiStream[agentcomposev2.AttachAgentRunRequest, agentcomposev2.AttachAgentRunResponse]) error {
			req, err := stream.Receive()
			if err != nil {
				return err
			}
			if req.GetStart() == nil {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("start frame is required"))
			}
			return stream.Send(&agentcomposev2.AttachAgentRunResponse{
				Frame: &agentcomposev2.AttachAgentRunResponse_Result{Result: &agentcomposev2.AttachResult{Success: true}},
			})
		},
	})
	mux.Handle(path, handler)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:staticcheck // Tests required h2c transport compatibility.
		if r.URL.Path == agentcomposev2connect.RunServiceAttachAgentRunProcedure {
			seen <- r.Proto
		}
		mux.ServeHTTP(w, r)
	}), &http2.Server{}))
	defer server.Close()

	client := agentcomposev2connect.NewRunServiceClient(newDaemonHTTPClient(cliClientConfig{BaseURL: server.URL}), server.URL)
	stream := client.AttachAgentRun(context.Background())
	if err := stream.Send(&agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{
			Request: &agentcomposev2.RunAgentRequest{ProjectId: "project-1", AgentName: "dialog", Command: "bash"},
			Mode:    agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND,
		}},
	}); err != nil {
		t.Fatalf("AttachAgentRun Send() error = %v", err)
	}
	if err := stream.CloseRequest(); err != nil {
		t.Fatalf("AttachAgentRun CloseRequest() error = %v", err)
	}
	resp, err := stream.Receive()
	if err != nil {
		t.Fatalf("AttachAgentRun Receive() error = %v", err)
	}
	if !resp.GetResult().GetSuccess() {
		t.Fatalf("AttachAgentRun result = %#v, want success", resp)
	}
	if got, want := <-seen, "HTTP/2.0"; got != want {
		t.Fatalf("AttachAgentRun protocol = %q, want %q", got, want)
	}
}

func TestDaemonHTTPClientUsesConfiguredRPCTimeout(t *testing.T) {
	client := newDaemonHTTPClient(cliClientConfig{BaseURL: "http://127.0.0.1:7410"})
	if client.Timeout != 0 {
		t.Fatalf("default daemon client timeout = %v, want none", client.Timeout)
	}

	client = newDaemonHTTPClient(cliClientConfig{BaseURL: "http://127.0.0.1:7410", Timeout: 45 * time.Second})
	if client.Timeout != 45*time.Second {
		t.Fatalf("configured daemon client timeout = %v, want 45s", client.Timeout)
	}
}

func TestDaemonHTTPClientConfiguredTimeoutCoversServerStream(t *testing.T) {
	server := newRunServiceStubServer(t, runServiceStub{
		runAgentStream: func(ctx context.Context, _ *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.StreamAgentRunResponse]) error {
			if err := stream.Send(&agentcomposev2.StreamAgentRunResponse{EventType: agentcomposev2.StreamAgentRunEventType_STREAM_AGENT_RUN_EVENT_TYPE_STARTED}); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return nil
			}
		},
	})
	defer server.Close()

	client := agentcomposev2connect.NewRunServiceClient(newDaemonHTTPClient(cliClientConfig{BaseURL: server.URL, Timeout: 250 * time.Millisecond}), server.URL)
	stream, err := client.StreamAgentRun(context.Background(), connect.NewRequest(&agentcomposev2.RunAgentRequest{}))
	if err != nil {
		t.Fatalf("StreamAgentRun returned error before receiving the first frame: %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("StreamAgentRun did not receive the first frame: %v", stream.Err())
	}
	if stream.Receive() {
		t.Fatal("StreamAgentRun received an unexpected frame after its configured timeout")
	}
	if err := stream.Err(); err == nil || connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("StreamAgentRun error = %v, want deadline_exceeded", err)
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

func TestNewAttachBidiProbeSkipsUnixSocket(t *testing.T) {
	if probe := newAttachBidiProbe(cliClientConfig{BaseURL: "http://agent-compose", SocketPath: "/tmp/agent-compose.sock", UseUnixSocket: true}); probe != nil {
		t.Fatal("newAttachBidiProbe(unix socket) = non-nil probe, want nil")
	}
}

func TestNewAttachBidiProbeSkipsHTTPS(t *testing.T) {
	if probe := newAttachBidiProbe(cliClientConfig{BaseURL: "https://example.com"}); probe != nil {
		t.Fatal("newAttachBidiProbe(https) = non-nil probe, want nil")
	}
}

func TestNewAttachBidiProbeDetectsHTTP1OnlyServerMismatch(t *testing.T) {
	// Simulate an HTTP/1-only reverse proxy with a raw TCP listener: read the
	// h2c client's prior-knowledge preface, then answer with a plain HTTP/1.1
	// status line. The h2c client mis-parses that head as a frame header and
	// surfaces the http2/HTTP/1 transport mismatch. (httptest.NewServer is not
	// used here: its handling of the malformed `PRI *` request can reset the
	// connection before the client reads the response, making the test flaky.)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf) // drain the h2c preface and request frames
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
		time.Sleep(200 * time.Millisecond) // hold open so the client reads the head
	}()

	baseURL := "http://" + ln.Addr().String()
	probe := newAttachBidiProbe(cliClientConfig{BaseURL: baseURL})
	if probe == nil {
		t.Fatalf("newAttachBidiProbe(%q) = nil, want a probe", baseURL)
	}
	err = probe(context.Background())
	if err == nil {
		t.Fatal("probe over HTTP/1-only server returned nil, want transport mismatch")
	}
	if !isAttachHTTP2TransportMismatch(err) {
		t.Fatalf("probe error = %v, want h2c/HTTP/1 transport mismatch", err)
	}
}

func TestNewAttachBidiProbeSucceedsOverH2C(t *testing.T) {
	seen := make(chan string, 1)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:staticcheck // Tests required h2c transport compatibility.
		if r.URL.Path == "/api/version" {
			seen <- r.Proto
		}
		w.WriteHeader(http.StatusOK)
	}), &http2.Server{}))
	defer server.Close()

	probe := newAttachBidiProbe(cliClientConfig{BaseURL: server.URL})
	if probe == nil {
		t.Fatalf("newAttachBidiProbe(%q) = nil, want a probe", server.URL)
	}
	if err := probe(context.Background()); err != nil {
		t.Fatalf("probe over h2c server returned error = %v, want nil", err)
	}
	if got, want := <-seen, "HTTP/2.0"; got != want {
		t.Fatalf("probe request protocol = %q, want %q", got, want)
	}
}

func TestNewAttachBidiProbeReturnsNilOnConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := server.URL
	server.Close() // now dialing fails with connection refused

	probe := newAttachBidiProbe(cliClientConfig{BaseURL: url})
	if probe == nil {
		t.Fatalf("newAttachBidiProbe(%q) = nil, want a probe", url)
	}
	if err := probe(context.Background()); err != nil {
		t.Fatalf("probe over closed server returned error = %v, want nil (transient error left for attach)", err)
	}
}

func TestNewAttachBidiProbeReturnsNilOnTimeout(t *testing.T) {
	blocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	defer func() {
		close(blocked)
		server.Close()
	}()

	probe := newAttachBidiProbe(cliClientConfig{BaseURL: server.URL})
	if probe == nil {
		t.Fatalf("newAttachBidiProbe(%q) = nil, want a probe", server.URL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := probe(ctx); err != nil {
		t.Fatalf("probe timeout returned error = %v, want nil (deadline exceeded is not a mismatch)", err)
	}
}

func TestAttachRunProbePassThroughOpensRealStream(t *testing.T) {
	mux := http.NewServeMux()
	path, handler := agentcomposev2connect.NewRunServiceHandler(runServiceStub{
		runAttach: func(_ context.Context, stream *connect.BidiStream[agentcomposev2.AttachAgentRunRequest, agentcomposev2.AttachAgentRunResponse]) error {
			req, err := stream.Receive()
			if err != nil {
				return err
			}
			if req.GetStart() == nil {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("start frame is required"))
			}
			return stream.Send(&agentcomposev2.AttachAgentRunResponse{
				Frame: &agentcomposev2.AttachAgentRunResponse_Result{Result: &agentcomposev2.AttachResult{Success: true}},
			})
		},
	})
	mux.Handle(path, handler)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:staticcheck // Tests required h2c transport compatibility.
		mux.ServeHTTP(w, r)
	}), &http2.Server{}))
	defer server.Close()

	client := connectAttachAgentRunClient{
		client: agentcomposev2connect.NewRunServiceClient(newDaemonHTTPClient(cliClientConfig{BaseURL: server.URL}), server.URL),
		probe:  func(context.Context) error { return nil },
	}
	stream := client.AttachAgentRun(context.Background())
	if err := stream.Send(&agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{
			Request: &agentcomposev2.RunAgentRequest{ProjectId: "project-1", AgentName: "dialog", Command: "bash"},
			Mode:    agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND,
		}},
	}); err != nil {
		t.Fatalf("AttachAgentRun Send() error = %v", err)
	}
	if err := stream.CloseRequest(); err != nil {
		t.Fatalf("AttachAgentRun CloseRequest() error = %v", err)
	}
	resp, err := stream.Receive()
	if err != nil {
		t.Fatalf("AttachAgentRun Receive() error = %v", err)
	}
	if !resp.GetResult().GetSuccess() {
		t.Fatalf("AttachAgentRun result = %#v, want success", resp)
	}
}
