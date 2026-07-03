package agentcompose

import (
	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/agentcompose/workspaces"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/images"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"

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

	configDB := do.MustInvoke[*ConfigStore](di)
	t.Cleanup(func() { _ = configDB.db.Close() })
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
	ctx, cancel := context.WithCancel(context.Background())
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
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
	if err := os.MkdirAll(config.SessionRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll session root: %v", err)
	}
	configDB := newSupportTestConfigStore(t)
	store := &Store{config: config}
	bus := newTestLoaderBus(8)
	manager := &LoaderManager{
		config:       config,
		rootCtx:      ctx,
		store:        store,
		configDB:     configDB,
		driver:       &fakeSessionDriver{},
		executor:     &Executor{config: config, store: store, runtimes: fixedRuntimeProvider{runtime: &fakeLoaderAgentRuntime{}}},
		bus:          bus,
		engine:       &recordingLoaderEngine{},
		loaders:      map[string]Loader{},
		running:      map[string]int{},
		scheduleWake: make(chan struct{}, 1),
	}
	loader, err := configDB.CreateLoader(ctx, Loader{
		Summary: domain.LoaderSummary{ID: "start-loader", Name: "Start Loader", Runtime: domain.LoaderRuntimeScheduler, Enabled: true},
		Script:  "function main() { return {}; }",
		Triggers: []domain.LoaderTrigger{{
			ID:      "evt",
			Kind:    domain.LoaderTriggerKindEvent,
			Topic:   "runtime.test",
			Enabled: true,
		}},
	})
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}
	if _, err := configDB.ReplaceLoaderTriggers(ctx, loader.Summary.ID, loader.Triggers); err != nil {
		t.Fatalf("ReplaceLoaderTriggers returned error: %v", err)
	}
	manager.Start()

	dispatchBus := newTestLoaderBus(8)
	dispatcher := NewEventDispatcher(ctx, configDB, dispatchBus)
	dispatcher.SetInterval(time.Millisecond)
	dispatcher.Start()
	event, err := configDB.CreateEvent(ctx, domain.TopicEventRecord{
		Source:         domain.TopicEventSourceLoader,
		Topic:          "runtime.dispatch",
		PayloadJSON:    `{"ok":true}`,
		DispatchStatus: domain.TopicEventDispatchPending,
		CreatedAt:      time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateTopicEvent returned error: %v", err)
	}
	deadline := time.After(time.Second)
	for {
		select {
		case topicEvent := <-dispatchBus.Events():
			if topicEvent.EventID == event.ID {
				if err := topicEvent.Ack(ctx); err != nil {
					t.Fatalf("Ack returned error: %v", err)
				}
				cancel()
				return
			}
			if topicEvent.Release != nil {
				topicEvent.Release()
			}
		case <-deadline:
			cancel()
			t.Fatalf("timed out waiting for dispatched event")
		}
	}
}

func testSupportConstructorsAndHelpers(t *testing.T) {
	t.Helper()
	ctx := context.Background()
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
		LLMAPIEndpoint:       "http://127.0.0.1",
		LLMTimeout:           time.Second,
		SessionStartTimeout:  time.Second,
		SessionStopTimeout:   time.Second,
	}
	configDB := newSupportTestConfigStore(t)
	store := &Store{config: config}
	runtime := &fakeLoaderAgentRuntime{}
	driver := &fakeSessionDriver{}
	runtimes := fixedRuntimeProvider{runtime: runtime}
	executor := &Executor{config: config, store: store, runtimes: runtimes}
	capProvider := newTestCapabilityProvider("", "")
	bus := newTestLoaderBus(4)
	sessions := &SessionRPCBridge{config: config, store: store, configDB: configDB, driver: driver, runtimes: runtimes, bus: bus}
	manager := &LoaderManager{
		config:       config,
		rootCtx:      ctx,
		store:        store,
		configDB:     configDB,
		driver:       driver,
		executor:     executor,
		llm:          &LLMClient{config: config, configDB: configDB},
		cap:          capProvider,
		bus:          bus,
		engine:       &recordingLoaderEngine{},
		sessions:     sessions,
		loaders:      map[string]Loader{},
		running:      map[string]int{},
		scheduleWake: make(chan struct{}, 1),
	}

	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, slog.Default())
	do.ProvideValue(di, config)
	do.ProvideValue(di, store)
	do.ProvideValue(di, configDB)
	do.ProvideValue[Driver](di, driver)
	do.ProvideValue[RuntimeProvider](di, runtimes)
	do.ProvideValue(di, executor)
	do.ProvideValue(di, manager)
	do.ProvideValue(di, &LLMClient{config: config, configDB: configDB})
	do.ProvideValue[capabilityIntegration](di, capProvider)
	do.ProvideValue(di, bus)
	do.ProvideValue(di, newTestSessionStreamBroker())
	do.ProvideValue[loaders.LoaderEngine](di, &recordingLoaderEngine{})
	do.ProvideValue(di, sessions)

	if _, err := NewExecutor(di); err != nil {
		t.Fatalf("NewExecutor returned error: %v", err)
	}
	if _, err := NewLLMClient(di); err != nil {
		t.Fatalf("NewLLMClient returned error: %v", err)
	}
	if createdBus, err := loaders.NewBus(di); err != nil || createdBus.Events() == nil {
		t.Fatalf("NewLoaderBus = %#v/%v", createdBus, err)
	}
	if engine, err := loaders.NewLoaderEngine(di); err != nil || engine == nil {
		t.Fatalf("NewLoaderEngine = %#v/%v", engine, err)
	}
	if createdManager, err := NewLoaderManager(di); err != nil || createdManager.rootCtx == nil || createdManager.scheduleWake == nil {
		t.Fatalf("NewLoaderManager = %#v/%v", createdManager, err)
	}
	if bridge, err := NewSessionRPCBridge(di); err != nil || bridge.store != store {
		t.Fatalf("NewSessionRPCBridge = %#v/%v", bridge, err)
	}
	service, err := NewService(di)
	if err != nil || service.startedAt.IsZero() || service.store != store {
		t.Fatalf("NewService = %#v/%v", service, err)
	}
	ociBackend, ok := service.ociImages.(*images.OCIBackend)
	if !ok || !ociBackend.HasCache() || ociBackend.CacheRoot() != config.ImageCacheRoot {
		t.Fatalf("NewService OCI backend = %#v ok=%v", service.ociImages, ok)
	}
	autoBackend, ok := service.autoImages.(*images.AutoBackend)
	if !ok || !autoBackend.HasDockerBackend() || !autoBackend.HasOCIBackend() || autoBackend.Mode() != config.ImageStoreMode {
		t.Fatalf("NewService auto backend = %#v ok=%v", service.autoImages, ok)
	}

	testSupportAgentAndLoaderHelpers(t)
	testSupportLegacyAgentRunMerge(t, store)
	testSupportWorkspaceMoveMerge(t)
	testSupportSessionRPCAndAgentResumeHelpers(t, manager)
}

func testSupportAgentAndLoaderHelpers(t *testing.T) {
	t.Helper()
	longStderr := strings.Repeat("x", 300)
	if got := execution.SummarizeAgentExecFailure(ExecResult{Stderr: "  line one\nline two  "}); got != "line one line two" {
		t.Fatalf("summarizeAgentExecFailure whitespace = %q", got)
	}
	if got := execution.SummarizeAgentExecFailure(ExecResult{Stderr: longStderr}); len(got) != 243 || !strings.HasSuffix(got, "...") {
		t.Fatalf("summarizeAgentExecFailure long = %q len=%d", got, len(got))
	}
	if got := execution.SummarizeAgentExecFailure(ExecResult{}); got != "" {
		t.Fatalf("summarizeAgentExecFailure empty = %q", got)
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

	if api.CellTypeFromProto(agentcomposev1.CellType_CELL_TYPE_SHELL) != execution.CellTypeShell ||
		api.CellTypeFromProto(agentcomposev1.CellType_CELL_TYPE_PYTHON) != execution.CellTypePython ||
		api.CellTypeFromProto(agentcomposev1.CellType_CELL_TYPE_AGENT) != execution.CellTypeAgent ||
		api.CellTypeFromProto(agentcomposev1.CellType_CELL_TYPE_UNSPECIFIED) != execution.CellTypeJavaScript {
		t.Fatalf("fromProtoCellType returned unexpected values")
	}
	if api.CellTypeToProto(execution.CellTypeShell) != agentcomposev1.CellType_CELL_TYPE_SHELL ||
		api.CellTypeToProto(execution.CellTypePython) != agentcomposev1.CellType_CELL_TYPE_PYTHON ||
		api.CellTypeToProto(execution.CellTypeAgent) != agentcomposev1.CellType_CELL_TYPE_AGENT ||
		api.CellTypeToProto("unknown") != agentcomposev1.CellType_CELL_TYPE_JAVASCRIPT {
		t.Fatalf("toProtoCellType returned unexpected values")
	}

	responseJSON := `{"session":{"summary":{"sessionId":"resp-session"}}}`
	if got := loaderSessionRPCLinkedSessionID("CreateSession", `{"sessionId":"req-session"}`, responseJSON); got != "resp-session" {
		t.Fatalf("linked response session id = %q", got)
	}
	if got := loaderSessionRPCLinkedSessionID("GetSession", `{"sessionId":"req-session"}`, `{}`); got != "req-session" {
		t.Fatalf("linked request session id = %q", got)
	}
	if got := loaderSessionRPCLinkedSessionID("ListSessions", `{"sessionId":"req-session"}`, `{}`); got != "" {
		t.Fatalf("linked list session id = %q", got)
	}
	if loaderSessionIDFromJSON(`{bad`) != "" || loaderSessionIDFromJSON(`{"session":{"summary":{}}}`) != "" {
		t.Fatalf("loaderSessionIDFromJSON returned value for invalid payload")
	}
	if execution.FirstNonZeroInt(0, 0, 7, 9) != 7 || execution.FirstNonZeroInt(0, 0) != 0 {
		t.Fatalf("firstNonZeroInt returned unexpected values")
	}
	if domain.SessionTypeFromTriggerSource("script:loader-1") != domain.SessionTypeScript || domain.SessionTypeFromTriggerSource("") != domain.SessionTypeManual {
		t.Fatalf("sessionTypeFromTriggerSource returned unexpected values")
	}
	if parsed, err := parseOptionalRFC3339("2026-06-02T09:00:00Z", "created_from"); err != nil || !parsed.Equal(time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("parseOptionalRFC3339 parsed = %s err=%v", parsed, err)
	}
	if _, err := parseOptionalRFC3339("bad", "created_from"); err == nil {
		t.Fatalf("parseOptionalRFC3339 invalid returned nil error")
	}
}

func testSupportLegacyAgentRunMerge(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	session, err := store.CreateSession(ctx, "Legacy Runs", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	older := time.Now().UTC().Add(-2 * time.Minute)
	newer := time.Now().UTC().Add(-time.Minute)
	if err := store.saveCells(session.Summary.ID, []NotebookCell{{ID: "cell-1", Type: execution.CellTypeShell, Source: "echo", CreatedAt: newer}}); err != nil {
		t.Fatalf("saveCells returned error: %v", err)
	}
	legacyRuns := []AgentRun{
		{ID: "run-1", Agent: "codex", Message: "legacy", Output: "done", Success: true, CreatedAt: older, AgentSessionID: "agent-1"},
		{ID: "cell-1", Agent: "codex", Message: "duplicate", CreatedAt: newer},
	}
	data, err := json.Marshal(legacyRuns)
	if err != nil {
		t.Fatalf("marshal legacy runs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.sessionDir(session.Summary.ID), "state", "agent_runs.json"), data, 0o644); err != nil {
		t.Fatalf("write legacy runs: %v", err)
	}
	cells, err := store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListCells returned error: %v", err)
	}
	if len(cells) != 2 || cells[0].ID != "run-1" || cells[1].ID != "cell-1" {
		t.Fatalf("merged cells = %#v", cells)
	}
	if cells[0].Type != execution.CellTypeAgent || cells[0].AgentSessionID != "agent-1" {
		t.Fatalf("merged legacy agent cell = %#v", cells[0])
	}
}

func testSupportWorkspaceMoveMerge(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "from-src.txt"), []byte("src\n"), 0o644); err != nil {
		t.Fatalf("write src file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "nested", "from-dst.txt"), []byte("dst\n"), 0o644); err != nil {
		t.Fatalf("write dst file: %v", err)
	}
	if err := workspaces.MoveWorkspaceEntry(src, dst); err != nil {
		t.Fatalf("moveWorkspaceEntry returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(dst, "nested", "from-src.txt"), "src\n")
	assertFileContent(t, filepath.Join(dst, "nested", "from-dst.txt"), "dst\n")
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("expected source dir removed, stat err=%v", err)
	}

	srcFile := filepath.Join(root, "src-dir")
	dstFile := filepath.Join(root, "dst-file")
	if err := os.MkdirAll(srcFile, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.WriteFile(dstFile, []byte("dst\n"), 0o644); err != nil {
		t.Fatalf("write destination file: %v", err)
	}
	if err := workspaces.MoveWorkspaceEntry(srcFile, dstFile); err == nil {
		t.Fatalf("moveWorkspaceEntry file collision returned nil error")
	}
}

func testSupportSessionRPCAndAgentResumeHelpers(t *testing.T, manager *LoaderManager) {
	t.Helper()
	ctx := context.Background()
	loader, err := manager.configDB.CreateLoader(ctx, Loader{
		Summary: domain.LoaderSummary{ID: "loader-rpc", Name: "Loader RPC", Runtime: domain.LoaderRuntimeScheduler, Enabled: true},
		Script:  "function main() { return {}; }",
	})
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}
	run := domain.LoaderRunSummary{ID: "run-rpc", LoaderID: loader.Summary.ID, Status: domain.LoaderRunStatusRunning, StartedAt: time.Now().UTC()}
	if err := manager.configDB.CreateLoaderRun(ctx, run); err != nil {
		t.Fatalf("CreateLoaderRun returned error: %v", err)
	}
	host := &loaderRunHost{manager: manager, loader: loader, run: &run}
	response, err := host.CallSessionRPC(ctx, "ListSessions", `{}`)
	if err != nil {
		t.Fatalf("CallSessionRPC ListSessions returned error: %v", err)
	}
	if !strings.Contains(response, "sessions") {
		t.Fatalf("CallSessionRPC response = %q", response)
	}
	if _, err := host.CallSessionRPC(ctx, "NoSuchMethod", `{}`); err == nil {
		t.Fatalf("CallSessionRPC invalid method returned nil error")
	}
	events, err := manager.configDB.ListLoaderEvents(ctx, loader.Summary.ID, 20)
	if err != nil {
		t.Fatalf("ListLoaderEvents returned error: %v", err)
	}
	var completed, failed bool
	for _, event := range events {
		completed = completed || event.Type == "loader.session.rpc.completed"
		failed = failed || event.Type == "loader.session.rpc.failed"
	}
	if !completed || !failed {
		t.Fatalf("loader rpc events = %#v", events)
	}

	root := t.TempDir()
	session := &Session{Summary: SessionSummary{ID: "session-agent", WorkspacePath: filepath.Join(root, "workspace")}}
	codexState := filepath.Join(execution.HostSessionDir(session), "state", "agents", "providers")
	if err := os.MkdirAll(codexState, 0o755); err != nil {
		t.Fatalf("mkdir codex state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexState, "codex.json"), []byte(`{"sessionId":"codex-session"}`), 0o644); err != nil {
		t.Fatalf("write codex state: %v", err)
	}
	sessionDir := filepath.Join(execution.HostSessionHome(session), ".codex", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir codex sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "codex-session.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write matching jsonl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "other.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write other jsonl: %v", err)
	}
	info := execution.CollectAgentResumeInfo(session, "codex", "", "manifest.json")
	if info == nil || info.SessionID != "codex-session" || len(info.SessionJSONLPaths) != 1 {
		t.Fatalf("agent resume info = %#v", info)
	}
	if execution.ShouldIncludeAgentJSONL("notes.txt", "codex", "codex-session") || !execution.ShouldIncludeAgentJSONL(filepath.Join(sessionDir, "codex-session.jsonl"), "codex", "codex-session") {
		t.Fatalf("shouldIncludeAgentJSONL returned unexpected values")
	}
}

func newSupportTestConfigStore(t *testing.T) *ConfigStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	store := &ConfigStore{db: db}
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = store.db.Close() })
	if err := store.initSchema(context.Background()); err != nil {
		t.Fatalf("initSchema: %v", err)
	}
	return store
}
