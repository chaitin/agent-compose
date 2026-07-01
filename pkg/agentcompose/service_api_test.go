package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"google.golang.org/protobuf/types/known/emptypb"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
)

func TestServiceConfigAndLoaderAPIs(t *testing.T) {
	testServiceConfigAndLoaderAPIs(t)
}

func TestUpdateGlobalEnvConfigPreservesUnchangedSecrets(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newTestServiceAPIHarness(t)

	if _, err := service.UpdateGlobalEnvConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateGlobalEnvConfigRequest{
		EnvItems: []*agentcomposev1.SessionEnvVar{
			{Name: "PLAIN", Value: "visible"},
			{Name: "TOKEN", Value: "secret-value", Secret: true},
		},
	})); err != nil {
		t.Fatalf("UpdateGlobalEnvConfig(initial) returned error: %v", err)
	}
	if _, err := service.UpdateGlobalEnvConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateGlobalEnvConfigRequest{
		EnvItems: []*agentcomposev1.SessionEnvVar{
			{Name: "FOO", Value: "bar"},
			{Name: "PLAIN", Value: "visible"},
			{Name: "TOKEN", Value: "", Secret: true},
		},
	})); err != nil {
		t.Fatalf("UpdateGlobalEnvConfig(preserve secret) returned error: %v", err)
	}
	stored, err := service.configDB.ListGlobalEnv(ctx)
	if err != nil {
		t.Fatalf("ListGlobalEnv returned error: %v", err)
	}
	env := sessionEnvMap(stored)
	if got := env["TOKEN"]; got != "secret-value" {
		t.Fatalf("stored TOKEN = %q, want preserved secret value", got)
	}
	if got := env["FOO"]; got != "bar" {
		t.Fatalf("stored FOO = %q, want bar", got)
	}

	if _, err := service.UpdateGlobalEnvConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateGlobalEnvConfigRequest{
		EnvItems: []*agentcomposev1.SessionEnvVar{{Name: "NEW_TOKEN", Value: "", Secret: true}},
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("UpdateGlobalEnvConfig(new empty secret) code = %v, want invalid_argument", connect.CodeOf(err))
	}

	if _, err := service.UpdateGlobalEnvConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateGlobalEnvConfigRequest{
		EnvItems: []*agentcomposev1.SessionEnvVar{{Name: "PLAIN", Value: "visible"}},
	})); err != nil {
		t.Fatalf("UpdateGlobalEnvConfig(delete secret) returned error: %v", err)
	}
	stored, err = service.configDB.ListGlobalEnv(ctx)
	if err != nil {
		t.Fatalf("ListGlobalEnv(after delete) returned error: %v", err)
	}
	env = sessionEnvMap(stored)
	if _, ok := env["TOKEN"]; ok {
		t.Fatalf("TOKEN was preserved after deletion request: %#v", stored)
	}
}

func TestServiceSessionKernelAgentAndLLMAPIs(t *testing.T) {
	testServiceSessionKernelAgentAndLLMAPIs(t)
}

func TestServiceGenerateLLMChatCompletionsProtocol(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_ENDPOINT", "")

	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readRequestBodyForTest(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-svc","model":"model-a","choices":[{"message":{"role":"assistant","content":"llm text"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)

	service := &Service{
		llm: &LLMClient{
			config: &appconfig.Config{
				LLMAPIEndpoint: server.URL,
				LLMAPIProtocol: llmAPIProtocolChatCompletions,
				LLMModel:       "model-a",
			},
			configDB: newTestConfigStore(t),
			client:   server.Client(),
		},
	}
	resp, err := service.Generate(ctx, connect.NewRequest(&agentcomposev1.GenerateLLMRequest{
		Prompt: "hello",
		Model:  "model-a",
	}))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if resp.Msg.GetText() != "llm text" || resp.Msg.GetResponseId() != "chatcmpl-svc" || resp.Msg.GetFinishReason() != "stop" {
		t.Fatalf("unexpected llm response: %+v", resp.Msg)
	}
	if !strings.Contains(gotBody, `"messages":[{"role":"user","content":"hello"}`) {
		t.Fatalf("expected chat completions request body, got %s", gotBody)
	}
}

func testServiceSessionKernelAgentAndLLMAPIs(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	service, runtime, driver := newTestServiceAPIHarness(t)

	created, err := service.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{
		Title:      "Service Session",
		Driver:     driverpkg.RuntimeDriverBoxlite,
		GuestImage: "guest:latest",
		Tags: []*agentcomposev1.SessionTag{
			{Name: "purpose", Value: "coverage"},
		},
		EnvItems: []*agentcomposev1.SessionEnvVar{
			{Name: "PLAIN", Value: "value"},
		},
	}))
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	sessionID := created.Msg.GetSession().GetSummary().GetSessionId()
	if sessionID == "" {
		t.Fatalf("expected session id")
	}
	if len(driver.startCalls) != 1 || driver.startCalls[0] != sessionID {
		t.Fatalf("start calls = %v, want [%s]", driver.startCalls, sessionID)
	}
	assertNextLoaderTopic(t, service.bus, "agent-compose.session.created")

	got, err := service.GetSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID}))
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if got.Msg.GetSession().GetSummary().GetVmStatus() != VMStatusRunning {
		t.Fatalf("session vm status = %q, want running", got.Msg.GetSession().GetSummary().GetVmStatus())
	}
	listed, err := service.ListSessions(ctx, connect.NewRequest(&agentcomposev1.ListSessionsRequest{Limit: 10}))
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if len(listed.Msg.GetSessions()) != 1 {
		t.Fatalf("session count = %d, want 1", len(listed.Msg.GetSessions()))
	}
	proxy, err := service.GetSessionProxy(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID}))
	if err != nil {
		t.Fatalf("GetSessionProxy returned error: %v", err)
	}
	if proxy.Msg.GetProxyPath() == "" || proxy.Msg.GetNotebookUrl() == "" {
		t.Fatalf("proxy response missing path/notebook url: %+v", proxy.Msg)
	}

	cellResp, err := service.ExecuteCell(ctx, connect.NewRequest(&agentcomposev1.ExecuteCellRequest{
		SessionId: sessionID,
		Type:      agentcomposev1.CellType_CELL_TYPE_SHELL,
		Source:    "echo hello",
	}))
	if err != nil {
		t.Fatalf("ExecuteCell returned error: %v", err)
	}
	if cellResp.Msg.GetCell().GetStdout() != "cell stdout\n" || !cellResp.Msg.GetCell().GetSuccess() {
		t.Fatalf("unexpected cell response: %+v", cellResp.Msg.GetCell())
	}
	assertNextLoaderTopic(t, service.bus, "agent-compose.cell.completed")
	cells, err := service.ListCells(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID}))
	if err != nil {
		t.Fatalf("ListCells returned error: %v", err)
	}
	if len(cells.Msg.GetCells()) != 1 {
		t.Fatalf("cell count = %d, want 1", len(cells.Msg.GetCells()))
	}

	agentResp, err := service.SendAgentMessage(ctx, connect.NewRequest(&agentcomposev1.SendAgentMessageRequest{
		SessionId: sessionID,
		Agent:     "claude-code",
		Message:   "summarize",
	}))
	if err != nil {
		t.Fatalf("SendAgentMessage returned error: %v", err)
	}
	if agentResp.Msg.GetAssistantEvent().GetType() != "agent.assistant" {
		t.Fatalf("assistant event type = %q", agentResp.Msg.GetAssistantEvent().GetType())
	}
	if len(runtime.providers) != 1 || runtime.providers[0] != "claude" {
		t.Fatalf("runtime providers = %v, want [claude]", runtime.providers)
	}
	assertNextLoaderTopic(t, service.bus, "agent-compose.agent.completed")

	events, err := service.ListSessionEvents(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID}))
	if err != nil {
		t.Fatalf("ListSessionEvents returned error: %v", err)
	}
	if len(events.Msg.GetEvents()) < 4 {
		t.Fatalf("event count = %d, want at least 4", len(events.Msg.GetEvents()))
	}

	llmResp, err := service.Generate(ctx, connect.NewRequest(&agentcomposev1.GenerateLLMRequest{
		Prompt:       "hello",
		Model:        "model-a",
		OutputSchema: `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`,
	}))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if llmResp.Msg.GetText() != `{"answer":"ok"}` || llmResp.Msg.GetJson() != `{"answer":"ok"}` || llmResp.Msg.GetResponseId() != "resp-service" {
		t.Fatalf("unexpected llm response: %+v", llmResp.Msg)
	}

	resumed, err := service.ResumeSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID}))
	if err != nil {
		t.Fatalf("ResumeSession returned error: %v", err)
	}
	if resumed.Msg.GetSession().GetSummary().GetVmStatus() != VMStatusRunning {
		t.Fatalf("resumed status = %q, want running", resumed.Msg.GetSession().GetSummary().GetVmStatus())
	}
	assertNextLoaderTopic(t, service.bus, "agent-compose.session.resumed")

	stopped, err := service.StopSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID}))
	if err != nil {
		t.Fatalf("StopSession returned error: %v", err)
	}
	if stopped.Msg.GetSession().GetSummary().GetVmStatus() != VMStatusStopped {
		t.Fatalf("stopped status = %q, want stopped", stopped.Msg.GetSession().GetSummary().GetVmStatus())
	}
	if len(driver.stopCalls) != 1 || driver.stopCalls[0] != sessionID {
		t.Fatalf("stop calls = %v, want [%s]", driver.stopCalls, sessionID)
	}
	assertNextLoaderTopic(t, service.bus, "agent-compose.session.stopped")
}

func assertNextLoaderTopic(t *testing.T, bus *LoaderBus, want string) {
	t.Helper()
	select {
	case event := <-bus.Events():
		if event.Topic != want {
			t.Fatalf("loader topic = %q, want %q", event.Topic, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for loader topic %q", want)
	}
}

func TestServiceProxyRoutesRedirectAndProxy(t *testing.T) {
	testServiceProxyRoutesRedirectAndProxy(t)
}

func testServiceProxyRoutesRedirectAndProxy(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	service, _, _ := newTestServiceAPIHarness(t)
	created, err := service.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{
		Title:      "Proxy Session",
		Driver:     driverpkg.RuntimeDriverBoxlite,
		GuestImage: "guest:latest",
	}))
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	sessionID := created.Msg.GetSession().GetSummary().GetSessionId()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent-compose/session/"+sessionID+"/api/status" {
			t.Fatalf("backend path = %q", r.URL.Path)
		}
		if r.URL.RawQuery != "x=1" {
			t.Fatalf("backend query = %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte("proxied"))
	}))
	t.Cleanup(backend.Close)
	hostPort := httptestServerPort(t, backend.URL)
	if err := service.store.SaveProxyState(sessionID, ProxyState{
		ProxyPath:  "/agent-compose/session/" + sessionID + "/lab",
		GuestHost:  "127.0.0.1",
		HostPort:   hostPort,
		GuestPort:  8888,
		JupyterURL: backend.URL,
		Token:      "secret token",
	}); err != nil {
		t.Fatalf("SaveProxyState returned error: %v", err)
	}

	app := echo.New()
	registerProxyRoutes(app, service)

	req := httptest.NewRequest(http.MethodGet, "/agent-compose/session/"+sessionID, nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("redirect status = %d, want %d body %s", rec.Code, http.StatusTemporaryRedirect, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); location != "/agent-compose/session/"+sessionID+"/lab?token=secret+token" {
		t.Fatalf("redirect location = %q", location)
	}

	req = httptest.NewRequest(http.MethodGet, "/agent-compose/session/"+sessionID+"/api/status?x=1", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, want 200 body %s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "proxied" {
		t.Fatalf("proxy body = %q", rec.Body.String())
	}
}

func TestServiceProxyRoutesUseGuestHostTarget(t *testing.T) {
	ctx := context.Background()
	service, _, driver := newTestServiceAPIHarness(t)
	created, err := service.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{
		Title:      "Docker Proxy Session",
		Driver:     driverpkg.RuntimeDriverDocker,
		GuestImage: "guest:latest",
	}))
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	sessionID := created.Msg.GetSession().GetSummary().GetSessionId()
	driver.startCalls = nil

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent-compose/session/"+sessionID+"/api/status" {
			t.Fatalf("backend path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte("guest-proxied"))
	}))
	t.Cleanup(backend.Close)
	if err := service.store.SaveProxyState(sessionID, ProxyState{
		ProxyPath: "/agent-compose/session/" + sessionID + "/lab",
		GuestHost: "localhost",
		HostPort:  unusedLocalTCPPort(t),
		GuestPort: httptestServerPort(t, backend.URL),
		Token:     "secret",
	}); err != nil {
		t.Fatalf("SaveProxyState returned error: %v", err)
	}

	app := echo.New()
	registerProxyRoutes(app, service)

	req := httptest.NewRequest(http.MethodGet, "/agent-compose/session/"+sessionID+"/api/status", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, want 200 body %s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "guest-proxied" {
		t.Fatalf("proxy body = %q", rec.Body.String())
	}
	if len(driver.startCalls) != 0 {
		t.Fatalf("driver start calls = %v, want none when guest target is reachable", driver.startCalls)
	}
}

func TestServiceEnsureProxyReadyStartPaths(t *testing.T) {
	testServiceEnsureProxyReadyStartPaths(t)
}

func TestServiceProtoConversionHelpers(t *testing.T) {
	testServiceProtoConversionHelpers(t)
}

func testServiceProtoConversionHelpers(t *testing.T) {
	t.Helper()
	now := time.Date(2026, 6, 2, 9, 10, 11, 12, time.UTC)
	session := &Session{
		Summary: SessionSummary{
			ID:            "session-proto",
			Title:         "Proto Session",
			TriggerSource: "manual",
			Driver:        driverpkg.RuntimeDriverDocker,
			VMStatus:      VMStatusRunning,
			GuestImage:    "guest:latest",
			WorkspacePath: "/workspace",
			ProxyPath:     "/agent-compose/session/session-proto/lab",
			CreatedAt:     now,
			UpdatedAt:     now,
			CellCount:     2,
			EventCount:    1,
			Tags:          []SessionTag{{Name: "kind", Value: "proto"}},
		},
		WorkspaceID: "workspace-1",
		Workspace:   &SessionWorkspace{ID: "workspace-1", Name: "Workspace", Type: "file", ConfigJSON: `{"root":"."}`},
		EnvItems: []SessionEnvVar{
			{Name: "PLAIN", Value: "value"},
			{Name: "SECRET", Value: "secret-value", Secret: true},
		},
	}
	detail := toProtoSessionDetail(session)
	if detail.GetSummary().GetSessionId() != "session-proto" || detail.GetWorkspace().GetId() != "workspace-1" {
		t.Fatalf("session detail = %+v", detail)
	}
	if len(detail.GetEnvItems()) != 2 || detail.GetEnvItems()[1].GetValue() != "********" {
		t.Fatalf("session env items = %+v", detail.GetEnvItems())
	}
	globalEnv := toProtoGlobalEnvConfig([]SessionEnvVar{{Name: "VISIBLE", Value: "visible"}, {Name: "TOKEN", Value: "token", Secret: true}})
	if len(globalEnv.GetEnvItems()) != 2 || globalEnv.GetEnvItems()[1].GetValue() != "********" {
		t.Fatalf("global env = %+v", globalEnv.GetEnvItems())
	}
	if toSessionWorkspaceSnapshot(WorkspaceConfig{ID: "ws", Name: "WS", Type: "git", ConfigJSON: "{}"}).Type != "git" {
		t.Fatalf("workspace snapshot conversion failed")
	}
	workspaceProto := toProtoWorkspaceConfig(WorkspaceConfig{ID: "ws", Name: "WS", Type: "git", ConfigJSON: "{}", Comment: "note", CreatedAt: now, UpdatedAt: now})
	if workspaceProto.GetId() != "ws" || workspaceProto.GetComment() != "note" {
		t.Fatalf("workspace proto = %+v", workspaceProto)
	}
	if toProtoSessionWorkspace(nil) != nil {
		t.Fatalf("nil session workspace did not convert to nil")
	}

	cell := NotebookCell{
		ID:             "cell-agent",
		Type:           CellTypeAgent,
		Source:         "prompt",
		Stdout:         "stdout",
		Stderr:         "stderr",
		Output:         "",
		ExitCode:       3,
		Success:        false,
		Running:        true,
		CreatedAt:      now,
		Agent:          "codex",
		AgentSessionID: "agent-session",
		StopReason:     "failed",
	}
	cellProto := toProtoCell(cell)
	if cellProto.GetType() != agentcomposev1.CellType_CELL_TYPE_AGENT || cellProto.GetOutput() != "stdoutstderr" || cellProto.GetExitCode() != 3 {
		t.Fatalf("cell proto = %+v", cellProto)
	}
	agentProto := toProtoAgentRun(cell)
	if agentProto.GetAgentSessionId() != "agent-session" || !agentProto.GetRunning() {
		t.Fatalf("agent proto = %+v", agentProto)
	}
	for _, item := range []struct {
		proto agentcomposev1.CellType
		local string
	}{
		{agentcomposev1.CellType_CELL_TYPE_SHELL, CellTypeShell},
		{agentcomposev1.CellType_CELL_TYPE_PYTHON, CellTypePython},
		{agentcomposev1.CellType_CELL_TYPE_AGENT, CellTypeAgent},
		{agentcomposev1.CellType_CELL_TYPE_JAVASCRIPT, CellTypeJavaScript},
		{agentcomposev1.CellType_CELL_TYPE_UNSPECIFIED, CellTypeJavaScript},
	} {
		if got := fromProtoCellType(item.proto); got != item.local {
			t.Fatalf("fromProtoCellType(%v) = %q, want %q", item.proto, got, item.local)
		}
		if got := toProtoCellType(item.local); item.local != CellTypeJavaScript && got != item.proto {
			t.Fatalf("toProtoCellType(%q) = %v, want %v", item.local, got, item.proto)
		}
	}
	if toProtoCellType("unknown") != agentcomposev1.CellType_CELL_TYPE_JAVASCRIPT {
		t.Fatalf("unknown cell type did not map to javascript")
	}
	eventProto := toProtoEvent(SessionEvent{ID: "event-1", Type: "session.test", Level: "info", Message: "tested", CreatedAt: now})
	if eventProto.GetId() != "event-1" || eventProto.GetCreatedAt() == "" {
		t.Fatalf("event proto = %+v", eventProto)
	}
}

func testServiceEnsureProxyReadyStartPaths(t *testing.T) {
	ctx := context.Background()
	service, _, driver := newTestServiceAPIHarness(t)
	created, err := service.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{
		Title:      "Restart Session",
		Driver:     driverpkg.RuntimeDriverBoxlite,
		GuestImage: "guest:latest",
	}))
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	sessionID := created.Msg.GetSession().GetSummary().GetSessionId()
	session, err := service.store.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	session.Summary.VMStatus = VMStatusStopped
	if err := service.store.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	proxyState, err := service.store.GetProxyState(sessionID)
	if err != nil {
		t.Fatalf("GetProxyState returned error: %v", err)
	}
	proxyState.JupyterURL = "http://127.0.0.1:" + fmt.Sprint(proxyState.HostPort) + "/lab?token=" + proxyState.Token
	if err := service.store.SaveProxyState(sessionID, proxyState); err != nil {
		t.Fatalf("SaveProxyState returned error: %v", err)
	}
	driver.startCalls = nil

	loaded, readyProxy, err := service.ensureSessionProxyReady(ctx, sessionID)
	if err != nil {
		t.Fatalf("ensureSessionProxyReady returned error: %v", err)
	}
	if loaded.Summary.VMStatus != VMStatusRunning {
		t.Fatalf("loaded vm status = %q, want running", loaded.Summary.VMStatus)
	}
	if readyProxy.JupyterURL != proxyState.JupyterURL {
		t.Fatalf("ready proxy url = %q, want %q", readyProxy.JupyterURL, proxyState.JupyterURL)
	}
	if len(driver.startCalls) != 1 || driver.startCalls[0] != sessionID {
		t.Fatalf("driver start calls = %#v, want [%s]", driver.startCalls, sessionID)
	}
	if _, _, err := service.ensureSessionProxyReady(ctx, "missing-session"); err == nil {
		t.Fatalf("ensureSessionProxyReady missing returned nil error")
	}

	session.Summary.VMStatus = VMStatusStopped
	if err := service.store.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession stopped returned error: %v", err)
	}
	service.sessions.runtimes = service.runtimes
	bridgeLoaded, bridgeProxy, err := service.sessions.ensureSessionProxyReady(ctx, sessionID)
	if err != nil {
		t.Fatalf("bridge ensureSessionProxyReady returned error: %v", err)
	}
	if bridgeLoaded.Summary.VMStatus != VMStatusRunning || bridgeProxy.ProxyPath == "" {
		t.Fatalf("bridge loaded/proxy = %+v/%+v", bridgeLoaded.Summary, bridgeProxy)
	}
	if _, err := service.reconcileSessionRuntimeState(ctx, nil); err != nil {
		t.Fatalf("reconcile nil returned error: %v", err)
	}
	if _, err := service.sessions.reconcileSessionRuntimeState(ctx, nil); err != nil {
		t.Fatalf("bridge reconcile nil returned error: %v", err)
	}
}

func httptestServerPort(t *testing.T, rawURL string) int {
	t.Helper()
	parts := strings.Split(rawURL, ":")
	if len(parts) < 3 {
		t.Fatalf("unexpected httptest URL: %s", rawURL)
	}
	var port int
	if _, err := fmt.Sscanf(parts[len(parts)-1], "%d", &port); err != nil {
		t.Fatalf("parse httptest port from %q: %v", rawURL, err)
	}
	return port
}

func TestServiceStreamingAPIs(t *testing.T) {
	testServiceStreamingAPIs(t)
}

func TestAgentTraceEventsDoNotAttachAssistantTextToLastTool(t *testing.T) {
	t.Run("with input", func(t *testing.T) {
		events := agentTraceEvents(`[tool:WebSearch]
{
  "query": "first"
}

claude answer`, time.Now())
		if len(events) != 1 {
			t.Fatalf("trace event count = %d, want 1", len(events))
		}
		if events[0].Type != "agent.tool" || !strings.Contains(events[0].Message, "first") {
			t.Fatalf("tool event = %+v", events[0])
		}
		if strings.Contains(events[0].Message, "claude answer") {
			t.Fatalf("tool event should not include assistant text: %+v", events[0])
		}
	})
	t.Run("without input", func(t *testing.T) {
		events := agentTraceEvents(`[tool:NoInputTool]

claude answer`, time.Now())
		if len(events) != 1 {
			t.Fatalf("trace event count = %d, want 1", len(events))
		}
		if events[0].Message != "NoInputTool" {
			t.Fatalf("tool event message = %q, want NoInputTool", events[0].Message)
		}
	})
}

func testServiceStreamingAPIs(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	service, _, _ := newTestServiceAPIHarness(t)
	created, err := service.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{
		Title:      "Streaming Session",
		Driver:     driverpkg.RuntimeDriverBoxlite,
		GuestImage: "guest:latest",
	}))
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	sessionID := created.Msg.GetSession().GetSummary().GetSessionId()

	mux := http.NewServeMux()
	path, handler := agentcomposev1connect.NewSessionServiceHandler(service)
	mux.Handle(path, handler)
	path, handler = agentcomposev1connect.NewKernelServiceHandler(service)
	mux.Handle(path, handler)
	path, handler = agentcomposev1connect.NewAgentServiceHandler(service)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	sessionClient := agentcomposev1connect.NewSessionServiceClient(server.Client(), server.URL)
	watchCtx, cancelWatch := context.WithCancel(ctx)
	defer cancelWatch()
	watchStream, err := sessionClient.WatchSession(watchCtx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID}))
	if err != nil {
		t.Fatalf("WatchSession returned error: %v", err)
	}
	if !watchStream.Receive() {
		t.Fatalf("WatchSession initial receive failed: %v", watchStream.Err())
	}
	if got := watchStream.Msg().GetEventType(); got != agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_SESSION_UPDATED {
		t.Fatalf("WatchSession initial event = %v", got)
	}

	cellDone := make(chan error, 1)
	go func() {
		_, callErr := service.ExecuteCell(ctx, connect.NewRequest(&agentcomposev1.ExecuteCellRequest{
			SessionId: sessionID,
			Type:      agentcomposev1.CellType_CELL_TYPE_SHELL,
			Source:    "echo watch",
		}))
		cellDone <- callErr
	}()
	watchEvents := make([]agentcomposev1.WatchSessionEventType, 0, 4)
	watchOutput := ""
	for len(watchEvents) < 4 && watchStream.Receive() {
		msg := watchStream.Msg()
		watchEvents = append(watchEvents, msg.GetEventType())
		watchOutput += msg.GetChunk()
	}
	if err := <-cellDone; err != nil {
		t.Fatalf("ExecuteCell for watch stream returned error: %v", err)
	}
	cancelWatch()
	if len(watchEvents) != 4 ||
		watchEvents[0] != agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_CELL_STARTED ||
		watchEvents[1] != agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_CELL_OUTPUT ||
		watchEvents[2] != agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_CELL_COMPLETED ||
		watchEvents[3] != agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_EVENT_ADDED {
		t.Fatalf("watch stream events = %#v", watchEvents)
	}
	if watchOutput != "cell stdout\n" {
		t.Fatalf("watch stream output = %q", watchOutput)
	}

	kernel := agentcomposev1connect.NewKernelServiceClient(server.Client(), server.URL)
	cellStream, err := kernel.ExecuteCellStream(ctx, connect.NewRequest(&agentcomposev1.ExecuteCellRequest{
		SessionId: sessionID,
		Type:      agentcomposev1.CellType_CELL_TYPE_SHELL,
		Source:    "echo stream",
	}))
	if err != nil {
		t.Fatalf("ExecuteCellStream returned error: %v", err)
	}
	cellEvents := make([]agentcomposev1.ExecuteCellStreamEventType, 0)
	cellOutput := ""
	for cellStream.Receive() {
		msg := cellStream.Msg()
		cellEvents = append(cellEvents, msg.GetEventType())
		cellOutput += msg.GetChunk()
	}
	if err := cellStream.Err(); err != nil {
		t.Fatalf("cell stream returned error: %v", err)
	}
	if len(cellEvents) != 3 ||
		cellEvents[0] != agentcomposev1.ExecuteCellStreamEventType_EXECUTE_CELL_STREAM_EVENT_TYPE_STARTED ||
		cellEvents[1] != agentcomposev1.ExecuteCellStreamEventType_EXECUTE_CELL_STREAM_EVENT_TYPE_OUTPUT ||
		cellEvents[2] != agentcomposev1.ExecuteCellStreamEventType_EXECUTE_CELL_STREAM_EVENT_TYPE_COMPLETED {
		t.Fatalf("cell stream events = %#v", cellEvents)
	}
	if cellOutput != "cell stdout\n" {
		t.Fatalf("cell stream output = %q", cellOutput)
	}

	agent := agentcomposev1connect.NewAgentServiceClient(server.Client(), server.URL)
	agentStream, err := agent.SendAgentMessageStream(ctx, connect.NewRequest(&agentcomposev1.SendAgentMessageRequest{
		SessionId: sessionID,
		Agent:     "gemini-cli",
		Message:   "stream agent",
	}))
	if err != nil {
		t.Fatalf("SendAgentMessageStream returned error: %v", err)
	}
	agentEvents := make([]agentcomposev1.SendAgentMessageStreamEventType, 0)
	agentOutput := ""
	for agentStream.Receive() {
		msg := agentStream.Msg()
		agentEvents = append(agentEvents, msg.GetEventType())
		agentOutput += msg.GetChunk()
	}
	if err := agentStream.Err(); err != nil {
		t.Fatalf("agent stream returned error: %v", err)
	}
	if len(agentEvents) != 3 ||
		agentEvents[0] != agentcomposev1.SendAgentMessageStreamEventType_SEND_AGENT_MESSAGE_STREAM_EVENT_TYPE_STARTED ||
		agentEvents[1] != agentcomposev1.SendAgentMessageStreamEventType_SEND_AGENT_MESSAGE_STREAM_EVENT_TYPE_OUTPUT ||
		agentEvents[2] != agentcomposev1.SendAgentMessageStreamEventType_SEND_AGENT_MESSAGE_STREAM_EVENT_TYPE_COMPLETED {
		t.Fatalf("agent stream events = %#v", agentEvents)
	}
	if agentOutput != "loader agent transcript\n" {
		t.Fatalf("agent stream output = %q", agentOutput)
	}
}

func newTestServiceAPIHarness(t *testing.T) (*Service, *fakeLoaderAgentRuntime, *fakeSessionDriver) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Setenv("LLM_API_ENDPOINT", "")
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "service-api:latest",
		BoxliteHome:          filepath.Join(root, "boxlite"),
		GuestWorkspacePath:   "/data/workspace",
		JupyterGuestPort:     8888,
		SessionStartTimeout:  time.Second,
		SessionStopTimeout:   time.Second,
		AgentTimeout:         time.Second,
		JupyterProxyBasePath: "/agent-compose/session",
	}
	if err := os.MkdirAll(config.SessionRoot, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(session root) returned error: %v", err)
	}
	configDB := mustTestConfigStore(t, &appconfig.Config{
		DataRoot: root,
		DbAddr:   filepath.Join(root, "data.db"),
	})
	t.Cleanup(cancel)

	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readRequestBodyForTest(t, r)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(body, `"json_schema"`) {
			_, _ = w.Write([]byte(`{"id":"resp-service","model":"model-a","status":"completed","output_text":"{\"answer\":\"ok\"}"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp-service","model":"model-a","status":"completed","output_text":"llm text"}`))
	}))
	t.Cleanup(llmServer.Close)
	config.LLMAPIEndpoint = llmServer.URL
	config.LLMModel = "model-a"

	store := mustTestStore(t, config)
	runtime := &fakeLoaderAgentRuntime{}
	runtimes := fixedRuntimeProvider{runtime: runtime}
	driver := &fakeSessionDriver{}
	streams := &SessionStreamBroker{subscribers: map[string]map[int]chan sessionWatchEvent{}}
	executor := &Executor{config: config, store: store, configDB: configDB, runtimes: runtimes, streams: streams}
	bus := NewLoaderBusWithBuffer(256)
	aggregator := newDashboardOverviewAggregator(store, configDB)
	dashboard := newDashboardOverviewHub(ctx, aggregator, 10*time.Millisecond)
	manager := &LoaderManager{
		config:       config,
		rootCtx:      ctx,
		configDB:     configDB,
		engine:       &QJSLoaderEngine{},
		streams:      streams,
		dashboard:    dashboard,
		loaders:      map[string]Loader{},
		running:      map[string]int{},
		scheduleWake: make(chan struct{}, 1),
	}
	sessions := &SessionRPCBridge{config: config, store: store, configDB: configDB, driver: driver, bus: bus, streams: streams, dashboard: dashboard}
	service := &Service{
		config:    config,
		store:     store,
		configDB:  configDB,
		driver:    driver,
		runtimes:  runtimes,
		executor:  executor,
		loaders:   manager,
		llm:       &LLMClient{config: config, configDB: configDB, client: llmServer.Client()},
		bus:       bus,
		streams:   streams,
		dashboard: dashboard,
		sessions:  sessions,
	}
	return service, runtime, driver
}

func testServiceConfigAndLoaderAPIs(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_ENDPOINT", "")
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "service-api:latest",
		GuestWorkspacePath:   "/data/workspace",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
	}
	configDB := newTestConfigStore(t)
	manager := &LoaderManager{
		config:       config,
		rootCtx:      ctx,
		configDB:     configDB,
		engine:       &QJSLoaderEngine{},
		store:        mustTestStore(t, config),
		loaders:      map[string]Loader{},
		running:      map[string]int{},
		scheduleWake: make(chan struct{}, 1),
	}
	manager.llm = newTestLLMClient(t, configDB, "loader llm text")
	manager.bus = NewLoaderBusWithBuffer(4)
	service := &Service{
		config:   config,
		store:    manager.store,
		configDB: configDB,
		loaders:  manager,
	}

	envResp, err := service.UpdateGlobalEnvConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateGlobalEnvConfigRequest{
		EnvItems: []*agentcomposev1.SessionEnvVar{
			{Name: "OPENAI_API_KEY", Value: "sk-test", Secret: true},
			{Name: "PLAIN", Value: "value"},
		},
	}))
	if err != nil {
		t.Fatalf("UpdateGlobalEnvConfig returned error: %v", err)
	}
	if len(envResp.Msg.GetEnvItems()) != 2 {
		t.Fatalf("env item count = %d", len(envResp.Msg.GetEnvItems()))
	}
	loadedEnv, err := service.GetGlobalEnvConfig(ctx, connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatalf("GetGlobalEnvConfig returned error: %v", err)
	}
	if len(loadedEnv.Msg.GetEnvItems()) != 2 {
		t.Fatalf("loaded env item count = %d", len(loadedEnv.Msg.GetEnvItems()))
	}

	workspace, err := service.CreateWorkspaceConfig(ctx, connect.NewRequest(&agentcomposev1.CreateWorkspaceConfigRequest{
		Name:    "Files",
		Type:    "file",
		Comment: "service test",
	}))
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}
	workspaceID := workspace.Msg.GetWorkspace().GetId()
	if workspaceID == "" {
		t.Fatalf("expected workspace id")
	}
	workspaceList, err := service.ListWorkspaceConfigs(ctx, connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatalf("ListWorkspaceConfigs returned error: %v", err)
	}
	if len(workspaceList.Msg.GetWorkspaces()) != 1 {
		t.Fatalf("workspace count = %d", len(workspaceList.Msg.GetWorkspaces()))
	}
	updatedWorkspace, err := service.UpdateWorkspaceConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateWorkspaceConfigRequest{
		WorkspaceId: workspaceID,
		Name:        "Files Updated",
		Type:        "file",
		ConfigJson:  workspace.Msg.GetWorkspace().GetConfigJson(),
		Comment:     "updated",
	}))
	if err != nil {
		t.Fatalf("UpdateWorkspaceConfig returned error: %v", err)
	}
	if updatedWorkspace.Msg.GetWorkspace().GetName() != "Files Updated" {
		t.Fatalf("workspace name = %q", updatedWorkspace.Msg.GetWorkspace().GetName())
	}

	validateResp, err := service.ValidateLoader(ctx, connect.NewRequest(&agentcomposev1.ValidateLoaderRequest{
		Runtime: LoaderRuntimeScheduler,
		Script:  `scheduler.interval("tick", function(){ scheduler.log("tick"); }, 60000);`,
	}))
	if err != nil {
		t.Fatalf("ValidateLoader returned error: %v", err)
	}
	if len(validateResp.Msg.GetTriggers()) != 1 {
		t.Fatalf("validate trigger count = %d", len(validateResp.Msg.GetTriggers()))
	}
	if _, err := service.ValidateLoader(ctx, connect.NewRequest(&agentcomposev1.ValidateLoaderRequest{
		Runtime: "qjs",
		Script:  `scheduler.interval("tick", function(){ scheduler.log("tick"); }, 60000);`,
	})); err == nil {
		t.Fatalf("ValidateLoader accepted legacy qjs runtime")
	}
	if _, err := service.ValidateLoader(ctx, connect.NewRequest(&agentcomposev1.ValidateLoaderRequest{
		Runtime: "quickjs",
		Script:  `scheduler.interval("tick", function(){ scheduler.log("tick"); }, 60000);`,
	})); err == nil {
		t.Fatalf("ValidateLoader accepted legacy quickjs runtime")
	}

	created, err := service.CreateLoader(ctx, connect.NewRequest(&agentcomposev1.CreateLoaderRequest{
		Name:              "Service Loader",
		Description:       "created via service",
		Runtime:           LoaderRuntimeScheduler,
		Script:            `scheduler.interval("tick", function(){ scheduler.log("tick"); }, 60000);`,
		WorkspaceId:       workspaceID,
		Driver:            driverpkg.RuntimeDriverBoxlite,
		GuestImage:        "guest:latest",
		DefaultAgent:      "codex",
		SessionPolicy:     LoaderSessionPolicyNew,
		ConcurrencyPolicy: LoaderConcurrencyPolicyParallel,
		Enabled:           true,
		EnvItems: []*agentcomposev1.SessionEnvVar{
			{Name: "LOADER_SECRET", Value: "secret", Secret: true},
		},
	}))
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}
	loaderID := created.Msg.GetLoader().GetSummary().GetLoaderId()
	if loaderID == "" {
		t.Fatalf("expected loader id")
	}
	if created.Msg.GetLoader().GetEnvItems()[0].GetValue() != "********" {
		t.Fatalf("expected secret env item to be masked")
	}

	listed, err := service.ListLoaders(ctx, connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatalf("ListLoaders returned error: %v", err)
	}
	if len(listed.Msg.GetLoaders()) != 1 {
		t.Fatalf("loader count = %d", len(listed.Msg.GetLoaders()))
	}
	got, err := service.GetLoader(ctx, connect.NewRequest(&agentcomposev1.LoaderIDRequest{LoaderId: loaderID}))
	if err != nil {
		t.Fatalf("GetLoader returned error: %v", err)
	}
	if got.Msg.GetLoader().GetSummary().GetName() != "Service Loader" {
		t.Fatalf("loader name = %q", got.Msg.GetLoader().GetSummary().GetName())
	}

	updated, err := service.UpdateLoader(ctx, connect.NewRequest(&agentcomposev1.UpdateLoaderRequest{
		LoaderId:          loaderID,
		Name:              "Service Loader Updated",
		Description:       "updated via service",
		Runtime:           LoaderRuntimeScheduler,
		Script:            `scheduler.timeout("once", function(){ scheduler.log("once"); }, 5000);`,
		WorkspaceId:       workspaceID,
		Driver:            driverpkg.RuntimeDriverBoxlite,
		GuestImage:        "guest:v2",
		DefaultAgent:      "claude",
		SessionPolicy:     LoaderSessionPolicySticky,
		ConcurrencyPolicy: LoaderConcurrencyPolicySkip,
		Enabled:           true,
	}))
	if err != nil {
		t.Fatalf("UpdateLoader returned error: %v", err)
	}
	if updated.Msg.GetLoader().GetSummary().GetDefaultAgent() != "claude" {
		t.Fatalf("updated default agent = %q", updated.Msg.GetLoader().GetSummary().GetDefaultAgent())
	}
	triggerID := updated.Msg.GetLoader().GetTriggers()[0].GetTriggerId()

	disabled, err := service.SetLoaderEnabled(ctx, connect.NewRequest(&agentcomposev1.SetLoaderEnabledRequest{LoaderId: loaderID, Enabled: false}))
	if err != nil {
		t.Fatalf("SetLoaderEnabled(false) returned error: %v", err)
	}
	if disabled.Msg.GetLoader().GetSummary().GetEnabled() {
		t.Fatalf("expected loader to be disabled")
	}
	triggerDisabled, err := service.SetLoaderTriggerEnabled(ctx, connect.NewRequest(&agentcomposev1.SetLoaderTriggerEnabledRequest{
		LoaderId:  loaderID,
		TriggerId: triggerID,
		Enabled:   false,
	}))
	if err != nil {
		t.Fatalf("SetLoaderTriggerEnabled returned error: %v", err)
	}
	if triggerDisabled.Msg.GetLoader().GetTriggers()[0].GetEnabled() {
		t.Fatalf("expected trigger to be disabled")
	}

	now := time.Now().UTC()
	if err := configDB.CreateLoaderRun(ctx, LoaderRunSummary{
		LoaderID:      loaderID,
		ID:            "run-service",
		TriggerID:     triggerID,
		TriggerKind:   LoaderTriggerKindTimeout,
		TriggerSource: "manual",
		Status:        LoaderRunStatusSucceeded,
		StartedAt:     now,
		CompletedAt:   now.Add(10 * time.Millisecond),
		DurationMs:    10,
		ResultJSON:    `{"ok":true}`,
		PayloadJSON:   `{"value":1}`,
	}); err != nil {
		t.Fatalf("CreateLoaderRun returned error: %v", err)
	}
	if err := configDB.AddLoaderEvent(ctx, LoaderEvent{
		LoaderID:  loaderID,
		ID:        "event-service",
		RunID:     "run-service",
		TriggerID: triggerID,
		Type:      "loader.log",
		Level:     "info",
		Message:   "service event",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("AddLoaderEvent returned error: %v", err)
	}
	runs, err := service.ListLoaderRuns(ctx, connect.NewRequest(&agentcomposev1.ListLoaderRunsRequest{LoaderId: loaderID, Limit: 10}))
	if err != nil {
		t.Fatalf("ListLoaderRuns returned error: %v", err)
	}
	if len(runs.Msg.GetRuns()) != 1 {
		t.Fatalf("run count = %d", len(runs.Msg.GetRuns()))
	}
	run, err := service.GetLoaderRun(ctx, connect.NewRequest(&agentcomposev1.LoaderRunIDRequest{LoaderId: loaderID, RunId: "run-service"}))
	if err != nil {
		t.Fatalf("GetLoaderRun returned error: %v", err)
	}
	if run.Msg.GetRun().GetSummary().GetResultJson() != `{"ok":true}` {
		t.Fatalf("run result json = %q", run.Msg.GetRun().GetSummary().GetResultJson())
	}
	events, err := service.ListLoaderEvents(ctx, connect.NewRequest(&agentcomposev1.ListLoaderEventsRequest{LoaderId: loaderID, Limit: 10}))
	if err != nil {
		t.Fatalf("ListLoaderEvents returned error: %v", err)
	}
	if len(events.Msg.GetEvents()) != 1 {
		t.Fatalf("loader event count = %d", len(events.Msg.GetEvents()))
	}

	manager.engine = &recordingLoaderEngine{}
	runNow, err := service.RunLoaderNow(ctx, connect.NewRequest(&agentcomposev1.RunLoaderNowRequest{
		LoaderId:    loaderID,
		PayloadJson: `{"manual":true}`,
	}))
	if err != nil {
		t.Fatalf("RunLoaderNow returned error: %v", err)
	}
	if runNow.Msg.GetRun().GetSummary().GetStatus() != LoaderRunStatusSucceeded {
		t.Fatalf("RunLoaderNow status = %q error = %q", runNow.Msg.GetRun().GetSummary().GetStatus(), runNow.Msg.GetRun().GetSummary().GetError())
	}
	if runNow.Msg.GetRun().GetSummary().GetResultJson() != `{"ok":true}` {
		t.Fatalf("RunLoaderNow result = %q", runNow.Msg.GetRun().GetSummary().GetResultJson())
	}

	if _, err := service.DeleteLoader(ctx, connect.NewRequest(&agentcomposev1.LoaderIDRequest{LoaderId: loaderID})); err != nil {
		t.Fatalf("DeleteLoader returned error: %v", err)
	}
	if _, err := service.DeleteWorkspaceConfig(ctx, connect.NewRequest(&agentcomposev1.WorkspaceConfigIDRequest{WorkspaceId: workspaceID})); err != nil {
		t.Fatalf("DeleteWorkspaceConfig returned error: %v", err)
	}
}
