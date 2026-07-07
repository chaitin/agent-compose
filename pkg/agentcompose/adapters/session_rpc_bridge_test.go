package adapters

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/capability"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/volumes"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

type fakeRPCSessionDriver struct {
	startCalls []string
	stopCalls  []string
}

func (d *fakeRPCSessionDriver) StartSessionVM(_ context.Context, session *domain.Session) error {
	d.startCalls = append(d.startCalls, session.Summary.ID)
	return nil
}

func (d *fakeRPCSessionDriver) StopSessionVM(_ context.Context, session *domain.Session) error {
	d.stopCalls = append(d.stopCalls, session.Summary.ID)
	return nil
}

type testCapabilityProvider struct {
	target string
	guide  func(context.Context, string) ([]byte, error)
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

func TestSessionRPCBridgeCallJSONSupportsSessionRPCs(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSessionRPCBridge(t)

	createJSON, err := bridge.CallJSON(ctx, "CreateSession", `{"title":"Loader Created","tags":[{"name":"origin","value":"test"}]}`)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	var created agentcomposev1.SessionResponse
	if err := protojson.Unmarshal([]byte(createJSON), &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	sessionID := created.GetSession().GetSummary().GetSessionId()
	if sessionID == "" {
		t.Fatalf("expected CreateSession to return a session id")
	}
	if got, want := created.GetSession().GetSummary().GetVmStatus(), domain.VMStatusRunning; got != want {
		t.Fatalf("CreateSession vm status = %q, want %q", got, want)
	}
	if len(driver.startCalls) != 1 {
		t.Fatalf("StartSessionVM call count = %d, want 1", len(driver.startCalls))
	}

	getJSON, err := bridge.CallJSON(ctx, "GetSession", `{"sessionId":"`+sessionID+`"}`)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	var gotSession agentcomposev1.SessionResponse
	if err := protojson.Unmarshal([]byte(getJSON), &gotSession); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
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
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(listed.GetSessions()) != 1 || listed.GetSessions()[0].GetSessionId() != sessionID {
		t.Fatalf("listed sessions = %#v, want one session %s", listed.GetSessions(), sessionID)
	}

	if _, err := bridge.CallJSON(ctx, "GetSessionProxy", `{"sessionId":"`+sessionID+`"}`); err == nil || !strings.Contains(err.Error(), "jupyter is not enabled") {
		t.Fatalf("GetSessionProxy error = %v, want jupyter disabled error", err)
	}

	stopJSON, err := bridge.CallJSON(ctx, "StopSession", `{"sessionId":"`+sessionID+`"}`)
	if err != nil {
		t.Fatalf("StopSession returned error: %v", err)
	}
	var stopped agentcomposev1.SessionResponse
	if err := protojson.Unmarshal([]byte(stopJSON), &stopped); err != nil {
		t.Fatalf("unmarshal stop response: %v", err)
	}
	if got, want := stopped.GetSession().GetSummary().GetVmStatus(), domain.VMStatusStopped; got != want {
		t.Fatalf("StopSession vm status = %q, want %q", got, want)
	}
	if len(driver.stopCalls) != 1 {
		t.Fatalf("StopSessionVM call count = %d, want 1", len(driver.stopCalls))
	}

	resumeJSON, err := bridge.CallJSON(ctx, "ResumeSession", `{"sessionId":"`+sessionID+`"}`)
	if err != nil {
		t.Fatalf("ResumeSession returned error: %v", err)
	}
	var resumed agentcomposev1.SessionResponse
	if err := protojson.Unmarshal([]byte(resumeJSON), &resumed); err != nil {
		t.Fatalf("unmarshal resume response: %v", err)
	}
	if got, want := resumed.GetSession().GetSummary().GetVmStatus(), domain.VMStatusRunning; got != want {
		t.Fatalf("ResumeSession vm status = %q, want %q", got, want)
	}
	if len(driver.startCalls) != 2 {
		t.Fatalf("StartSessionVM call count after resume = %d, want 2", len(driver.startCalls))
	}

	if _, err := bridge.CallJSON(ctx, "MissingRPC", `{}`); err == nil || !strings.Contains(err.Error(), "unsupported session rpc") {
		t.Fatalf("unsupported rpc error = %v", err)
	}
	if _, err := bridge.CallJSON(ctx, "GetSession", `{bad json`); err == nil || !strings.Contains(err.Error(), "decode session rpc request") {
		t.Fatalf("bad json error = %v", err)
	}
}

func TestSessionRPCBridgeCapabilityGuideLifecycle(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSessionRPCBridge(t)
	catalog := "# Catalog: dev\n\ninitial"
	bridge.cap = testCapabilityProvider{
		target: "agent-compose:9100",
		guide: func(context.Context, string) ([]byte, error) {
			return []byte(catalog), nil
		},
	}

	resp, err := bridge.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{
		Title:     "capability",
		CapsetIds: []string{"dev"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, item := range resp.Msg.GetSession().GetEnvItems() {
		env[item.GetName()] = item.GetValue()
	}
	if env[capabilities.ProxyTargetEnvName] != "agent-compose:9100" || env[capabilities.SessionTokenEnvName] == "" {
		t.Fatalf("capability gateway vars not injected: %+v", env)
	}
	sessionID := resp.Msg.GetSession().GetSummary().GetSessionId()
	session, err := bridge.store.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if capsets := capabilities.SessionCapsets(session); len(capsets) != 1 || capsets[0] != "dev" {
		t.Fatalf("capset tag not injected: %+v", capsets)
	}
	if _, err := os.Stat(filepath.Join(session.Summary.WorkspacePath, "CAPABILITIES.md")); !os.IsNotExist(err) {
		t.Fatalf("capability guide must not be written into workspace, stat err = %v", err)
	}
	guidePath := capabilities.SessionGuidePath(session)
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
	if _, err := bridge.StopSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID})); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.ResumeSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID})); err != nil {
		t.Fatal(err)
	}
	guide, err = os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("capability guide not refreshed: %v", err)
	}
	if !strings.Contains(string(guide), "refreshed") {
		t.Fatalf("capability guide content was not refreshed: %s", guide)
	}
}

func TestSessionRPCBridgeCapabilityGuideIsBestEffort(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSessionRPCBridge(t)
	bridge.cap = testCapabilityProvider{
		target: "agent-compose:9100",
		guide: func(context.Context, string) ([]byte, error) {
			return nil, errors.New("octobus unavailable")
		},
	}

	resp, err := bridge.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{
		Title:     "best-effort",
		CapsetIds: []string{"dev"},
	}))
	if err != nil {
		t.Fatalf("create session must not fail when guide rendering fails: %v", err)
	}
	session, err := bridge.store.GetSession(ctx, resp.Msg.GetSession().GetSummary().GetSessionId())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(capabilities.SessionGuidePath(session)); !os.IsNotExist(err) {
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

func TestSessionRPCBridgeCreateSessionResolvesVolumeMounts(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSessionRPCBridge(t)
	bindSource := t.TempDir()

	resp, err := bridge.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{
		Title:  "volumes",
		Driver: driverpkg.RuntimeDriverDocker,
		Volumes: []*agentcomposev1.VolumeMountSpec{{
			Type:     domain.VolumeMountTypeBind,
			Source:   bindSource,
			Target:   "/mnt/input",
			ReadOnly: true,
		}},
	}))
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	protoMounts := resp.Msg.GetSession().GetVolumeMounts()
	if len(protoMounts) != 1 {
		t.Fatalf("response volume mounts = %#v, want one", protoMounts)
	}
	if protoMounts[0].GetType() != domain.VolumeMountTypeBind || protoMounts[0].GetHostPath() != bindSource || !protoMounts[0].GetReadOnly() {
		t.Fatalf("response volume mount = %#v", protoMounts[0])
	}

	sessionID := resp.Msg.GetSession().GetSummary().GetSessionId()
	session, err := bridge.store.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if len(session.VolumeMounts) != 1 || session.VolumeMounts[0].Target != "/mnt/input" || session.VolumeMounts[0].HostPath != bindSource {
		t.Fatalf("persisted volume mounts = %#v", session.VolumeMounts)
	}
}

func TestSessionRPCBridgeCreateSessionRecordsVolumeWarnings(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSessionRPCBridge(t)

	resp, err := bridge.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{
		Title:  "volume-warning",
		Driver: driverpkg.RuntimeDriverDocker,
		Volumes: []*agentcomposev1.VolumeMountSpec{{
			Type:   domain.VolumeMountTypeBind,
			Source: t.TempDir(),
			Target: "/workspace",
		}},
	}))
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	sessionID := resp.Msg.GetSession().GetSummary().GetSessionId()
	events, err := bridge.store.ListEvents(ctx, sessionID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type == "session.volume.warning" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected volume warning event, got %#v", events)
	}
}

func TestSessionRuntimeLivenessAndNotifierBranches(t *testing.T) {
	ctx := context.Background()
	session := &domain.Session{Summary: domain.SessionSummary{ID: "session-1"}}
	if alive, checked, err := (sessionRuntimeLiveness{}).IsSessionAlive(ctx, "boxlite", session, domain.VMState{}); err != nil || alive || checked {
		t.Fatalf("nil runtime liveness = alive %v checked %v err %v", alive, checked, err)
	}
	if alive, checked, err := (sessionRuntimeLiveness{runtimes: fakeRuntimeProvider{runtime: fakeSessionRuntime{}}}).IsSessionAlive(ctx, "boxlite", session, domain.VMState{}); err != nil || alive || checked {
		t.Fatalf("runtime without liveness = alive %v checked %v err %v", alive, checked, err)
	}
	runtime := driverRuntimeAdapter{runtime: fakeDriverRuntime{alive: true}}
	if alive, checked, err := (sessionRuntimeLiveness{runtimes: fakeRuntimeProvider{runtime: runtime}}).IsSessionAlive(ctx, "microsandbox", session, domain.VMState{}); err != nil || !alive || !checked {
		t.Fatalf("driver runtime adapter liveness = alive %v checked %v err %v", alive, checked, err)
	}

	streams := sessions.NewStreamBrokerForTest()
	events, unsubscribe := streams.Subscribe("session-1")
	defer unsubscribe()
	notifier := sessionLifecycleNotifier{streams: streams}
	notifier.PublishSessionUpdated(&session.Summary)
	got := <-events
	if got.EventType != sessions.WatchEventTypeSessionUpdated || got.Session.ID != "session-1" {
		t.Fatalf("session update event = %#v", got)
	}
	notifier.PublishEventAdded("session-1", domain.SessionEvent{ID: "event-1", Type: "test.event"})
	got = <-events
	if got.EventType != sessions.WatchEventTypeEventAdded || got.Event.ID != "event-1" {
		t.Fatalf("event added = %#v", got)
	}
	notifier.NotifyDashboard("test")
}

func newTestSessionRPCBridge(t *testing.T) (*SessionRPCBridge, *fakeRPCSessionDriver) {
	t.Helper()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		DbAddr:               filepath.Join(root, "data.db"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "agent-compose-test:latest",
		BoxliteHome:          filepath.Join(root, "boxlite"),
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/state",
		JupyterGuestPort:     8888,
		SessionStartTimeout:  time.Second,
		SessionStopTimeout:   time.Second,
		JupyterProxyBasePath: "/agent-compose/session",
	}
	if err := os.MkdirAll(config.SessionRoot, 0o755); err != nil {
		t.Fatalf("create session root: %v", err)
	}
	db, err := sql.Open("sqlite", config.DbAddr)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	configDB := configstore.FromDB(db)
	if err := configDB.InitSchema(context.Background()); err != nil {
		t.Fatalf("InitSchema returned error: %v", err)
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	driver := &fakeRPCSessionDriver{}
	return NewSessionRPCBridge(
		config,
		store,
		configDB,
		driver,
		nil,
		nil,
		sessions.NewStreamBrokerForTest(),
		testCapabilityProvider{},
		volumes.NewManager(configDB, volumes.NewLocalDriver(config)),
		nil,
	), driver
}

func TestSessionRPCBridgeCapabilityGuideFromHTTPProvider(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSessionRPCBridge(t)
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

	resp, err := bridge.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{
		Title:     "http-capability",
		CapsetIds: []string{"dev"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	session, err := bridge.store.GetSession(ctx, resp.Msg.GetSession().GetSummary().GetSessionId())
	if err != nil {
		t.Fatal(err)
	}
	guide, err := os.ReadFile(capabilities.SessionGuidePath(session))
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
