package app

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	dockerdriver "agent-compose/pkg/driver/docker"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	if got, err := dockerdriver.EnsureDockerImage(ctx, "  "); err != nil || got != "" {
		t.Fatalf("driverpkg.EnsureDockerImage(empty) = %q/%v, want empty nil", got, err)
	}
	if err := toWorkspaceUploadHTTPError(nil); err != nil {
		t.Fatalf("toWorkspaceUploadHTTPError(nil) = %v", err)
	}
	if httpErr, ok := toWorkspaceUploadHTTPError(errors.New("http: request body too large")).(*echo.HTTPError); !ok || httpErr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("upload large error = %#v", httpErr)
	}
	if httpErr, ok := toWorkspaceUploadHTTPError(errors.New("bad archive")).(*echo.HTTPError); !ok || httpErr.Code != http.StatusBadRequest {
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
		httpErr, ok := toWorkspaceHTTPError(item.err).(*echo.HTTPError)
		if !ok || httpErr.Code != item.code {
			t.Fatalf("toWorkspaceHTTPError(%v) = %#v, want %d", item.err, httpErr, item.code)
		}
	}

	if err := validateLoaderCommandRequest(LoaderCommandRequest{Mode: "exec"}); err == nil {
		t.Fatalf("validate exec without command returned nil")
	}
	if err := validateLoaderCommandRequest(LoaderCommandRequest{Mode: "shell"}); err == nil {
		t.Fatalf("validate shell without script returned nil")
	}
	if err := validateLoaderCommandRequest(LoaderCommandRequest{Mode: "bad"}); err == nil {
		t.Fatalf("validate bad mode returned nil")
	}
	if err := validateLoaderCommandRequest(LoaderCommandRequest{Mode: "exec", Command: "python3"}); err != nil {
		t.Fatalf("validate exec returned error: %v", err)
	}
	cancelCtx, cancel := loaderCommandContext(ctx, 0)
	cancel()
	if cancelCtx.Err() == nil {
		t.Fatalf("loaderCommandContext without timeout did not cancel")
	}
	timeoutCtx, timeoutCancel := loaderCommandContext(ctx, 1)
	defer timeoutCancel()
	select {
	case <-timeoutCtx.Done():
	case <-time.After(time.Second):
		t.Fatalf("loaderCommandContext timeout did not expire")
	}

	config := &appconfig.Config{
		GuestWorkspacePath: "/workspace",
		GuestHomePath:      "/root",
		GuestStateRoot:     "/data/state",
		GuestRuntimeRoot:   "/data/runtime",
		Version:            "v-test",
	}
	request := LoaderCommandRequest{Mode: "exec", Command: "python3", Args: []string{"-V"}, Env: map[string]string{"FOO": "bar"}}
	payload := runtimeCommandRequestPayload(config, request, "/guest/cell")
	if payload.Cwd != "/workspace" || payload.MaxOutputBytes != defaultLoaderCommandMaxOutputBytes || payload.ArtifactDir != "/guest/cell" {
		t.Fatalf("runtime command payload = %#v", payload)
	}
	session := &Session{Summary: SessionSummary{ID: "session-1"}, EnvItems: []SessionEnvVar{{Name: "CUSTOM", Value: "value"}}}
	spec := buildLoaderCommandExecSpec(config, session, "/guest/request.json")
	specCommand := strings.Join(spec.Args, " ")
	if spec.Command != "sh" || spec.Cwd != "/workspace" || spec.Env["CUSTOM"] != "value" || spec.Env["GOPATH"] != "/usr/local/go" || !strings.Contains(specCommand, "agent-compose-runtime exec") {
		t.Fatalf("loader command exec spec = %#v", spec)
	}
	for _, want := range []string{
		"cd '/workspace'",
		"--state-root '/data/state'",
		"--workspace '/workspace'",
		"--home '/root'",
	} {
		if !strings.Contains(specCommand, want) {
			t.Fatalf("loader command exec spec command %q missing %q", specCommand, want)
		}
	}
	if strings.Contains(specCommand, "cp -R /root/.codex/.") {
		t.Fatalf("loader command exec spec still syncs guest codex config: %q", specCommand)
	}
	for key, want := range map[string]string{
		"WORKSPACE":    "/workspace",
		"STATE_ROOT":   "/data/state",
		"RUNTIME_ROOT": "/data/runtime",
	} {
		if got := spec.Env[key]; got != want {
			t.Fatalf("loader command env %s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"HOME", "SESSION_WORKSPACE"} {
		if _, ok := spec.Env[key]; ok {
			t.Fatalf("loader command env still contains %s: %#v", key, spec.Env)
		}
	}
	if source := loaderCommandCellSource(LoaderCommandRequest{Mode: "shell", Script: "echo hi"}); source != "echo hi" {
		t.Fatalf("shell source = %q", source)
	}

	commandPayload := RuntimeCommandResult{Stdout: "out", Stderr: "err", Output: "outerr", ExitCode: 0, Success: true}
	payloadJSON, err := json.Marshal(commandPayload)
	if err != nil {
		t.Fatalf("marshal command payload: %v", err)
	}
	parsed, err := parseCommandExecResult(ExecResult{Stdout: "noise\n" + commandResultPrefix + string(payloadJSON)})
	if err != nil || !parsed.Success || parsed.Stdout != "out" {
		t.Fatalf("parseCommandExecResult(stdout) = %#v/%v", parsed, err)
	}
	parsed, err = parseCommandExecResult(ExecResult{Stdout: "noise", Output: string(payloadJSON)})
	if err != nil || parsed.Output != "outerr" {
		t.Fatalf("parseCommandExecResult(output fallback) = %#v/%v", parsed, err)
	}
	if _, err := parseCommandExecResult(ExecResult{}); err == nil {
		t.Fatalf("parseCommandExecResult(empty) returned nil")
	}
	if _, err := parseCommandExecResult(ExecResult{Stdout: "no json"}); err == nil {
		t.Fatalf("parseCommandExecResult(no payload) returned nil")
	}
	artifactDir := t.TempDir()
	if err := mirrorRuntimeCommandArtifacts(artifactDir, commandPayload); err != nil {
		t.Fatalf("mirrorRuntimeCommandArtifacts returned error: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(artifactDir, "stdout.txt")); err != nil || string(data) != "out" {
		t.Fatalf("stdout artifact = %q/%v", string(data), err)
	}

	agentPayload := agentExecResponse{Provider: "codex", FinalText: "done", Transcript: "transcript", SessionID: "agent-session", StopReason: "completed"}
	agentJSON, err := json.Marshal(agentPayload)
	if err != nil {
		t.Fatalf("marshal agent payload: %v", err)
	}
	agentResult, err := parseAgentExecResult("codex", ExecResult{Stdout: "noise\n" + agentResultPrefix + string(agentJSON), ExitCode: 0, Success: true})
	if err != nil || agentResult.SessionID != "agent-session" || agentResult.DisplayOutput != "transcript" {
		t.Fatalf("parseAgentExecResult = %#v/%v", agentResult, err)
	}
	if _, err := parseAgentExecResult("codex", ExecResult{}); err == nil {
		t.Fatalf("parseAgentExecResult(empty) returned nil")
	}
	if _, err := parseAgentExecResult("codex", ExecResult{Stdout: "no payload", Stderr: "stderr detail"}); err == nil || !strings.Contains(err.Error(), "stderr detail") {
		t.Fatalf("parseAgentExecResult(no payload) = %v", err)
	}
	if stripped := stripAgentResultPayload("hello\n" + agentResultPrefix + string(agentJSON)); strings.TrimSpace(stripped) != "hello" {
		t.Fatalf("stripAgentResultPayload = %q", stripped)
	}

	if got := int64FromMap(map[string]any{"n": json.Number("42")}, "n"); got != 42 {
		t.Fatalf("int64FromMap(json.Number) = %d", got)
	}
	if got := int64FromMap(map[string]any{"n": "bad"}, "n"); got != 0 {
		t.Fatalf("int64FromMap(bad) = %d", got)
	}
	if err := validateLoaderPublishTopic("bad.topic"); err == nil {
		t.Fatalf("validateLoaderPublishTopic bad prefix returned nil")
	}
	if err := validateLoaderPublishTopic("runtime.good"); err != nil {
		t.Fatalf("validateLoaderPublishTopic runtime.good = %v", err)
	}
	if jsonObjectDocument(`[]`) || !jsonObjectDocument(`{"ok":true}`) {
		t.Fatalf("jsonObjectDocument returned unexpected values")
	}

	root := t.TempDir()
	storeConfig := &appconfig.Config{SessionRoot: filepath.Join(root, "sessions"), RuntimeDriver: driverpkg.RuntimeDriverBoxlite, DefaultImage: "guest:latest", JupyterProxyBasePath: "/agent-compose/session", JupyterGuestPort: 8888}
	store := NewStoreForConfig(storeConfig)
	if err := os.MkdirAll(storeConfig.SessionRoot, 0o755); err != nil {
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
}
