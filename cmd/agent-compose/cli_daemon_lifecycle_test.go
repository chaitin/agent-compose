package main

import (
	"agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
	storagesqlite "agent-compose/pkg/storage/sqlite"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"agent-compose/proto/health/v1/healthv1connect"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/joho/godotenv"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/types/known/emptypb"
)

func clearDaemonTestEnv() {
	envFile, err := findDaemonTestEnvFile()
	if err != nil {
		return
	}
	values, err := godotenv.Read(envFile)
	if err != nil {
		return
	}
	for key := range values {
		_ = os.Unsetenv(key)
	}
}

func findDaemonTestEnvFile() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		path := filepath.Join(dir, ".env")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

func TestVersionCommandPrintsBuildVersionWithoutStartingDaemon(t *testing.T) {
	oldVersion := config.BuildVersion
	config.BuildVersion = "test-version"
	t.Cleanup(func() { config.BuildVersion = oldVersion })

	stdout, stderr, runCount, err := executeCommand("version")
	if err != nil {
		t.Fatalf("version command returned error: %v", err)
	}
	if stdout != "test-version\n" {
		t.Fatalf("version stdout = %q, want %q", stdout, "test-version\n")
	}
	if stderr != "" {
		t.Fatalf("version stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestDaemonHelpDoesNotStartDaemon(t *testing.T) {
	stdout, stderr, runCount, err := executeCommand("daemon", "--help")
	if err != nil {
		t.Fatalf("daemon --help returned error: %v", err)
	}
	if !strings.Contains(stdout, "Start the agent-compose daemon") || !strings.Contains(stdout, "Usage:") {
		t.Fatalf("daemon --help stdout missing expected help text: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("daemon --help stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestUnknownCommandFailsWithoutStartingDaemon(t *testing.T) {
	_, _, runCount, err := executeCommand("does-not-exist")
	if err == nil {
		t.Fatal("unknown command returned nil error")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unknown command error = %q, want unknown command", err.Error())
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestRootCommandPrintsHelpWithoutStartingDaemon(t *testing.T) {
	stdout, stderr, runCount, err := executeCommand()
	if err != nil {
		t.Fatalf("root command returned error: %v", err)
	}
	if !strings.Contains(stdout, "agent-compose daemon and CLI") || !strings.Contains(stdout, "Usage:") {
		t.Fatalf("root command stdout missing expected help text: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("root command stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestDaemonCommandStartsDaemon(t *testing.T) {
	_, _, runCount, err := executeCommand("daemon")
	if err != nil {
		t.Fatalf("daemon command returned error: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("daemon command called daemon runner %d times, want 1", runCount)
	}
}

func TestConfigCommandPrintsNormalizedYAMLWithoutStartingDaemon(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "review-project")
	writeComposeFile(t, dir, `
workspaces:
  default:
    provider: file
    path: .
variables:
  API_KEY:
    value: ${API_KEY}
    secret: true
  PUBLIC: visible
agents:
  reviewer:
    provider: codex
    env:
      MODE: strict
`)
	withWorkingDir(t, dir)
	t.Setenv("API_KEY", "sk-secret")
	t.Setenv("AGENT_COMPOSE_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv("AGENT_COMPOSE_HOST", "")

	stdout, stderr, runCount, err := executeCommand("config")
	if err != nil {
		t.Fatalf("config command returned error: %v", err)
	}
	if stderr != "" {
		t.Fatalf("config stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
	for _, want := range []string{"name: review-project", "variables:", "agents:", "secret: true", "********", "visible", "strict"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("config YAML missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "network:") {
		t.Fatalf("config YAML contains removed network field:\n%s", stdout)
	}
	if strings.Contains(stdout, "sk-secret") {
		t.Fatalf("config YAML leaked secret value:\n%s", stdout)
	}
}

func TestConfigCommandPrintsNormalizedJSONWithoutStartingDaemon(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "json-project")
	writeComposeFile(t, dir, `
name: json-project
variables:
  TOKEN:
    value: ${TOKEN}
    secret: true
workspaces:
  default:
    provider: file
    path: .
agents:
  reviewer:
    provider: codex
`)
	withWorkingDir(t, dir)
	t.Setenv("TOKEN", "secret-token")
	t.Setenv("AGENT_COMPOSE_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv("AGENT_COMPOSE_HOST", "")

	stdout, stderr, runCount, err := executeCommand("config", "--json")
	if err != nil {
		t.Fatalf("config --json returned error: %v", err)
	}
	if stderr != "" {
		t.Fatalf("config --json stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
	if strings.Contains(stdout, "secret-token") {
		t.Fatalf("config JSON leaked secret value: %s", stdout)
	}
	if strings.Contains(stdout, `"network":`) {
		t.Fatalf("config JSON contains removed network field: %s", stdout)
	}
	var decoded struct {
		Name      string `json:"name"`
		Variables []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Secret bool   `json:"secret"`
		} `json:"variables"`
		Agents []struct {
			Name   string `json:"name"`
			Driver struct {
				Name string `json:"name"`
			} `json:"driver"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("config --json output is not JSON: %v\n%s", err, stdout)
	}
	if decoded.Name != "json-project" {
		t.Fatalf("decoded config = %#v", decoded)
	}
	if len(decoded.Variables) != 1 || decoded.Variables[0].Name != "TOKEN" || decoded.Variables[0].Value != "********" || !decoded.Variables[0].Secret {
		t.Fatalf("decoded variables = %#v", decoded.Variables)
	}
	if len(decoded.Agents) != 1 || decoded.Agents[0].Name != "reviewer" || decoded.Agents[0].Driver.Name != "docker" {
		t.Fatalf("decoded agents = %#v", decoded.Agents)
	}
}

func TestRealCLILogsUsesDaemonRunDataWithTimestampsAndChronologicalOrder(t *testing.T) {
	if os.Getenv("AGENT_COMPOSE_REAL_CLI_TEST") != "1" {
		t.Skip("set AGENT_COMPOSE_REAL_CLI_TEST=1 to run the real CLI + daemon logs test")
	}
	root := t.TempDir()
	binPath := filepath.Join(root, "agent-compose")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelBuild()
	build := osexec.CommandContext(buildCtx, "go", "build", "-buildvcs=false", "-o", binPath, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOCACHE="+filepath.Join(root, "gocache"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent-compose CLI failed: %v\n%s", err, output)
	}

	composePath := writeComposeFile(t, filepath.Join(root, "project"), `
name: real-cli-logs
agents:
  writer:
    provider: codex
  reviewer:
    provider: codex
`)
	_, normalized, projectID, err := resolveComposeProject(cliOptions{ComposeFile: composePath})
	if err != nil {
		t.Fatalf("resolve compose project: %v", err)
	}

	dataRoot := filepath.Join(root, "data")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatalf("create test data root: %v", err)
	}
	storeConfig := &config.Config{DataRoot: dataRoot, DbAddr: filepath.Join(dataRoot, "data.db")}
	database, err := storagesqlite.Open(storeConfig.DbAddr, storeConfig.DbTimeout)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store := configstore.FromDB(database.DB())
	if _, err := store.UpsertProject(context.Background(), domain.ProjectRecord{ID: projectID, Name: normalized.Name, SourcePath: composePath, CurrentRevision: 1}); err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	earlyStarted := time.Date(2026, 7, 8, 1, 2, 3, 0, time.UTC)
	lateStarted := earlyStarted.Add(2 * time.Second)
	if _, err := store.CreateProjectRun(context.Background(), domain.ProjectRunRecord{
		RunID:           "run-real-late",
		ProjectID:       projectID,
		ProjectName:     normalized.Name,
		ProjectRevision: 1,
		AgentName:       "reviewer",
		Source:          domain.ProjectRunSourceAPI,
		Status:          domain.ProjectRunStatusSucceeded,
		Output:          "late log\n",
		ResultJSON:      "{}",
		StartedAt:       lateStarted,
		CompletedAt:     lateStarted.Add(time.Second),
	}); err != nil {
		t.Fatalf("create late run: %v", err)
	}
	if _, err := store.CreateProjectRun(context.Background(), domain.ProjectRunRecord{
		RunID:           "run-real-early",
		ProjectID:       projectID,
		ProjectName:     normalized.Name,
		ProjectRevision: 1,
		AgentName:       "writer",
		Source:          domain.ProjectRunSourceAPI,
		Status:          domain.ProjectRunStatusSucceeded,
		Output:          "early log\n",
		ResultJSON:      "{}",
		StartedAt:       earlyStarted,
		CompletedAt:     earlyStarted.Add(time.Second),
	}); err != nil {
		t.Fatalf("create early run: %v", err)
	}
	if err := store.DB().Close(); err != nil {
		t.Fatalf("close config store: %v", err)
	}

	socketPath := shortUnixSocketPath(t)
	t.Setenv("DATA_ROOT", dataRoot)
	t.Setenv("HTTP_LISTEN", "")
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	t.Setenv("AGENT_COMPOSE_HOST", "")
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

	daemonCtx, stopDaemon := context.WithCancel(context.Background())
	app, err := NewDaemonApp(daemonCtx, DaemonOptions{StartBackground: func(do.Injector) error { return nil }})
	if err != nil {
		stopDaemon()
		t.Fatalf("NewDaemonApp returned error: %v", err)
	}
	errCh := runDaemonAppAsync(app, daemonCtx)
	t.Cleanup(func() {
		stopDaemon()
		waitForDaemonExit(t, errCh)
	})
	waitForHTTPStatus(t, newUnixHTTPClient(socketPath), "http://agent-compose/api/version", http.StatusOK)

	runCtx, cancelRun := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRun()
	logsCmd := osexec.CommandContext(runCtx, binPath, "--file", composePath, "logs")
	logsCmd.Env = append(os.Environ(),
		"AGENT_COMPOSE_SOCKET="+socketPath,
		"AGENT_COMPOSE_HOST=",
		"DATA_ROOT="+dataRoot,
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	logsCmd.Stdout = &stdout
	logsCmd.Stderr = &stderr
	if err := logsCmd.Run(); err != nil {
		t.Fatalf("real CLI logs failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	want := "writer-run-real-early [2026-07-08T01:02:04.000Z]| early log\n" +
		"reviewer-run-real-late [2026-07-08T01:02:06.000Z]| late log\n"
	if stdout.String() != want || stderr.String() != "" {
		t.Fatalf("real CLI logs stdout/stderr = %q / %q, want %q / empty", stdout.String(), stderr.String(), want)
	}
}

func TestDaemonAppRegistersCoreRoutes(t *testing.T) {
	testDaemonAppRegistersCoreRoutes(t)
}

func testDaemonAppRegistersCoreRoutes(t *testing.T) {
	t.Helper()
	app, cancel := newTestDaemonApp(t, "127.0.0.1:0", nil)
	defer cancel()

	server := httptest.NewServer(app.Echo)
	defer server.Close()

	healthClient := healthv1connect.NewHealthServiceClient(server.Client(), server.URL)
	healthResp, err := healthClient.Status(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatalf("health status returned error: %v", err)
	}
	if healthResp.Msg.GetVersion() != config.BuildVersion {
		t.Fatalf("health version = %q, want %q", healthResp.Msg.GetVersion(), config.BuildVersion)
	}

	legacyReq, err := http.NewRequest(http.MethodPost, server.URL+"/agentcompose.v1.SessionService/GetSession", strings.NewReader(`{"sessionId":"missing"}`))
	if err != nil {
		t.Fatal(err)
	}
	legacyReq.Header.Set("Content-Type", "application/json")
	legacyResp, err := server.Client().Do(legacyReq)
	if err != nil {
		t.Fatalf("removed v1 GetSession request: %v", err)
	}
	defer func() { _ = legacyResp.Body.Close() }()
	if legacyResp.StatusCode != http.StatusNotFound {
		t.Fatalf("removed v1 GetSession HTTP status = %d, want %d", legacyResp.StatusCode, http.StatusNotFound)
	}

	projectClient := agentcomposev2connect.NewProjectServiceClient(server.Client(), server.URL)
	validateResp, err := projectClient.ValidateProject(context.Background(), connect.NewRequest(&agentcomposev2.ValidateProjectRequest{}))
	if err != nil {
		t.Fatalf("ValidateProject returned error: %v", err)
	}
	if validateResp.Msg.GetValid() || len(validateResp.Msg.GetIssues()) == 0 {
		t.Fatalf("ValidateProject empty response = %#v, want validation issue", validateResp.Msg)
	}
	runClient := agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL)
	_, err = runClient.GetRun(context.Background(), connect.NewRequest(&agentcomposev2.GetRunRequest{}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || err == nil || !strings.Contains(err.Error(), "run id is required") {
		t.Fatalf("GetRun empty error = %v, want invalid argument with run id message", err)
	}
	execClient := agentcomposev2connect.NewExecServiceClient(server.Client(), server.URL)
	_, err = execClient.Exec(context.Background(), connect.NewRequest(&agentcomposev2.ExecRequest{}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || err == nil || !strings.Contains(err.Error(), "exec target is required") {
		t.Fatalf("Exec empty error = %v, want invalid argument with target message", err)
	}
	imageClient := agentcomposev2connect.NewImageServiceClient(server.Client(), server.URL)
	_, err = imageClient.InspectImage(context.Background(), connect.NewRequest(&agentcomposev2.InspectImageRequest{}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || err == nil || !strings.Contains(err.Error(), "image_ref is required") {
		t.Fatalf("InspectImage empty error = %v, want invalid argument with image_ref message", err)
	}

	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/agentcompose.v2.ProjectService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.RunService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.ExecService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.ImageService/*"},
		{method: http.MethodGet, path: "/api/webhook-sources"},
		{method: http.MethodGet, path: "/api/agent-compose/workspaces/:workspaceID/files"},
		{method: http.MethodGet, path: "/jupyter/:sessionID"},
	} {
		if !hasRoute(app, route.method, route.path) {
			t.Fatalf("%s %s route was not registered", route.method, route.path)
		}
	}
}

func TestDaemonAppDoesNotRegisterStaticWebRoutes(t *testing.T) {
	testDaemonAppDoesNotRegisterStaticWebRoutes(t)
}

func testDaemonAppDoesNotRegisterStaticWebRoutes(t *testing.T) {
	t.Helper()
	app, cancel := newTestDaemonApp(t, "127.0.0.1:0", nil)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	rec := httptest.NewRecorder()
	app.Echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/version status = %d, want %d", rec.Code, http.StatusOK)
	}

	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/ui"},
		{method: http.MethodGet, path: "/ui/*"},
		{method: http.MethodGet, path: "/*"},
	} {
		if hasRoute(app, route.method, route.path) {
			t.Fatalf("%s %s static route should not be registered", route.method, route.path)
		}
	}

	for _, path := range []string{"/ui", "/ui/runs", "/agent-compose.html"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		app.Echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}

func TestDaemonAppStartsBackgroundOnce(t *testing.T) {
	testDaemonAppStartsBackgroundOnce(t)
}

func TestDaemonAppWaitsForBackgroundShutdown(t *testing.T) {
	app, cancelApp := newTestDaemonAppWithSocketAndTCP(t, shortUnixSocketPath(t), "127.0.0.1:0", func(do.Injector) error { return nil })
	defer cancelApp()
	stopEntered := make(chan struct{})
	stopRelease := make(chan struct{})
	app.stopBackground = func(context.Context, do.Injector) error {
		close(stopEntered)
		<-stopRelease
		return nil
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := runDaemonAppAsync(app, runCtx)
	cancelRun()
	select {
	case <-stopEntered:
	case err := <-runDone:
		t.Fatalf("Run returned before stopping background managers: %v", err)
	}
	select {
	case err := <-runDone:
		t.Fatalf("Run returned before background shutdown completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(stopRelease)
	if err := <-runDone; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestDaemonAppGivesBackgroundShutdownAnIndependentDeadline(t *testing.T) {
	requestEntered := make(chan struct{})
	requestRelease := make(chan struct{})
	releaseRequest := sync.OnceFunc(func() { close(requestRelease) })
	defer releaseRequest()
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(requestEntered)
		<-requestRelease
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() {
		if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("close server: %v", err)
		}
	}()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()

	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + listener.Addr().String())
		if requestErr == nil {
			requestErr = response.Body.Close()
		}
		requestDone <- requestErr
	}()
	select {
	case <-requestEntered:
	case <-time.After(time.Second):
		t.Fatal("request did not enter blocking handler")
	}

	backgroundContextErr := make(chan error, 1)
	app := &DaemonApp{
		shutdownTimeout: 50 * time.Millisecond,
		stopBackground: func(ctx context.Context, _ do.Injector) error {
			backgroundContextErr <- ctx.Err()
			return nil
		},
	}
	servers := &daemonServers{items: []*daemonServer{{
		name: "test", value: listener.Addr().String(), listener: listener, server: server,
	}}}
	shutdownErr := app.shutdown(servers)
	if shutdownErr == nil {
		t.Fatal("shutdown returned nil error while an active request exceeded the server deadline")
	}
	if err := <-backgroundContextErr; err != nil {
		t.Fatalf("background shutdown received expired context: %v", err)
	}

	releaseRequest()
	select {
	case requestErr := <-requestDone:
		if requestErr != nil {
			t.Errorf("request returned error: %v", requestErr)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not finish after handler release")
	}
	if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve returned error: %v", err)
	}
}

func testDaemonAppStartsBackgroundOnce(t *testing.T) {
	t.Helper()
	starts := 0
	app, cancel := newTestDaemonApp(t, "127.0.0.1:0", func(di do.Injector) error {
		starts++
		return nil
	})
	defer cancel()

	if err := app.StartBackground(); err != nil {
		t.Fatalf("first StartBackground returned error: %v", err)
	}
	if err := app.StartBackground(); err != nil {
		t.Fatalf("second StartBackground returned error: %v", err)
	}
	if starts != 1 {
		t.Fatalf("background start count = %d, want 1", starts)
	}
}

func TestDaemonAppReportsTCPPortConflict(t *testing.T) {
	testDaemonAppReportsTCPPortConflict(t)
}

func testDaemonAppReportsTCPPortConflict(t *testing.T) {
	t.Helper()
	socketPath := shortUnixSocketPath(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen occupied tcp port: %v", err)
	}
	defer func() {
		if err := occupied.Close(); err != nil {
			t.Fatalf("close occupied tcp port: %v", err)
		}
	}()
	httpListen := occupied.Addr().String()

	app, cancel := newTestDaemonAppWithSocketAndTCP(t, socketPath, httpListen, nil)
	defer cancel()
	err = app.Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil error, want tcp port conflict")
	}
	for _, part := range []string{"HTTP_LISTEN", httpListen} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("error %q does not contain %q", err.Error(), part)
		}
	}
	if _, statErr := os.Stat(socketPath); !errorsIsNotExist(statErr) {
		t.Fatalf("socket path was not cleaned after tcp listen failure, stat err=%v", statErr)
	}
}

func newTestDaemonApp(t *testing.T, httpListen string, startBackground func(do.Injector) error) (*DaemonApp, context.CancelFunc) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("HTTP_LISTEN", httpListen)
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

func runDaemonAppAsync(app *DaemonApp, ctx context.Context) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx)
	}()
	return errCh
}

func waitForDaemonExit(t *testing.T, errCh <-chan error) {
	t.Helper()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("daemon Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon Run did not return after shutdown")
	}
}
