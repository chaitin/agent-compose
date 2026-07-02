package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/workspaces"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func TestControlPlaneHelperErrorAndParsingBranches(t *testing.T) {
	testControlPlaneHelperErrorAndParsingBranches(t)
}

func testControlPlaneHelperErrorAndParsingBranches(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	if got, err := driverpkg.EnsureDockerImage(ctx, "  "); err != nil || got != "" {
		t.Fatalf("driverpkg.EnsureDockerImage(empty) = %q/%v, want empty nil", got, err)
	}
	if err := workspaces.ToWorkspaceUploadHTTPError(nil); err != nil {
		t.Fatalf("toWorkspaceUploadHTTPError(nil) = %v", err)
	}
	if httpErr, ok := workspaces.ToWorkspaceUploadHTTPError(errors.New("http: request body too large")).(*echo.HTTPError); !ok || httpErr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("upload large error = %#v", httpErr)
	}
	if httpErr, ok := workspaces.ToWorkspaceUploadHTTPError(errors.New("bad archive")).(*echo.HTTPError); !ok || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("upload bad error = %#v", httpErr)
	}
	for _, item := range []struct {
		err  error
		code int
	}{
		{errors.New("workspace not found"), http.StatusNotFound},
		{errors.New("workspace config is not a file workspace"), http.StatusBadRequest},
		{errors.New("invalid path"), http.StatusBadRequest},
		{errors.New("missing root"), http.StatusBadRequest},
		{errors.New("disk failed"), http.StatusInternalServerError},
	} {
		httpErr, ok := workspaces.ToWorkspaceHTTPError(item.err).(*echo.HTTPError)
		if !ok || httpErr.Code != item.code {
			t.Fatalf("toWorkspaceHTTPError(%v) = %#v, want %d", item.err, httpErr, item.code)
		}
	}

	root := t.TempDir()
	sessionConfig := &appconfig.Config{SessionRoot: filepath.Join(root, "sessions"), RuntimeDriver: driverpkg.RuntimeDriverBoxlite, DefaultImage: "guest:latest", JupyterProxyBasePath: "/agent-compose/session", JupyterGuestPort: 8888}
	store := mustTestStore(t, sessionConfig)
	if err := os.MkdirAll(sessionConfig.SessionRoot, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	created, err := store.CreateSession(ctx, "Stopped", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	service := &Service{store: store, llm: nil}
	if _, err := service.ExecuteCell(ctx, connect.NewRequest(&agentcomposev1.ExecuteCellRequest{SessionId: "missing"})); err == nil {
		t.Fatalf("ExecuteCell missing returned nil")
	}
	if _, err := service.ExecuteCell(ctx, connect.NewRequest(&agentcomposev1.ExecuteCellRequest{SessionId: created.Summary.ID, Type: agentcomposev1.CellType_CELL_TYPE_SHELL, Source: "echo"})); err == nil {
		t.Fatalf("ExecuteCell stopped returned nil")
	}
	if _, err := service.SendAgentMessage(ctx, connect.NewRequest(&agentcomposev1.SendAgentMessageRequest{SessionId: created.Summary.ID, Message: ""})); err == nil {
		t.Fatalf("SendAgentMessage stopped returned nil")
	}
	if _, err := service.Generate(ctx, connect.NewRequest(&agentcomposev1.GenerateLLMRequest{Prompt: "hello"})); err == nil {
		t.Fatalf("Generate without llm returned nil")
	}
	var nilService *Service
	if _, err := nilService.Generate(ctx, connect.NewRequest(&agentcomposev1.GenerateLLMRequest{Prompt: "hello"})); err == nil {
		t.Fatalf("Generate on nil service returned nil")
	}
}
