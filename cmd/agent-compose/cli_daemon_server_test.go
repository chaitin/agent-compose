package main

import (
	"agent-compose/pkg/config"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"
)

func TestDaemonTCPServerRunAttachBidiUsesH2C(t *testing.T) {
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
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Proto
		handler.ServeHTTP(w, r)
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	servers := &daemonServers{}
	servers.add("HTTP_LISTEN", listener.Addr().String(), listener, mux, nil)
	errCh := servers.serve(slog.Default())
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := servers.shutdown(shutdownCtx); err != nil {
			t.Fatalf("shutdown daemon server: %v", err)
		}
		for range servers.items {
			if err := <-errCh; err != nil {
				t.Fatalf("daemon server returned error: %v", err)
			}
		}
	})

	baseURL := "http://" + listener.Addr().String()
	client := agentcomposev2connect.NewRunServiceClient(newDaemonHTTPClient(cliClientConfig{BaseURL: baseURL}), baseURL)
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

func TestNewDaemonAppBuildsHandlerWithoutListening(t *testing.T) {
	testNewDaemonAppBuildsHandlerWithoutListening(t)
}

func testNewDaemonAppBuildsHandlerWithoutListening(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen test port: %v", err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Fatalf("close test listener: %v", err)
		}
	}()

	app, cancel := newTestDaemonApp(t, ln.Addr().String(), func(di do.Injector) error {
		t.Fatalf("background managers started during construction")
		return nil
	})
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	rec := httptest.NewRecorder()
	app.Echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/version status = %d, want %d", rec.Code, http.StatusOK)
	}
	var decoded struct {
		Data struct {
			Timezone       string `json:"timezone"`
			TimezoneOffset *int   `json:"timezone_offset"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("/api/version JSON decode failed: %v", err)
	}
	if decoded.Data.Timezone == "" || decoded.Data.TimezoneOffset == nil {
		t.Fatalf("/api/version timezone fields = %q/%v, want server timezone", decoded.Data.Timezone, decoded.Data.TimezoneOffset)
	}
}

func TestNewDaemonAppDefaultsToSocketOnlyConfig(t *testing.T) {
	testNewDaemonAppDefaultsToSocketOnlyConfig(t)
}

func testNewDaemonAppDefaultsToSocketOnlyConfig(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("HTTP_LISTEN", "")
	t.Setenv("AGENT_COMPOSE_SOCKET", "")
	t.Setenv("AGENT_COMPOSE_HOST", "")
	t.Setenv("RUNTIME_DRIVER", config.RuntimeDriverDocker)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	app, err := NewDaemonApp(ctx, DaemonOptions{
		StartBackground: func(do.Injector) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewDaemonApp returned error: %v", err)
	}
	if app.Config.HttpListen != "" {
		t.Fatalf("HttpListen = %q, want empty by default", app.Config.HttpListen)
	}
	wantSocket := filepath.Join(runtimeDir, "agent-compose.sock")
	if app.Config.AgentComposeSocket != wantSocket {
		t.Fatalf("AgentComposeSocket = %q, want %q", app.Config.AgentComposeSocket, wantSocket)
	}
}

func TestDaemonAppServesUnixSocketAndOptionalTCP(t *testing.T) {
	testDaemonAppServesUnixSocketAndOptionalTCP(t)
}

func testDaemonAppServesUnixSocketAndOptionalTCP(t *testing.T) {
	t.Helper()
	socketPath := shortUnixSocketPath(t)
	tcpListen := freeTCPListenAddress(t)
	app, cancel := newTestDaemonAppWithSocketAndTCP(t, socketPath, tcpListen, nil)
	defer cancel()

	runCtx, stop := context.WithCancel(context.Background())
	errCh := runDaemonAppAsync(app, runCtx)

	unixClient := newUnixHTTPClient(socketPath)
	waitForHTTPStatus(t, unixClient, "http://agent-compose/api/version", http.StatusOK)
	waitForHTTPStatus(t, http.DefaultClient, "http://"+tcpListen+"/api/version", http.StatusOK)

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat socket path: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o, want 0600", got)
	}

	stop()
	waitForDaemonExit(t, errCh)
	if _, err := os.Stat(socketPath); !errorsIsNotExist(err) {
		t.Fatalf("socket path still exists after shutdown, stat err=%v", err)
	}
	ln, err := net.Listen("tcp", tcpListen)
	if err != nil {
		t.Fatalf("tcp listener was not released after shutdown: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close tcp listener after shutdown: %v", err)
	}
}

func TestDaemonAppCleansStaleUnixSocket(t *testing.T) {
	testDaemonAppCleansStaleUnixSocket(t)
}

func testDaemonAppCleansStaleUnixSocket(t *testing.T) {
	t.Helper()
	socketPath := shortUnixSocketPath(t)
	createStaleUnixSocketFile(t, socketPath)
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("expected stale socket file to remain: %v", err)
	}

	app, cancel := newTestDaemonAppWithSocketAndTCP(t, socketPath, "", nil)
	defer cancel()
	runCtx, stop := context.WithCancel(context.Background())
	errCh := runDaemonAppAsync(app, runCtx)

	waitForHTTPStatus(t, newUnixHTTPClient(socketPath), "http://agent-compose/api/version", http.StatusOK)
	stop()
	waitForDaemonExit(t, errCh)
}

func TestDaemonAppReportsUncreatableUnixSocketPath(t *testing.T) {
	testDaemonAppReportsUncreatableUnixSocketPath(t)
}

func testDaemonAppReportsUncreatableUnixSocketPath(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	parentFile := filepath.Join(root, "socket-parent-file")
	if err := os.WriteFile(parentFile, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("write socket parent file: %v", err)
	}
	socketPath := filepath.Join(parentFile, "agent-compose.sock")

	app, cancel := newTestDaemonAppWithSocketAndTCP(t, socketPath, "", nil)
	defer cancel()
	err := app.Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil error, want unix socket path error")
	}
	for _, part := range []string{"AGENT_COMPOSE_SOCKET", socketPath} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("error %q does not contain %q", err.Error(), part)
		}
	}
}

func newTestDaemonAppWithSocketAndTCP(t *testing.T, socketPath string, httpListen string, startBackground func(do.Injector) error) (*DaemonApp, context.CancelFunc) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("HTTP_LISTEN", httpListen)
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	t.Setenv("RUNTIME_DRIVER", config.RuntimeDriverDocker)
	t.Setenv("SANDBOX_START_TIMEOUT", "1s")
	t.Setenv("SANDBOX_STOP_TIMEOUT", "1s")
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("BOXLITE_HOME", filepath.Join(root, "boxlite"))
	t.Setenv("BOXLITE_RUNTIME_DIR", filepath.Join(root, "boxlite-runtime"))
	t.Setenv("DOCKER_HOME", filepath.Join(root, "docker"))
	t.Setenv("MICROSANDBOX_HOME", filepath.Join(root, "microsandbox"))
	t.Setenv("MICROSANDBOX_MSB_PATH", filepath.Join(root, "msb"))
	t.Setenv("MICROSANDBOX_LIB_PATH", filepath.Join(root, "libmicrosandbox_go_ffi.so"))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts := DaemonOptions{}
	if startBackground != nil {
		opts.StartBackground = startBackground
	} else {
		opts.StartBackground = func(do.Injector) error { return nil }
	}
	app, err := NewDaemonApp(ctx, opts)
	if err != nil {
		cancel()
		t.Fatalf("NewDaemonApp returned error: %v", err)
	}
	return app, cancel
}

func createStaleUnixSocketFile(t *testing.T, socketPath string) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("create unix socket fd: %v", err)
	}
	defer func() {
		if err := syscall.Close(fd); err != nil {
			t.Fatalf("close unix socket fd: %v", err)
		}
	}()
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: socketPath}); err != nil {
		t.Fatalf("bind stale unix socket file: %v", err)
	}
}

func shortUnixSocketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortUnixSocketDir(t), "ac.sock")
}

func shortUnixSocketDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "ac-sock-")
	if err != nil {
		t.Fatalf("create short unix socket temp dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Fatalf("remove short unix socket temp dir: %v", err)
		}
	})
	return root
}
