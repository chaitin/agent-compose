package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestSetupRegistersServiceGraph(t *testing.T) {
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
		{method: http.MethodPost, path: "/agentcompose.v2.SandboxService/*"},
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

func TestRunAgentRequestFromProtoPreservesCommand(t *testing.T) {
	req := runAgentRequestFromProto(&agentcomposev2.RunAgentRequest{
		ProjectId: "project-1",
		AgentName: "worker",
		Prompt:    "prompt",
		Command:   "echo hi",
		TriggerId: "trigger-1",
	})
	if req.ProjectID != "project-1" || req.AgentName != "worker" || req.Prompt != "prompt" || req.Command != "echo hi" || req.TriggerID != "trigger-1" {
		t.Fatalf("mapped request = %#v", req)
	}
}

func TestApplyProjectValidationIssuesOmitProjectAndRevision(t *testing.T) {
	handler := projectControllerDelegate{controller: projects.NewController(projects.ControllerDependencies{})}
	resp, err := handler.ApplyProject(context.Background(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{}))
	if err != nil {
		t.Fatalf("ApplyProject returned error: %v", err)
	}
	if len(resp.Msg.GetIssues()) == 0 {
		t.Fatalf("expected validation issues, got %#v", resp.Msg)
	}
	if resp.Msg.GetProject() != nil || resp.Msg.GetRevision() != nil {
		t.Fatalf("validation failure project=%#v revision=%#v", resp.Msg.GetProject(), resp.Msg.GetRevision())
	}
}

func TestStopProjectSessionUsesInternalStopSemantics(t *testing.T) {
	sessionID := "session-1"
	store := &projectStopSessionStore{
		session: &domain.Session{Summary: domain.SessionSummary{
			ID:       sessionID,
			VMStatus: domain.VMStatusRunning,
		}},
	}
	driver := &projectStopSessionDriver{}
	streams := &projectStopSessionStreams{}

	if err := stopProjectSession(context.Background(), store, driver, streams, store.session); err != nil {
		t.Fatalf("stopProjectSession returned error: %v", err)
	}
	if driver.stopCount != 1 {
		t.Fatalf("StopSessionVM calls = %d, want 1", driver.stopCount)
	}
	if store.updated == nil || store.updated.Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("updated session = %#v, want stopped", store.updated)
	}
	if len(store.events) != 1 || store.events[0].Type != "session.stopped" || store.events[0].Message != "session stopped" {
		t.Fatalf("events = %#v, want one session.stopped event", store.events)
	}
	if streams.updatedCount != 1 || streams.eventCount != 1 {
		t.Fatalf("stream notifications updated=%d events=%d, want 1/1", streams.updatedCount, streams.eventCount)
	}
}

type projectStopSessionStore struct {
	session *domain.Session
	updated *domain.Session
	events  []domain.SessionEvent
}

func (s *projectStopSessionStore) GetSession(context.Context, string) (*domain.Session, error) {
	copy := *s.session
	return &copy, nil
}

func (s *projectStopSessionStore) UpdateSession(_ context.Context, session *domain.Session) error {
	copy := *session
	s.updated = &copy
	return nil
}

func (s *projectStopSessionStore) AddEvent(_ context.Context, _ string, event domain.SessionEvent) error {
	s.events = append(s.events, event)
	return nil
}

type projectStopSessionDriver struct {
	stopCount int
}

func (d *projectStopSessionDriver) StopSessionVM(context.Context, *domain.Session) error {
	d.stopCount++
	return nil
}

type projectStopSessionStreams struct {
	updatedCount int
	eventCount   int
}

func (s *projectStopSessionStreams) PublishSessionUpdated(*domain.SessionSummary) {
	s.updatedCount++
}

func (s *projectStopSessionStreams) PublishEventAdded(string, domain.SessionEvent) {
	s.eventCount++
}

func hasEchoRoute(app *echo.Echo, method string, path string) bool {
	for _, route := range app.Routes() {
		if route.Method == method && route.Path == path {
			return true
		}
	}
	return false
}
