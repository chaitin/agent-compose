package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/storage"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	sessionspkg "agent-compose/pkg/sessions"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

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

func TestSessionRPCBridgeCallJSONSmoke(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSessionRPCBridge(t)

	createJSON, err := bridge.CallJSON(ctx, "CreateSession", `{"title":"Loader Created"}`)
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
	if len(driver.startCalls) != 1 {
		t.Fatalf("StartSessionVM call count = %d, want %d", len(driver.startCalls), 1)
	}
}

func newTestSessionRPCBridge(t *testing.T) (*sessionspkg.SessionRPCBridge, *fakeSessionDriver) {
	bridge, driver, _ := newTestSessionRPCBridgeWithStore(t)
	return bridge, driver
}

func newTestSessionRPCBridgeWithStore(t *testing.T) (*sessionspkg.SessionRPCBridge, *fakeSessionDriver, *storage.Store) {
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
	streams, _ := sessionspkg.NewSessionStreamBroker(nil)
	store := mustTestStore(t, config)
	return sessionspkg.NewSessionRPCBridgeFromDeps(config, store, configDB, driver, nil, nil, streams, newTestCapabilityProvider("", ""), nil), driver, store
}
