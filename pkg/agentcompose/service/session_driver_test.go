package agentcompose

import (
	"agent-compose/pkg/agentcompose/domain"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionDriverStartSessionVMSavesRuntimeProxyState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		BoxliteHome:          filepath.Join(root, "boxlite"),
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
		SessionStartTimeout:  2 * time.Second,
	}
	if err := os.MkdirAll(config.SessionRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(session root) returned error: %v", err)
	}
	store := &Store{config: config}

	session, err := store.CreateSession(ctx, "Proxy Session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	updatedProxyState := ProxyState{
		ProxyPath:  session.Summary.ProxyPath,
		GuestHost:  "agent-compose-session-1",
		HostPort:   39000,
		GuestPort:  8888,
		JupyterURL: "http://127.0.0.1:39000/lab?token=secret",
		Token:      "secret",
	}
	proxyState, err := store.GetProxyState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState returned error: %v", err)
	}
	vmState, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState returned error: %v", err)
	}
	driver := &SessionDriver{config: config, store: store}

	if err := driver.saveSessionStartInfo(session, vmState, proxyState, domain.SessionVMInfo{
		BoxID:      "container-1",
		JupyterURL: updatedProxyState.JupyterURL,
		ProxyState: &updatedProxyState,
	}); err != nil {
		t.Fatalf("saveSessionStartInfo returned error: %v", err)
	}
	savedProxyState, err := store.GetProxyState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState returned error: %v", err)
	}
	if savedProxyState.GuestHost != "agent-compose-session-1" || savedProxyState.GuestPort != 8888 {
		t.Fatalf("saved proxy target = %s:%d, want agent-compose-session-1:8888", savedProxyState.GuestHost, savedProxyState.GuestPort)
	}
	if savedProxyState.JupyterURL != updatedProxyState.JupyterURL {
		t.Fatalf("saved JupyterURL = %q, want %q", savedProxyState.JupyterURL, updatedProxyState.JupyterURL)
	}
	vmState, err = store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState returned error: %v", err)
	}
	if vmState.BoxID != "container-1" || vmState.BootstrapRef != updatedProxyState.JupyterURL {
		t.Fatalf("vm state = %+v, want box id and bootstrap ref from runtime", vmState)
	}
}
