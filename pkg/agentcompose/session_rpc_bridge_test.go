package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

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

func TestSessionRPCBridgeWrapperCallJSONSmoke(t *testing.T) {
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
