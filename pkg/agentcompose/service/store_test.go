package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samber/do/v2"
)

func TestStoreAgentRunLegacyVMAndListWorkflows(t *testing.T) {
	testStoreAgentRunLegacyVMAndListWorkflows(t)
}

func TestStorePersistenceErrorAndUpdateBranches(t *testing.T) {
	testStorePersistenceErrorAndUpdateBranches(t)
}

func TestStoreCreateSessionUsesConfiguredJupyterProxyBase(t *testing.T) {
	testStoreCreateSessionUsesConfiguredJupyterProxyBase(t)
}

func testStoreCreateSessionUsesConfiguredJupyterProxyBase(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	config := &appconfig.Config{
		SessionRoot:          filepath.Join(t.TempDir(), "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "default-box:latest",
		GuestHomePath:        "/home/agent-compose",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/jupyter",
	}
	di := do.New()
	do.ProvideValue(di, config)
	store, err := NewStore(di)
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}

	session, err := store.CreateSession(ctx, "Prefixed Proxy", "", driverpkg.RuntimeDriverBoxlite, "", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	wantProxyPath := "/agent-compose/jupyter/" + session.Summary.ID + "/lab"
	if session.Summary.ProxyPath != wantProxyPath {
		t.Fatalf("session proxy path = %q, want %q", session.Summary.ProxyPath, wantProxyPath)
	}
	proxyState, err := store.GetProxyState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState returned error: %v", err)
	}
	if proxyState.ProxyPath != wantProxyPath {
		t.Fatalf("proxy state path = %q, want %q", proxyState.ProxyPath, wantProxyPath)
	}
}

func testStorePersistenceErrorAndUpdateBranches(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	config := &appconfig.Config{
		SessionRoot:          filepath.Join(t.TempDir(), "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "default-box:latest",
		ImageRegistry:        "registry.test",
		GuestHomePath:        "/home/agent-compose",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
	}
	di := do.New()
	do.ProvideValue(di, config)
	store, err := NewStore(di)
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	session, err := store.CreateSession(ctx, "Persistence Branches", "", driverpkg.RuntimeDriverBoxlite, "", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	firstCell := NotebookCell{ID: "cell-1", Type: execution.CellTypeShell, Source: "echo first", Stdout: "first\n", Output: "first\n", Success: true, CreatedAt: time.Now().UTC()}
	if err := store.AddCell(ctx, session, firstCell); err != nil {
		t.Fatalf("AddCell first returned error: %v", err)
	}
	firstCell.Stdout = "updated\n"
	firstCell.Output = "updated\n"
	if err := store.AddCell(ctx, session, firstCell); err != nil {
		t.Fatalf("AddCell update returned error: %v", err)
	}
	cells, err := store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListCells returned error: %v", err)
	}
	if len(cells) != 1 || cells[0].Stdout != "updated\n" {
		t.Fatalf("updated cells = %#v", cells)
	}

	runningCell := NotebookCell{ID: "cell-running", Type: execution.CellTypeShell, Source: "sleep 1", Running: true, CreatedAt: time.Now().UTC()}
	if err := store.AddCell(ctx, session, runningCell); err != nil {
		t.Fatalf("AddCell running returned error: %v", err)
	}
	cellDir := filepath.Join(store.sessionDir(session.Summary.ID), "state", "cells", runningCell.ID)
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatalf("MkdirAll running cell dir returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cellDir, "stdout.txt"), []byte("live stdout\n"), 0o644); err != nil {
		t.Fatalf("write stdout artifact returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cellDir, "stderr.txt"), []byte("live stderr\n"), 0o644); err != nil {
		t.Fatalf("write stderr artifact returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cellDir, "output.txt"), []byte("live stdout\nlive stderr\n"), 0o644); err != nil {
		t.Fatalf("write output artifact returned error: %v", err)
	}
	cells, err = store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListCells running returned error: %v", err)
	}
	if len(cells) != 2 || cells[1].Stdout != "live stdout\n" || cells[1].Stderr != "live stderr\n" || cells[1].Output != "live stdout\nlive stderr\n" {
		t.Fatalf("running cells = %#v", cells)
	}

	if err := store.AddEvent(ctx, session.Summary.ID, SessionEvent{ID: "event-1", Type: "session.tested", Level: "info", Message: "tested", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("AddEvent returned error: %v", err)
	}
	loaded, err := store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if loaded.Summary.CellCount != 2 || loaded.Summary.EventCount != 1 {
		t.Fatalf("loaded counts = cells %d events %d", loaded.Summary.CellCount, loaded.Summary.EventCount)
	}

	if err := os.WriteFile(filepath.Join(store.sessionDir(session.Summary.ID), "state", "cells.json"), []byte(`{bad json`), 0o644); err != nil {
		t.Fatalf("write corrupt cells: %v", err)
	}
	if _, err := store.ListCells(ctx, session.Summary.ID); err == nil {
		t.Fatalf("ListCells corrupt returned nil error")
	}
	if err := store.saveCells(session.Summary.ID, nil); err != nil {
		t.Fatalf("saveCells reset returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.sessionDir(session.Summary.ID), "state", "events.json"), []byte(`{bad json`), 0o644); err != nil {
		t.Fatalf("write corrupt events: %v", err)
	}
	if _, err := store.ListEvents(ctx, session.Summary.ID); err == nil {
		t.Fatalf("ListEvents corrupt returned nil error")
	}
	if err := store.saveEvents(session.Summary.ID, nil); err != nil {
		t.Fatalf("saveEvents reset returned error: %v", err)
	}
	if err := os.WriteFile(store.proxyStatePath(session.Summary.ID), []byte(`{bad json`), 0o644); err != nil {
		t.Fatalf("write corrupt proxy state: %v", err)
	}
	if _, err := store.GetProxyState(session.Summary.ID); err == nil {
		t.Fatalf("GetProxyState corrupt returned nil error")
	}
	if err := os.WriteFile(store.vmStatePath(session.Summary.ID), []byte(`{bad json`), 0o644); err != nil {
		t.Fatalf("write corrupt vm state: %v", err)
	}
	if _, err := store.GetVMState(session.Summary.ID); err == nil {
		t.Fatalf("GetVMState corrupt returned nil error")
	}
	if err := os.WriteFile(filepath.Join(store.sessionDir(session.Summary.ID), "metadata.json"), []byte(`{"summary":{"driver":"bad"}}`), 0o644); err != nil {
		t.Fatalf("write invalid metadata: %v", err)
	}
	if _, err := store.GetSession(ctx, session.Summary.ID); err == nil {
		t.Fatalf("GetSession invalid metadata returned nil error")
	}
}

func testStoreAgentRunLegacyVMAndListWorkflows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	config := &appconfig.Config{
		SessionRoot:          filepath.Join(t.TempDir(), "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "default-box:latest",
		ImageRegistry:        "registry.test",
		GuestHomePath:        "/home/agent-compose",
		MicrosandboxHome:     filepath.Join(t.TempDir(), "microsandbox"),
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
	}
	di := do.New()
	do.ProvideValue(di, config)
	store, err := NewStore(di)
	if err != nil {
		t.Fatalf("NewStore returned error: %v", err)
	}
	if _, err := os.Stat(config.SessionRoot); err != nil {
		t.Fatalf("session root was not created: %v", err)
	}

	session, err := store.CreateSession(ctx, "", "", driverpkg.RuntimeDriverBoxlite, "", "ws-1", "script:timer", &SessionWorkspace{
		ID:         "ws-1",
		Name:       "Workspace",
		Type:       "file",
		ConfigJSON: "{}",
	}, []SessionEnvVar{{Name: "PLAIN", Value: "value"}}, []SessionTag{{Name: "kind", Value: "loader"}})
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if session.Summary.Title == "" || session.Summary.TriggerSource != "script:timer" || session.Summary.GuestImage != "default-box:latest" {
		t.Fatalf("unexpected session summary: %#v", session.Summary)
	}
	if session.Workspace == nil || session.Workspace.ID != "ws-1" {
		t.Fatalf("workspace was not cloned into session: %#v", session.Workspace)
	}

	createdAt := time.Now().UTC().Add(-time.Minute)
	if err := store.AddAgentRun(ctx, session.Summary.ID, AgentRun{
		ID:             "run-1",
		Agent:          "codex",
		Message:        "hello",
		Output:         "world",
		ExitCode:       0,
		Success:        true,
		Running:        true,
		CreatedAt:      createdAt,
		AgentSessionID: "agent-session-1",
		StopReason:     "completed",
	}); err != nil {
		t.Fatalf("AddAgentRun returned error: %v", err)
	}
	cells, err := store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListCells returned error: %v", err)
	}
	if len(cells) != 1 || cells[0].Type != execution.CellTypeAgent || cells[0].AgentSessionID != "agent-session-1" || !cells[0].Running {
		t.Fatalf("agent run cells = %#v", cells)
	}

	if err := store.SaveVMState(session.Summary.ID, VMState{Driver: driverpkg.RuntimeDriverMicrosandbox, Image: "micro:latest"}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}
	vmState, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState returned error: %v", err)
	}
	if vmState.Driver != driverpkg.RuntimeDriverMicrosandbox || vmState.Mode != driverpkg.RuntimeDriverMicrosandbox || vmState.RuntimeHome == "" {
		t.Fatalf("vm state = %#v", vmState)
	}

	if err := os.Remove(store.vmStatePath(session.Summary.ID)); err != nil {
		t.Fatalf("remove runtime vm state: %v", err)
	}
	legacyData, err := json.Marshal(VMState{Mode: driverpkg.RuntimeDriverBoxlite, Image: "legacy:latest"})
	if err != nil {
		t.Fatalf("marshal legacy vm state: %v", err)
	}
	if err := os.WriteFile(store.legacyVMStatePath(session.Summary.ID), legacyData, 0o644); err != nil {
		t.Fatalf("write legacy vm state: %v", err)
	}
	vmState, err = store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState legacy returned error: %v", err)
	}
	if vmState.Driver != driverpkg.RuntimeDriverBoxlite || vmState.Image != "legacy:latest" {
		t.Fatalf("legacy vm state = %#v", vmState)
	}

	if err := store.AddEvent(ctx, session.Summary.ID, SessionEvent{ID: "evt-1", Type: "session.started", Level: "info", Message: "started", CreatedAt: createdAt}); err != nil {
		t.Fatalf("AddEvent returned error: %v", err)
	}
	events, err := store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if len(events) != 1 || events[0].ID != "evt-1" {
		t.Fatalf("events = %#v", events)
	}

	listed, err := store.ListSessions(ctx, SessionListOptions{TitleQuery: "agent-compose", Driver: driverpkg.RuntimeDriverBoxlite, VMStatus: domain.VMStatusPending, Limit: 1})
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if listed.TotalCount != 1 || len(listed.Sessions) != 1 || listed.HasMore {
		t.Fatalf("listed sessions = %#v", listed)
	}
	stored, err := store.loadSession(session.Summary.ID)
	if err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}
	stored.Summary.GuestImage = ""
	if err := store.saveSession(stored); err != nil {
		t.Fatalf("saveSession returned error: %v", err)
	}
	loaded, err := store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if loaded.Summary.EventCount != 1 || loaded.Summary.CellCount != 1 || loaded.Summary.GuestImage != "legacy:latest" {
		t.Fatalf("loaded session summary = %#v", loaded.Summary)
	}
}
