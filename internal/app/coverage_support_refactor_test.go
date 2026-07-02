package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func TestSupportConstructorsAndHelpers(t *testing.T) {
	testSupportConstructorsAndHelpers(t)
}

func TestSupportControlPlaneStartAndConfigHelpers(t *testing.T) {
	testSupportControlPlaneStartAndConfigHelpers(t)
}

func TestSupportSetupRegistersServiceGraph(t *testing.T) {
	testSupportSetupRegistersServiceGraph(t)
}

func testSupportSetupRegistersServiceGraph(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("SESSION_ROOT", filepath.Join(root, "sessions"))
	t.Setenv("RUNTIME_DRIVER", driverpkg.RuntimeDriverDocker)
	t.Setenv("DOCKER_IMAGE", "guest:latest")
	t.Setenv("SESSION_START_TIMEOUT", "1s")
	t.Setenv("SESSION_STOP_TIMEOUT", "1s")
	t.Setenv("JUPYTER_PROXY_BASE", "/agent-compose/jupyter/")
	t.Setenv("LLM_API_ENDPOINT", "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	di := do.New()
	appconfig.Setup(di)
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, slog.Default())
	do.ProvideValue(di, echo.New())
	Setup(di)

	app := do.MustInvoke[*echo.Echo](di)
	if len(app.Routes()) == 0 {
		t.Fatalf("expected Setup to register routes")
	}
	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/agentcompose.v2.ProjectService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.RunService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.ExecService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.ImageService/*"},
		{method: http.MethodGet, path: "/agent-compose/jupyter/:sessionID"},
		{method: http.MethodPost, path: "/agent-compose/jupyter/:sessionID/*"},
	} {
		if !hasEchoRoute(app, route.method, route.path) {
			t.Fatalf("%s %s route was not registered", route.method, route.path)
		}
	}
	config := do.MustInvoke[*appconfig.Config](di)
	req := httptest.NewRequest(http.MethodGet, strings.TrimRight(config.JupyterProxyBasePath, "/")+"/missing", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("proxy route status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func hasEchoRoute(app *echo.Echo, method string, path string) bool {
	for _, route := range app.Routes() {
		if route.Method == method && route.Path == path {
			return true
		}
	}
	return false
}

func testSupportControlPlaneStartAndConfigHelpers(t *testing.T) {
	t.Helper()
	config := &appconfig.Config{
		DataRoot:             t.TempDir(),
		SessionRoot:          filepath.Join(t.TempDir(), "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		JupyterProxyBasePath: "/agent-compose/session",
		JupyterGuestPort:     8888,
		GuestWorkspacePath:   "/workspace",
		GuestHomePath:        "/home/agent-compose",
		GuestRuntimeRoot:     "/agent-compose",
		SessionStartTimeout:  time.Second,
		SessionStopTimeout:   time.Second,
	}
	if config.SessionRoot == "" || config.JupyterProxyBasePath == "" {
		t.Fatalf("test config was not initialized")
	}
}

func testSupportConstructorsAndHelpers(t *testing.T) {
	t.Helper()
	longStderr := strings.Repeat("x", 300)
	if got := summarizeAgentExecFailure(ExecResult{Stderr: "  line one\nline two  "}); got != "line one line two" {
		t.Fatalf("summarizeAgentExecFailure whitespace = %q", got)
	}
	if got := summarizeAgentExecFailure(ExecResult{Stderr: longStderr}); len(got) != 243 || !strings.HasSuffix(got, "...") {
		t.Fatalf("summarizeAgentExecFailure long = %q len=%d", got, len(got))
	}
	if got := summarizeAgentResult(AgentRunResult{Agent: "codex", Success: true}); got != "codex finished without output" {
		t.Fatalf("summarizeAgentResult success empty = %q", got)
	}
	if got := summarizeAgentResult(AgentRunResult{Agent: "codex", Success: false}); got != "codex failed without output" {
		t.Fatalf("summarizeAgentResult failed empty = %q", got)
	}
	if got := summarizeAgentResult(AgentRunResult{DisplayOutput: "display"}); got != "display" {
		t.Fatalf("summarizeAgentResult display = %q", got)
	}
	if fromProtoCellType(agentcomposev1.CellType_CELL_TYPE_SHELL) != CellTypeShell ||
		fromProtoCellType(agentcomposev1.CellType_CELL_TYPE_PYTHON) != CellTypePython ||
		fromProtoCellType(agentcomposev1.CellType_CELL_TYPE_AGENT) != CellTypeAgent ||
		fromProtoCellType(agentcomposev1.CellType_CELL_TYPE_UNSPECIFIED) != CellTypeJavaScript {
		t.Fatalf("fromProtoCellType returned unexpected values")
	}
	if toProtoCellType(CellTypeShell) != agentcomposev1.CellType_CELL_TYPE_SHELL ||
		toProtoCellType(CellTypePython) != agentcomposev1.CellType_CELL_TYPE_PYTHON ||
		toProtoCellType(CellTypeAgent) != agentcomposev1.CellType_CELL_TYPE_AGENT ||
		toProtoCellType("unknown") != agentcomposev1.CellType_CELL_TYPE_JAVASCRIPT {
		t.Fatalf("toProtoCellType returned unexpected values")
	}
	if firstNonZeroInt(0, 0, 7, 9) != 7 || firstNonZeroInt(0, 0) != 0 {
		t.Fatalf("firstNonZeroInt returned unexpected values")
	}
}
