package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/capability"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/internal/testutil"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/workspaces"
)

type fakeRPCSandboxDriver struct {
	startCalls    []string
	startSessions []*domain.Sandbox
	onStart       func(*domain.Sandbox)
	stopCalls     []string
	validateErr   error
	startErr      error
	stopErr       error
}

func (d *fakeRPCSandboxDriver) ValidateSandboxRuntime(*domain.Sandbox) error {
	return d.validateErr
}

func (d *fakeRPCSandboxDriver) StartSandboxVM(_ context.Context, session *domain.Sandbox) error {
	d.startCalls = append(d.startCalls, session.Summary.ID)
	d.startSessions = append(d.startSessions, session)
	if d.onStart != nil {
		d.onStart(session)
	}
	return d.startErr
}

func (d *fakeRPCSandboxDriver) StopSandboxVM(_ context.Context, session *domain.Sandbox) error {
	d.stopCalls = append(d.stopCalls, session.Summary.ID)
	return d.stopErr
}

type testCapabilityProvider struct {
	target string
	guide  func(context.Context, string) ([]byte, error)
}

type scopedTestCapabilityProvider struct {
	testCapabilityProvider
	guideForScope func(context.Context, capabilities.GuideScope, string) ([]byte, error)
}

func (p scopedTestCapabilityProvider) CapabilityGuideForScope(ctx context.Context, scope capabilities.GuideScope, declaration string) ([]byte, error) {
	return p.guideForScope(ctx, scope, declaration)
}

func (p testCapabilityProvider) Status(context.Context) capability.Status {
	return capability.Status{Configured: true, OK: true, Status: "ok"}
}

func (p testCapabilityProvider) ListCapsets(context.Context) ([]capability.Capset, error) {
	return nil, nil
}

func (p testCapabilityProvider) Catalog(context.Context, string) (capability.Catalog, error) {
	return capability.Catalog{}, nil
}

func (p testCapabilityProvider) CapabilityGuide(ctx context.Context, capsetID string) ([]byte, error) {
	if p.guide == nil {
		return nil, capability.ErrNotConfigured
	}
	return p.guide(ctx, capsetID)
}

func (p testCapabilityProvider) ProxyTarget() string {
	return p.target
}

func TestSandboxRPCBridgeCallJSONSupportsSessionRPCs(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)

	createJSON, err := bridge.CallJSON(ctx, "CreateSession", `{"title":"Loader Created","tags":[{"name":"origin","value":"test"}]}`)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	var created sandboxRPCResponse
	if err := json.Unmarshal([]byte(createJSON), &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	sessionID := created.Session.Summary.SessionID
	if sessionID == "" {
		t.Fatalf("expected CreateSession to return a session id")
	}
	if got, want := created.Session.Summary.VMStatus, domain.VMStatusRunning; got != want {
		t.Fatalf("CreateSession vm status = %q, want %q", got, want)
	}
	if len(driver.startCalls) != 1 {
		t.Fatalf("StartSandboxVM call count = %d, want 1", len(driver.startCalls))
	}

	getJSON, err := bridge.CallJSON(ctx, "GetSession", `{"sessionId":"`+sessionID+`"}`)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	var gotSession sandboxRPCResponse
	if err := json.Unmarshal([]byte(getJSON), &gotSession); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if gotSession.Session.Summary.SessionID != sessionID {
		t.Fatalf("GetSession session id = %q, want %q", gotSession.Session.Summary.SessionID, sessionID)
	}

	listJSON, err := bridge.CallJSON(ctx, "ListSessions", ``)
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	var listed sandboxRPCListResponse
	if err := json.Unmarshal([]byte(listJSON), &listed); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].SessionID != sessionID {
		t.Fatalf("listed sessions = %#v, want one session %s", listed.Sessions, sessionID)
	}

	if _, err := bridge.CallJSON(ctx, "GetSessionProxy", `{"sessionId":"`+sessionID+`"}`); err == nil || !strings.Contains(err.Error(), "jupyter is not enabled") {
		t.Fatalf("GetSessionProxy error = %v, want jupyter disabled error", err)
	}

	stopJSON, err := bridge.CallJSON(ctx, "StopSession", `{"sessionId":"`+sessionID+`"}`)
	if err != nil {
		t.Fatalf("StopSession returned error: %v", err)
	}
	var stopped sandboxRPCResponse
	if err := json.Unmarshal([]byte(stopJSON), &stopped); err != nil {
		t.Fatalf("unmarshal stop response: %v", err)
	}
	if got, want := stopped.Session.Summary.VMStatus, domain.VMStatusStopped; got != want {
		t.Fatalf("StopSession vm status = %q, want %q", got, want)
	}
	if len(driver.stopCalls) != 1 {
		t.Fatalf("StopSandboxVM call count = %d, want 1", len(driver.stopCalls))
	}

	resumeJSON, err := bridge.CallJSON(ctx, "ResumeSession", `{"sessionId":"`+sessionID+`"}`)
	if err != nil {
		t.Fatalf("ResumeSession returned error: %v", err)
	}
	var resumed sandboxRPCResponse
	if err := json.Unmarshal([]byte(resumeJSON), &resumed); err != nil {
		t.Fatalf("unmarshal resume response: %v", err)
	}
	if got, want := resumed.Session.Summary.VMStatus, domain.VMStatusRunning; got != want {
		t.Fatalf("ResumeSession vm status = %q, want %q", got, want)
	}
	if len(driver.startCalls) != 2 {
		t.Fatalf("StartSandboxVM call count after resume = %d, want 2", len(driver.startCalls))
	}

	if _, err := bridge.CallJSON(ctx, "MissingRPC", `{}`); err == nil || !strings.Contains(err.Error(), "unsupported session rpc") {
		t.Fatalf("unsupported rpc error = %v", err)
	}
	if _, err := bridge.CallJSON(ctx, "GetSession", `{bad json`); err == nil || !strings.Contains(err.Error(), "decode session rpc request") {
		t.Fatalf("bad json error = %v", err)
	}
}

func TestSandboxRPCBridgeCreateSandboxInheritsSchedulerAgentEnvironment(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	bridge.config.RuntimeBaseURL = "http://agent-compose.test:7410"
	definition, err := bridge.configDB.CreateAgentDefinition(ctx, domain.AgentDefinition{
		ID:           "agent-scheduler",
		Name:         "scheduler-agent",
		Enabled:      true,
		Provider:     "codex",
		Model:        "gpt-scheduler",
		SystemPrompt: "Scheduler inherited prompt",
		EnvItems:     []domain.SandboxEnvVar{{Name: "AGENT_ENV", Value: "from-agent"}},
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	ctx = loaders.WithSandboxCreationContext(ctx, loaders.SandboxCreationContext{
		AgentDefinitionID: definition.ID,
		Provider:          "claude",
	})
	responseJSON, err := bridge.CallJSONWithSource(ctx, "CreateSession", `{"title":"scheduler child","envItems":[{"name":"REQUEST_ENV","value":"from-script"}]}`, domain.SandboxTypeScript+":loader-1")
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	var response sandboxRPCResponse
	if err := json.Unmarshal([]byte(responseJSON), &response); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	loaded, err := bridge.store.GetSandbox(ctx, response.Session.Summary.SessionID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	env := domain.SandboxEnvMap(loaded.EnvItems)
	if env["AGENT_ENV"] != "from-agent" || env["REQUEST_ENV"] != "from-script" {
		t.Fatalf("sandbox env = %#v", env)
	}
	if execution.SessionTagValue(loaded.Summary.Tags, domain.AgentSandboxTagID) != definition.ID {
		t.Fatalf("sandbox tags = %#v", loaded.Summary.Tags)
	}
	if execution.SessionTagValue(loaded.Summary.Tags, domain.AgentSandboxTagProvider) != definition.Provider {
		t.Fatalf("sandbox provider tag = %#v", loaded.Summary.Tags)
	}
	if data, err := os.ReadFile(execution.HostAgentSystemPromptPath(loaded)); err != nil || string(data) != definition.SystemPrompt {
		t.Fatalf("system prompt = %q err=%v", string(data), err)
	}
	if len(driver.startSessions) != 1 {
		t.Fatalf("driver start sessions = %d", len(driver.startSessions))
	}
}

func TestSandboxRPCBridgeCreateIgnoresRequestedAgentIdentityTags(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSandboxRPCBridge(t)

	loaded, err := bridge.createSandbox(ctx, sandboxRPCCreateRequest{
		Title: "untrusted agent tags",
		Tags: []domain.SandboxTag{
			{Name: domain.AgentSandboxTagID, Value: "forged-agent"},
			{Name: domain.AgentSandboxTagName, Value: "forged-name"},
			{Name: domain.AgentSandboxTagProvider, Value: "claude"},
			{Name: "custom", Value: "preserved"},
		},
	}, domain.SandboxTypeManual)
	if err != nil {
		t.Fatalf("createSandbox returned error: %v", err)
	}
	if execution.SessionTagValue(loaded.Summary.Tags, domain.AgentSandboxTagID) != "" || execution.SessionTagValue(loaded.Summary.Tags, domain.AgentSandboxTagName) != "" {
		t.Fatalf("sandbox retained requested agent identity: %#v", loaded.Summary.Tags)
	}
	if execution.SessionTagValue(loaded.Summary.Tags, domain.AgentSandboxTagProvider) != domain.DefaultAgentProvider || execution.SessionTagValue(loaded.Summary.Tags, "custom") != "preserved" {
		t.Fatalf("sandbox tags = %#v", loaded.Summary.Tags)
	}
}

func TestSandboxRPCBridgeCapabilityGuideLifecycle(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSandboxRPCBridge(t)
	catalog := "# Catalog: dev\n\ninitial"
	bridge.cap = testCapabilityProvider{
		target: "agent-compose:9100",
		guide: func(context.Context, string) ([]byte, error) {
			return []byte(catalog), nil
		},
	}
	bridge.capTokens = NewCapabilitySandboxResolver(bridge.store)

	sandbox, err := bridge.createSandbox(ctx, sandboxRPCCreateRequest{Title: "capability", CapsetIDs: []string{"dev"}}, domain.SandboxTypeManual)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, item := range sandbox.EnvItems {
		env[item.Name] = item.Value
	}
	if env[capabilities.ProxyTargetEnvName] != "agent-compose:9100" || env[capabilities.SandboxTokenEnvName] == "" {
		t.Fatalf("capability gateway vars not injected: %+v", env)
	}
	sessionID := sandbox.Summary.ID
	session, err := bridge.store.GetSandbox(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	capabilityToken := capabilities.SandboxToken(session)
	binding, err := bridge.capTokens.ResolveCapabilitySandbox(ctx, capabilityToken)
	if err != nil || binding.SandboxID != sessionID || len(binding.CapsetIDs) != 1 || binding.CapsetIDs[0] != "dev" {
		t.Fatalf("capability token binding = %#v err=%v", binding, err)
	}
	if capsets := capabilities.SandboxCapsets(session); len(capsets) != 1 || capsets[0] != "dev" {
		t.Fatalf("capset tag not injected: %+v", capsets)
	}
	if _, err := os.Stat(filepath.Join(session.Summary.WorkspacePath, "CAPABILITIES.md")); !os.IsNotExist(err) {
		t.Fatalf("capability guide must not be written into workspace, stat err = %v", err)
	}
	guidePath := capabilities.SandboxGuidePath(session)
	guide, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("capability guide not written: %v", err)
	}
	if !strings.Contains(string(guide), capabilities.ProxyTargetEnvName) || !strings.Contains(string(guide), "initial") {
		t.Fatalf("capability guide content = %s", guide)
	}

	if err := os.Remove(guidePath); err != nil {
		t.Fatalf("remove capability guide: %v", err)
	}
	catalog = "# Catalog: dev\n\nrefreshed"
	if _, err := bridge.StopSandbox(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.capTokens.ResolveCapabilitySandbox(ctx, capabilityToken); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("stopped sandbox token resolve error = %v", err)
	}
	if _, err := bridge.ResumeSandbox(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	if binding, err := bridge.capTokens.ResolveCapabilitySandbox(ctx, capabilityToken); err != nil || binding.SandboxID != sessionID {
		t.Fatalf("resumed sandbox token binding = %#v err=%v", binding, err)
	}
	guide, err = os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("capability guide not refreshed: %v", err)
	}
	if !strings.Contains(string(guide), "refreshed") {
		t.Fatalf("capability guide content was not refreshed: %s", guide)
	}
}

func TestSandboxRPCBridgeHistoricalUnsupportedRuntimeIsUnimplementedAndPreservesSummary(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	unsupported := domain.ClassifyError(domain.ErrUnsupported, "", driverpkg.ErrRuntimeDriverNotCompiled)
	session, err := bridge.store.CreateSandbox(ctx, "historical", "", driverpkg.RuntimeDriverMicrosandbox, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusStopped
	session.Summary.RuntimeRef = "original-runtime-ref"
	if err := bridge.store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	driver.validateErr = unsupported

	_, err = bridge.ResumeSandbox(ctx, session.Summary.ID)
	if connect.CodeOf(err) != connect.CodeUnimplemented || !errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) {
		t.Fatalf("ResumeSandbox error = %v, code=%v; want unimplemented typed error", err, connect.CodeOf(err))
	}
	loaded, err := bridge.store.GetSandbox(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox after resume returned error: %v", err)
	}
	if loaded.Summary.VMStatus != domain.VMStatusStopped || loaded.Summary.Driver != driverpkg.RuntimeDriverMicrosandbox || loaded.Summary.RuntimeRef != "original-runtime-ref" {
		t.Fatalf("unsupported resume changed summary: %#v", loaded.Summary)
	}

	driver.validateErr = nil
	driver.stopErr = unsupported
	loaded.Summary.VMStatus = domain.VMStatusRunning
	if err := bridge.store.UpdateSandbox(ctx, loaded); err != nil {
		t.Fatalf("UpdateSandbox running returned error: %v", err)
	}
	_, err = bridge.StopSandbox(ctx, session.Summary.ID)
	if connect.CodeOf(err) != connect.CodeUnimplemented || !errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) {
		t.Fatalf("StopSandbox error = %v, code=%v; want unimplemented typed error", err, connect.CodeOf(err))
	}
	loaded, err = bridge.store.GetSandbox(ctx, session.Summary.ID)
	if err != nil || loaded.Summary.VMStatus != domain.VMStatusRunning || loaded.Summary.RuntimeRef != "original-runtime-ref" {
		t.Fatalf("unsupported stop changed summary: %#v, %v", loaded, err)
	}
}

func TestSandboxRPCBridgeCapabilityGuideIsBestEffort(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSandboxRPCBridge(t)
	bridge.cap = testCapabilityProvider{
		target: "agent-compose:9100",
		guide: func(context.Context, string) ([]byte, error) {
			return nil, errors.New("octobus unavailable")
		},
	}

	sandbox, err := bridge.createSandbox(ctx, sandboxRPCCreateRequest{Title: "best-effort", CapsetIDs: []string{"dev"}}, domain.SandboxTypeManual)
	if err != nil {
		t.Fatalf("create session must not fail when guide rendering fails: %v", err)
	}
	session, err := bridge.store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(capabilities.SandboxGuidePath(session)); !os.IsNotExist(err) {
		t.Fatalf("expected no capability guide when provider fails, stat err = %v", err)
	}
	events, err := bridge.store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type == "capability.guide.warning" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected capability guide warning event, got %#v", events)
	}
}

func TestSandboxRPCBridgeCapabilityGuideRoutesManagedScopeAndMergesBestEffort(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSandboxRPCBridge(t)
	var gotScope capabilities.GuideScope
	bridge.cap = scopedTestCapabilityProvider{
		testCapabilityProvider: testCapabilityProvider{
			target: "agent-compose:9100",
			guide: func(_ context.Context, capsetID string) ([]byte, error) {
				if capsetID != "legacy" {
					t.Fatalf("global capset = %q, want legacy", capsetID)
				}
				return []byte("global guide"), nil
			},
		},
		guideForScope: func(_ context.Context, scope capabilities.GuideScope, declaration string) ([]byte, error) {
			gotScope = scope
			if declaration == "broken/fail" {
				return nil, errors.New("unavailable")
			}
			return []byte("project guide for " + declaration), nil
		},
	}

	root := t.TempDir()
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            "sandbox-scoped-guide",
		WorkspacePath: filepath.Join(root, "workspace"),
		Tags: []domain.SandboxTag{
			{Name: "project", Value: "project-1"},
			{Name: domain.AgentSandboxTagID, Value: "agent-1"},
		},
	}}
	writeCapabilityGuide(ctx, bridge.cap, nil, nil, sandbox, []string{"legacy", "internal/dev", "broken/fail"})

	guide, err := os.ReadFile(capabilities.SandboxGuidePath(sandbox))
	if err != nil {
		t.Fatalf("read merged guide: %v", err)
	}
	if gotScope != (capabilities.GuideScope{ManagedProjectID: "project-1", ManagedAgentID: "agent-1"}) {
		t.Fatalf("guide scope = %#v", gotScope)
	}
	if content := string(guide); !strings.Contains(content, "global guide") || !strings.Contains(content, "project guide for internal/dev") {
		t.Fatalf("merged guide = %s", content)
	}
}

func TestSandboxRuntimeLivenessAndNotifierBranches(t *testing.T) {
	ctx := context.Background()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-1"}}
	if alive, checked, err := (sandboxRuntimeLiveness{}).IsSandboxAlive(ctx, "boxlite", session, domain.VMState{}); err != nil || alive || checked {
		t.Fatalf("nil runtime liveness = alive %v checked %v err %v", alive, checked, err)
	}
	if alive, checked, err := (sandboxRuntimeLiveness{runtimes: fakeRuntimeProvider{runtime: fakeSessionRuntime{}}}).IsSandboxAlive(ctx, "boxlite", session, domain.VMState{}); err != nil || alive || checked {
		t.Fatalf("runtime without liveness = alive %v checked %v err %v", alive, checked, err)
	}
	runtime := driverRuntimeAdapter{runtime: fakeDriverRuntime{alive: true}}
	if alive, checked, err := (sandboxRuntimeLiveness{runtimes: fakeRuntimeProvider{runtime: runtime}}).IsSandboxAlive(ctx, "microsandbox", session, domain.VMState{}); err != nil || !alive || !checked {
		t.Fatalf("driver runtime adapter liveness = alive %v checked %v err %v", alive, checked, err)
	}

	streams := sessions.NewStreamBrokerForTest()
	events, unsubscribe := streams.Subscribe("session-1")
	defer unsubscribe()
	notifier := sandboxLifecycleNotifier{streams: streams}
	notifier.PublishSandboxUpdated(&session.Summary)
	got := <-events
	if got.EventType != sessions.WatchEventTypeSandboxUpdated || got.Sandbox.ID != "session-1" {
		t.Fatalf("session update event = %#v", got)
	}
	notifier.PublishEventAdded("session-1", domain.SandboxEvent{ID: "event-1", Type: "test.event"})
	got = <-events
	if got.EventType != sessions.WatchEventTypeEventAdded || got.Event.ID != "event-1" {
		t.Fatalf("event added = %#v", got)
	}
	notifier.NotifyDashboard("test")
}

func newTestSandboxRPCBridge(t *testing.T) (*SandboxRPCBridge, *fakeRPCSandboxDriver) {
	t.Helper()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		DbAddr:               filepath.Join(root, "data.db"),
		RuntimeDriver:        driverpkg.RuntimeDriverDocker,
		DefaultImage:         "agent-compose-test:latest",
		BoxliteHome:          filepath.Join(root, "boxlite"),
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/state",
		JupyterGuestPort:     8888,
		SandboxStartTimeout:  time.Second,
		SandboxStopTimeout:   time.Second,
		JupyterProxyBasePath: "/agent-compose/session",
	}
	if err := os.MkdirAll(config.SandboxRoot, 0o755); err != nil {
		t.Fatalf("create sandbox root: %v", err)
	}
	configDB, store, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("open test storage: %v", err)
	}
	driver := &fakeRPCSandboxDriver{}
	streams := sessions.NewStreamBrokerForTest()
	agentExecutor := NewAgentExecutor(config, store, streams, NewAgentRunner(config, store, configDB, configDB, nil))
	return NewSandboxRPCBridge(
		config,
		store,
		configDB,
		workspaces.NewProvisioner(config, configDB, store),
		driver,
		nil,
		nil,
		streams,
		testCapabilityProvider{},
		nil,
		nil,
		agentExecutor,
	), driver
}

func TestSandboxRPCBridgeCapabilityGuideFromHTTPProvider(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSandboxRPCBridge(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/v1/catalog/dev" && r.URL.Query().Get("format") == "md" {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte("# Catalog: dev\n\nx-octobus-instance=inst"))
			return
		}
		http.Error(w, "unexpected", http.StatusBadRequest)
	}))
	defer server.Close()
	bridge.cap = capabilities.NewDynamicProvider(staticGatewaySource{addr: server.URL}, "agent-compose:9100")

	sandbox, err := bridge.createSandbox(ctx, sandboxRPCCreateRequest{Title: "http-capability", CapsetIDs: []string{"dev"}}, domain.SandboxTypeManual)
	if err != nil {
		t.Fatal(err)
	}
	session, err := bridge.store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	guide, err := os.ReadFile(capabilities.SandboxGuidePath(session))
	if err != nil {
		t.Fatalf("capability guide not written: %v", err)
	}
	if !strings.Contains(string(guide), "x-octobus-instance=inst") {
		t.Fatalf("capability guide missing routing info: %s", guide)
	}
}

type staticGatewaySource struct {
	addr string
}

func (s staticGatewaySource) GetCapabilityGateway(context.Context) (domain.CapabilityGatewaySettings, error) {
	return domain.CapabilityGatewaySettings{Addr: s.addr}, nil
}
