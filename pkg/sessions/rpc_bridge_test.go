package sessions

import (
	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/storage"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func mustTestStore(t testing.TB, config *appconfig.Config) *Store {
	t.Helper()
	store, err := storage.NewStoreFromConfig(config)
	if err != nil {
		t.Fatalf("NewStoreFromConfig returned error: %v", err)
	}
	return store
}

func mustTestConfigStore(t testing.TB, config *appconfig.Config) *ConfigStore {
	t.Helper()
	store, err := storage.NewConfigStoreFromConfig(config)
	if err != nil {
		t.Fatalf("NewConfigStoreFromConfig returned error: %v", err)
	}
	return store
}

type fixedGatewaySource struct {
	settings storage.CapabilityGatewaySettings
}

func (f fixedGatewaySource) GetCapabilityGateway(context.Context) (storage.CapabilityGatewaySettings, error) {
	return f.settings, nil
}

func newTestCapabilityProvider(addr, proxyTarget string) CapabilityProvider {
	return capabilities.NewProvider(fixedGatewaySource{settings: storage.CapabilityGatewaySettings{Addr: addr}}, proxyTarget)
}

type fakeSessionDriver struct {
	startCalls []string
	stopCalls  []string
	startHook  func(context.Context, *Session) error
	stopHook   func(context.Context, *Session) error
}

func (d *fakeSessionDriver) StartSessionVM(ctx context.Context, session *Session) error {
	d.startCalls = append(d.startCalls, session.Summary.ID)
	if d.startHook != nil {
		return d.startHook(ctx, session)
	}
	return nil
}

func (d *fakeSessionDriver) StopSessionVM(ctx context.Context, session *Session) error {
	d.stopCalls = append(d.stopCalls, session.Summary.ID)
	if d.stopHook != nil {
		return d.stopHook(ctx, session)
	}
	return nil
}

func TestSessionRPCBridgeCallJSONSupportsAllSessionRPCs(t *testing.T) {
	testSessionRPCBridgeCallJSONSupportsAllSessionRPCs(t)
}

func testSessionRPCBridgeCallJSONSupportsAllSessionRPCs(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSessionRPCBridge(t)

	createJSON, err := bridge.CallJSON(ctx, "CreateSession", `{"title":"Loader Created","tags":[{"name":"origin","value":"test"}]}`)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	var created agentcomposev1.SessionResponse
	if err := protojson.Unmarshal([]byte(createJSON), &created); err != nil {
		t.Fatalf("protojson.Unmarshal(create) returned error: %v", err)
	}
	if created.GetSession().GetSummary().GetSessionId() == "" {
		t.Fatalf("expected CreateSession to return a session id")
	}
	if got, want := created.GetSession().GetSummary().GetVmStatus(), VMStatusRunning; got != want {
		t.Fatalf("CreateSession vm status = %q, want %q", got, want)
	}
	if len(driver.startCalls) != 1 {
		t.Fatalf("StartSessionVM call count = %d, want %d", len(driver.startCalls), 1)
	}
	sessionID := created.GetSession().GetSummary().GetSessionId()

	getJSON, err := bridge.CallJSON(ctx, "GetSession", `{"sessionId":"`+sessionID+`"}`)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	var gotSession agentcomposev1.SessionResponse
	if err := protojson.Unmarshal([]byte(getJSON), &gotSession); err != nil {
		t.Fatalf("protojson.Unmarshal(get) returned error: %v", err)
	}
	if gotSession.GetSession().GetSummary().GetSessionId() != sessionID {
		t.Fatalf("GetSession session id = %q, want %q", gotSession.GetSession().GetSummary().GetSessionId(), sessionID)
	}

	listJSON, err := bridge.CallJSON(ctx, "ListSessions", ``)
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	var listed agentcomposev1.ListSessionsResponse
	if err := protojson.Unmarshal([]byte(listJSON), &listed); err != nil {
		t.Fatalf("protojson.Unmarshal(list) returned error: %v", err)
	}
	if len(listed.GetSessions()) != 1 {
		t.Fatalf("ListSessions count = %d, want %d", len(listed.GetSessions()), 1)
	}
	if listed.GetSessions()[0].GetSessionId() != sessionID {
		t.Fatalf("ListSessions first session id = %q, want %q", listed.GetSessions()[0].GetSessionId(), sessionID)
	}

	proxyJSON, err := bridge.CallJSON(ctx, "GetSessionProxy", `{"sessionId":"`+sessionID+`"}`)
	if err != nil {
		t.Fatalf("GetSessionProxy returned error: %v", err)
	}
	var proxy agentcomposev1.SessionProxyResponse
	if err := protojson.Unmarshal([]byte(proxyJSON), &proxy); err != nil {
		t.Fatalf("protojson.Unmarshal(proxy) returned error: %v", err)
	}
	if proxy.GetSessionId() != sessionID {
		t.Fatalf("GetSessionProxy session id = %q, want %q", proxy.GetSessionId(), sessionID)
	}
	if proxy.GetNotebookUrl() == "" || proxy.GetProxyPath() == "" {
		t.Fatalf("expected GetSessionProxy to return proxy urls")
	}

	stopJSON, err := bridge.CallJSON(ctx, "StopSession", `{"sessionId":"`+sessionID+`"}`)
	if err != nil {
		t.Fatalf("StopSession returned error: %v", err)
	}
	var stopped agentcomposev1.SessionResponse
	if err := protojson.Unmarshal([]byte(stopJSON), &stopped); err != nil {
		t.Fatalf("protojson.Unmarshal(stop) returned error: %v", err)
	}
	if got, want := stopped.GetSession().GetSummary().GetVmStatus(), VMStatusStopped; got != want {
		t.Fatalf("StopSession vm status = %q, want %q", got, want)
	}
	if len(driver.stopCalls) != 1 {
		t.Fatalf("StopSessionVM call count = %d, want %d", len(driver.stopCalls), 1)
	}

	resumeJSON, err := bridge.CallJSON(ctx, "ResumeSession", `{"sessionId":"`+sessionID+`"}`)
	if err != nil {
		t.Fatalf("ResumeSession returned error: %v", err)
	}
	var resumed agentcomposev1.SessionResponse
	if err := protojson.Unmarshal([]byte(resumeJSON), &resumed); err != nil {
		t.Fatalf("protojson.Unmarshal(resume) returned error: %v", err)
	}
	if got, want := resumed.GetSession().GetSummary().GetVmStatus(), VMStatusRunning; got != want {
		t.Fatalf("ResumeSession vm status = %q, want %q", got, want)
	}
	if len(driver.startCalls) != 3 {
		t.Fatalf("StartSessionVM call count after resume = %d, want %d", len(driver.startCalls), 3)
	}
}

func TestSessionRPCBridgeCreateSessionInjectsCapabilityGatewayVars(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSessionRPCBridge(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/v1/catalog/dev" && r.URL.Query().Get("format") == "md" {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte("# Catalog: dev\n\n## gRPC\n\n| Method | Metadata |\n| --- | --- |\n| `/pkg.Service/Call` | `x-octobus-capset=dev, x-octobus-instance=inst` |\n"))
			return
		}
		t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		http.Error(w, "unexpected", http.StatusBadRequest)
	}))
	defer server.Close()
	bridge.cap = newTestCapabilityProvider(server.URL, "agent-compose:9100")

	resp, err := bridge.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{Title: "capability", CapsetIds: []string{"dev"}}))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, item := range resp.Msg.GetSession().GetEnvItems() {
		env[item.GetName()] = item.GetValue()
	}
	if env[capabilities.CapProxyTargetEnvName] != "agent-compose:9100" || env[capabilities.CapabilitySessionTokenEnvName] == "" {
		t.Fatalf("capability gateway vars not injected: %+v", env)
	}
	// The capability guide is rendered from OctoBus and written as the session
	// MPI catalog (runtime/mpi/catalog.md), which agent-compose-runtime-js folds into the
	// agent system prompt — not into the user's workspace.
	session, err := bridge.store.GetSession(ctx, resp.Msg.GetSession().GetSummary().GetSessionId())
	if err != nil {
		t.Fatal(err)
	}
	// The allowed capset set is recorded as session tags, not env.
	if capsets := sessionCapabilityCapsets(session); len(capsets) != 1 || capsets[0] != "dev" {
		t.Fatalf("capset tag not injected: %+v", capsets)
	}
	if _, err := os.Stat(filepath.Join(session.Summary.WorkspacePath, "CAPABILITIES.md")); !os.IsNotExist(err) {
		t.Fatalf("capability guide must not be written into the workspace, stat err = %v", err)
	}
	guide, err := os.ReadFile(capabilities.SessionGuidePath(session))
	if err != nil {
		t.Fatalf("capability guide not written to MPI catalog: %v", err)
	}
	if !strings.Contains(string(guide), "x-octobus-instance=inst") {
		t.Fatalf("capability guide missing routing info: %s", guide)
	}
}

func TestSessionRPCBridgeResumeSessionRefreshesCapabilityGuide(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSessionRPCBridge(t)
	catalog := "# Catalog: dev\n\ninitial"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/v1/catalog/dev" && r.URL.Query().Get("format") == "md" {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(catalog))
			return
		}
		t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		http.Error(w, "unexpected", http.StatusBadRequest)
	}))
	defer server.Close()
	bridge.cap = newTestCapabilityProvider(server.URL, "agent-compose:9100")

	resp, err := bridge.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{Title: "capability", CapsetIds: []string{"dev"}}))
	if err != nil {
		t.Fatal(err)
	}
	sessionID := resp.Msg.GetSession().GetSummary().GetSessionId()
	session, err := bridge.store.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	guidePath := capabilities.SessionGuidePath(session)
	if err := os.Remove(guidePath); err != nil {
		t.Fatalf("remove capability guide returned error: %v", err)
	}
	catalog = "# Catalog: dev\n\nrefreshed"
	if _, err := bridge.StopSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID})); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.ResumeSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID})); err != nil {
		t.Fatal(err)
	}
	guide, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("capability guide not refreshed on resume: %v", err)
	}
	if !strings.Contains(string(guide), "refreshed") {
		t.Fatalf("capability guide content was not refreshed: %s", guide)
	}
}

func TestSessionRPCBridgeCapabilityInjectionIsBestEffort(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSessionRPCBridge(t)
	// Unreachable OctoBus: guide rendering must fail without blocking creation.
	bridge.cap = newTestCapabilityProvider("http://127.0.0.1:1", "agent-compose:9100")

	resp, err := bridge.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{Title: "best-effort", CapsetIds: []string{"dev"}}))
	if err != nil {
		t.Fatalf("create session must not fail when OctoBus is unreachable: %v", err)
	}
	env := map[string]string{}
	for _, item := range resp.Msg.GetSession().GetEnvItems() {
		env[item.GetName()] = item.GetValue()
	}
	if env[capabilities.CapProxyTargetEnvName] != "agent-compose:9100" || env[capabilities.CapabilitySessionTokenEnvName] == "" {
		t.Fatalf("capability env still injected despite OctoBus down: %+v", env)
	}
	session, err := bridge.store.GetSession(ctx, resp.Msg.GetSession().GetSummary().GetSessionId())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(capabilities.SessionGuidePath(session)); !os.IsNotExist(err) {
		t.Fatalf("expected no capability guide when OctoBus unreachable, stat err = %v", err)
	}
	events, err := bridge.store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if !sessionEventsContain(events, "capability.guide.warning") {
		t.Fatalf("expected capability guide warning event, got %#v", events)
	}
}

func sessionEventsContain(events []SessionEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func newTestSessionRPCBridge(t *testing.T) (*SessionRPCBridge, *fakeSessionDriver) {
	t.Helper()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "agent-compose-test:latest",
		BoxliteHome:          filepath.Join(root, "boxlite"),
		JupyterGuestPort:     8888,
		SessionStartTimeout:  time.Second,
		SessionStopTimeout:   time.Second,
		JupyterProxyBasePath: "/agent-compose/session",
	}
	if err := os.MkdirAll(config.SessionRoot, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(session root) returned error: %v", err)
	}
	configDB := mustTestConfigStore(t, &appconfig.Config{
		DataRoot: root,
		DbAddr:   filepath.Join(root, "data.db"),
	})
	driver := &fakeSessionDriver{}
	return &SessionRPCBridge{
		config:   config,
		store:    mustTestStore(t, config),
		configDB: configDB,
		driver:   driver,
		cap:      newTestCapabilityProvider("", ""),
	}, driver
}
