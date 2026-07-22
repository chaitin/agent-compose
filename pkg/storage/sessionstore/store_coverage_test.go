package sessionstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
)

func TestStoreAgentRunLegacyVMAndListWorkflows(t *testing.T) {
	testStoreAgentRunLegacyVMAndListWorkflows(t)
}

func TestIntegrationStoreAgentRunLegacyVMAndListWorkflows(t *testing.T) {
	testStoreAgentRunLegacyVMAndListWorkflows(t)
}

func TestE2EStoreAgentRunLegacyVMAndListWorkflows(t *testing.T) {
	testStoreAgentRunLegacyVMAndListWorkflows(t)
}

func TestStorePersistenceErrorAndUpdateBranches(t *testing.T) {
	testStorePersistenceErrorAndUpdateBranches(t)
}

func TestIntegrationStorePersistenceErrorAndUpdateBranches(t *testing.T) {
	testStorePersistenceErrorAndUpdateBranches(t)
}

func TestE2EStorePersistenceErrorAndUpdateBranches(t *testing.T) {
	testStorePersistenceErrorAndUpdateBranches(t)
}

func TestStoreCreateSessionUsesConfiguredJupyterProxyBase(t *testing.T) {
	ctx := context.Background()
	store := newCoverageStore(t)
	session, err := store.CreateSandbox(ctx, "Prefixed Proxy", "", driverpkg.RuntimeDriverBoxlite, "", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if !identity.IsID(session.Summary.ID) {
		t.Fatalf("session id = %q, want sha256 identity", session.Summary.ID)
	}
	if session.Summary.ShortID != identity.ShortID(session.Summary.ID) || len(session.Summary.ShortID) != 12 {
		t.Fatalf("session short id = %q for id %q", session.Summary.ShortID, session.Summary.ID)
	}
	if session.Summary.RuntimeRef != "agent-compose-"+session.Summary.ShortID {
		t.Fatalf("runtime ref = %q, want short-id ref", session.Summary.RuntimeRef)
	}
	sandboxDir := store.SandboxDir(session.Summary.ID)
	if strings.ContainsAny(filepath.Base(sandboxDir), ",:;") {
		t.Fatalf("sandbox dir basename = %q, want no runtime-forbidden characters", filepath.Base(sandboxDir))
	}
	if filepath.Base(sandboxDir) != strings.TrimPrefix(session.Summary.ID, identity.Prefix) {
		t.Fatalf("sandbox dir = %q, want hash identity path", sandboxDir)
	}
	if session.Summary.WorkspacePath != filepath.Join(sandboxDir, "workspace") {
		t.Fatalf("workspace path = %q, want under sandbox dir %q", session.Summary.WorkspacePath, sandboxDir)
	}
	if gotRoot := filepath.Dir(sandboxDir); filepath.Base(gotRoot) != "sandboxes" {
		t.Fatalf("sandbox dir root = %q, want sandboxes", gotRoot)
	}
	for _, rel := range []string{
		"metadata.json",
		"workspace",
		"context",
		"home",
		"runtime",
		"state",
		"logs",
		"vm",
		"proxy",
		filepath.Join("state", "cells.json"),
		filepath.Join("state", "events.jsonl"),
		filepath.Join("vm", "runtime.json"),
		filepath.Join("proxy", "jupyter.json"),
	} {
		if _, err := os.Stat(filepath.Join(sandboxDir, rel)); err != nil {
			t.Fatalf("sandbox layout missing %s: %v", rel, err)
		}
	}
	wantProxyPath := "/agent-compose/session/" + session.Summary.ID + "/lab"
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
	if proxyState.Enabled || proxyState.GuestPort != 0 || proxyState.HostPort != 0 || proxyState.Token != "" {
		t.Fatalf("default proxy state = %+v, want jupyter disabled without ports/token", proxyState)
	}
}

func TestSessionLockKeyUsesSessionDirName(t *testing.T) {
	id := identity.NewID(identity.ResourceSandbox, "lock-key")
	if got, want := sandboxLockKey(id), sandboxDirName(id); got != want {
		t.Fatalf("sandboxLockKey(%q) = %q, want %q", id, got, want)
	}
	if sandboxLockKey(id) != sandboxLockKey(strings.TrimPrefix(id, identity.Prefix)) {
		t.Fatalf("session lock key should match canonical and directory-form ids")
	}
}

func TestSessionDirNameFallbackPreservesInvalidInput(t *testing.T) {
	for _, id := range []string{" . ", " .. ", "   "} {
		if got := sandboxDirName(id); got != id {
			t.Fatalf("sandboxDirName(%q) = %q, want unchanged fallback", id, got)
		}
	}
}

func TestStoreCreateSessionWithJupyterOptions(t *testing.T) {
	ctx := context.Background()
	store := newCoverageStore(t)
	session, err := store.CreateSandboxWithOptions(ctx, "Jupyter", "", driverpkg.RuntimeDriverBoxlite, "", "", "", nil, nil, nil, CreateSandboxOptions{
		JupyterEnabled:   true,
		JupyterGuestPort: 9999,
		JupyterExpose:    true,
	})
	if err != nil {
		t.Fatalf("CreateSandboxWithOptions returned error: %v", err)
	}
	proxyState, err := store.GetProxyState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState returned error: %v", err)
	}
	if !proxyState.Enabled || !proxyState.Exposed || proxyState.GuestPort != 9999 || proxyState.HostPort == 0 || proxyState.Token == "" {
		t.Fatalf("proxy state = %+v, want enabled/exposed guest port 9999 with host port/token", proxyState)
	}
}

func TestStoreCreateSessionJupyterHostPortDependsOnDriver(t *testing.T) {
	ctx := context.Background()
	for _, driver := range []string{driverpkg.RuntimeDriverDocker, driverpkg.RuntimeDriverBoxlite, driverpkg.RuntimeDriverMicrosandbox} {
		t.Run(driver, func(t *testing.T) {
			store := newCoverageStore(t)
			session, err := store.CreateSandboxWithOptions(ctx, "Jupyter", "", driver, "", "", "", nil, nil, nil, CreateSandboxOptions{JupyterEnabled: true})
			if err != nil {
				t.Fatalf("CreateSandboxWithOptions returned error: %v", err)
			}
			proxyState, err := store.GetProxyState(session.Summary.ID)
			if err != nil {
				t.Fatalf("GetProxyState returned error: %v", err)
			}
			if driver == driverpkg.RuntimeDriverDocker && proxyState.HostPort != 0 {
				t.Fatalf("docker HostPort = %d, want Docker-assigned zero initial port", proxyState.HostPort)
			}
			if driver != driverpkg.RuntimeDriverDocker && proxyState.HostPort == 0 {
				t.Fatalf("%s HostPort = 0, want preallocated port", driver)
			}
		})
	}
}

func TestStoreCreateSessionInitializesJSONLEvents(t *testing.T) {
	ctx := context.Background()
	store := newCoverageStore(t)
	session, err := store.CreateSandbox(ctx, "JSONL Events", "", driverpkg.RuntimeDriverBoxlite, "", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if _, err := os.Stat(store.eventsJSONLPath(session.Summary.ID)); err != nil {
		t.Fatalf("events.jsonl stat err = %v, want file", err)
	}
	if _, err := os.Stat(store.eventsJSONPath(session.Summary.ID)); !os.IsNotExist(err) {
		t.Fatalf("legacy events.json stat err = %v, want not exist", err)
	}
}

func TestStoreEventJSONLLegacyCompatibility(t *testing.T) {
	ctx := context.Background()
	store := newCoverageStore(t)
	session, err := store.CreateSandbox(ctx, "Legacy JSON Events", "", driverpkg.RuntimeDriverBoxlite, "", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	baseTime := time.Now().UTC().Round(0)
	if err := os.Remove(store.eventsJSONLPath(session.Summary.ID)); err != nil {
		t.Fatalf("remove initial events.jsonl: %v", err)
	}
	legacyEvents := []SandboxEvent{{ID: "legacy-1", Type: "session.legacy", Level: "info", Message: "legacy", CreatedAt: baseTime}}
	legacyData, err := json.Marshal(legacyEvents)
	if err != nil {
		t.Fatalf("Marshal legacy events returned error: %v", err)
	}
	if err := os.WriteFile(store.eventsJSONPath(session.Summary.ID), legacyData, 0o644); err != nil {
		t.Fatalf("write legacy events returned error: %v", err)
	}

	events, err := store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents legacy returned error: %v", err)
	}
	if len(events) != 1 || events[0].ID != "legacy-1" {
		t.Fatalf("legacy events = %#v", events)
	}

	if err := store.AddEvent(ctx, session.Summary.ID, SandboxEvent{ID: "jsonl-1", Type: "session.jsonl", Level: "info", Message: "jsonl", CreatedAt: baseTime.Add(time.Second)}); err != nil {
		t.Fatalf("AddEvent returned error: %v", err)
	}
	events, err = store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents mixed returned error: %v", err)
	}
	if len(events) != 2 || events[0].ID != "legacy-1" || events[1].ID != "jsonl-1" {
		t.Fatalf("mixed events = %#v", events)
	}
	loaded, err := store.GetSandbox(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if loaded.Summary.EventCount != 2 {
		t.Fatalf("EventCount = %d, want 2", loaded.Summary.EventCount)
	}
}

func TestStoreSaveEventsReplacesWithJSONLAndRemovesLegacy(t *testing.T) {
	ctx := context.Background()
	store := newCoverageStore(t)
	session, err := store.CreateSandbox(ctx, "Replace Events", "", driverpkg.RuntimeDriverBoxlite, "", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if err := os.WriteFile(store.eventsJSONPath(session.Summary.ID), []byte(`[{"id":"legacy"}]`), 0o644); err != nil {
		t.Fatalf("write legacy events returned error: %v", err)
	}
	events := []SandboxEvent{
		{ID: "replacement-1", Type: "session.replaced", Level: "info", Message: "one", CreatedAt: time.Now().UTC().Round(0)},
		{ID: "replacement-2", Type: "session.replaced", Level: "info", Message: "two", CreatedAt: time.Now().UTC().Round(0)},
	}
	if err := store.SaveEvents(session.Summary.ID, events); err != nil {
		t.Fatalf("SaveEvents returned error: %v", err)
	}
	if _, err := os.Stat(store.eventsJSONPath(session.Summary.ID)); !os.IsNotExist(err) {
		t.Fatalf("legacy events.json stat err = %v, want not exist", err)
	}
	loaded, err := store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if len(loaded) != 2 || loaded[0].ID != "replacement-1" || loaded[1].ID != "replacement-2" {
		t.Fatalf("loaded replacement events = %#v", loaded)
	}
	metadata, err := store.GetSandbox(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession after SaveEvents returned error: %v", err)
	}
	if metadata.Summary.EventCount != 2 {
		t.Fatalf("EventCount after SaveEvents = %d, want 2", metadata.Summary.EventCount)
	}
	data, err := os.ReadFile(store.eventsJSONLPath(session.Summary.ID))
	if err != nil {
		t.Fatalf("read events.jsonl returned error: %v", err)
	}
	if strings.Contains(string(data), "[") || strings.Count(string(data), "\n") != 2 {
		t.Fatalf("events.jsonl data = %q, want two JSONL records", string(data))
	}

	replacement := []SandboxEvent{{ID: "replacement-final", Type: "session.replaced", Level: "info", Message: "final", CreatedAt: time.Now().UTC().Round(0)}}
	if err := store.SaveEvents(session.Summary.ID, replacement); err != nil {
		t.Fatalf("SaveEvents final replacement returned error: %v", err)
	}
	metadata, err = store.GetSandbox(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession after final SaveEvents returned error: %v", err)
	}
	if metadata.Summary.EventCount != 1 {
		t.Fatalf("EventCount after final SaveEvents = %d, want 1", metadata.Summary.EventCount)
	}
}

func TestStoreListEventsCorruptJSONLIncludesLineNumber(t *testing.T) {
	ctx := context.Background()
	store := newCoverageStore(t)
	session, err := store.CreateSandbox(ctx, "Corrupt JSONL", "", driverpkg.RuntimeDriverBoxlite, "", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	data := `{"id":"ok","type":"session.ok","level":"info","message":"ok","created_at":"2026-07-07T00:00:00Z"}` + "\n\n{bad json}\n"
	if err := os.WriteFile(store.eventsJSONLPath(session.Summary.ID), []byte(data), 0o644); err != nil {
		t.Fatalf("write corrupt events.jsonl returned error: %v", err)
	}
	_, err = store.ListEvents(ctx, session.Summary.ID)
	if err == nil {
		t.Fatalf("ListEvents corrupt JSONL returned nil error")
	}
	if !strings.Contains(err.Error(), store.eventsJSONLPath(session.Summary.ID)) || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("ListEvents corrupt JSONL error = %v, want file path and line number", err)
	}
}

func TestStoreConcurrentAddEventJSONL(t *testing.T) {
	ctx := context.Background()
	store := newCoverageStore(t)
	session, err := store.CreateSandbox(ctx, "Concurrent Events", "", driverpkg.RuntimeDriverBoxlite, "", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	const eventCount = 200
	var wg sync.WaitGroup
	errCh := make(chan error, eventCount)
	for index := 0; index < eventCount; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- store.AddEvent(ctx, session.Summary.ID, SandboxEvent{
				ID:        fmt.Sprintf("event-%03d", index),
				Type:      "session.concurrent",
				Level:     "info",
				Message:   "concurrent",
				CreatedAt: time.Now().UTC(),
			})
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("AddEvent concurrent returned error: %v", err)
		}
	}

	events, err := store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if len(events) != eventCount {
		t.Fatalf("len(events) = %d, want %d", len(events), eventCount)
	}
	loaded, err := store.GetSandbox(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if loaded.Summary.EventCount != eventCount {
		t.Fatalf("EventCount = %d, want %d", loaded.Summary.EventCount, eventCount)
	}
	lines, err := store.loadJSONLEvents(session.Summary.ID)
	if err != nil {
		t.Fatalf("loadJSONLEvents returned error: %v", err)
	}
	if len(lines) != eventCount {
		t.Fatalf("JSONL event count = %d, want %d", len(lines), eventCount)
	}
}

func TestStoreLegacyWrappersAndMissingStateWorkflows(t *testing.T) {
	testStoreLegacyWrappersAndMissingStateWorkflows(t)
}

func TestIntegrationStoreLegacyWrappersAndMissingStateWorkflows(t *testing.T) {
	testStoreLegacyWrappersAndMissingStateWorkflows(t)
}

func TestE2EStoreLegacyWrappersAndMissingStateWorkflows(t *testing.T) {
	testStoreLegacyWrappersAndMissingStateWorkflows(t)
}

func TestIntegrationStoreCreateAndRemoveWorkflows(t *testing.T) {
	TestStoreCreateSessionUsesConfiguredJupyterProxyBase(t)
	TestStoreCreateSessionWithJupyterOptions(t)
	TestRemoveSessionDeletesSessionDirectory(t)
	TestRemoveSessionRejectsUnsafeIDs(t)
	TestRemoveSessionMissingDirectoryReturnsError(t)
}

func TestE2EStoreCreateAndRemoveWorkflows(t *testing.T) {
	TestIntegrationStoreCreateAndRemoveWorkflows(t)
}

func TestNewWithConfigAllowsNonEmptyLegacySessionsRoot(t *testing.T) {
	dataRoot := t.TempDir()
	legacyRoot := filepath.Join(dataRoot, "sessions")
	if err := os.MkdirAll(legacyRoot, 0o755); err != nil {
		t.Fatalf("create legacy root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyRoot, "metadata.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}
	sandboxRoot := filepath.Join(dataRoot, "sandboxes")

	_, err := NewWithConfig(&appconfig.Config{
		DataRoot:    dataRoot,
		SandboxRoot: sandboxRoot,
	})
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	if info, statErr := os.Stat(sandboxRoot); statErr != nil || !info.IsDir() {
		t.Fatalf("sandbox root stat = %v/%v, want directory", info, statErr)
	}
}

func TestNewWithConfigAllowsEmptyLegacySessionsRoot(t *testing.T) {
	dataRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataRoot, "sessions"), 0o755); err != nil {
		t.Fatalf("create empty legacy root: %v", err)
	}
	sandboxRoot := filepath.Join(dataRoot, "sandboxes")

	if _, err := NewWithConfig(&appconfig.Config{
		DataRoot:    dataRoot,
		SandboxRoot: sandboxRoot,
	}); err != nil {
		t.Fatalf("NewWithConfig returned error for empty legacy root: %v", err)
	}
	if info, err := os.Stat(sandboxRoot); err != nil || !info.IsDir() {
		t.Fatalf("sandbox root stat = %v/%v, want directory", info, err)
	}
}

func TestNewWithConfigAllowsExplicitSandboxRootBesideLegacySessionsRoot(t *testing.T) {
	dataRoot := t.TempDir()
	legacyRoot := filepath.Join(dataRoot, "sessions")
	if err := os.MkdirAll(legacyRoot, 0o755); err != nil {
		t.Fatalf("create legacy root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyRoot, "metadata.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}
	sandboxRoot := filepath.Join(t.TempDir(), "fresh-sandboxes")

	if _, err := NewWithConfig(&appconfig.Config{
		DataRoot:            dataRoot,
		SandboxRoot:         sandboxRoot,
		SandboxRootExplicit: true,
	}); err != nil {
		t.Fatalf("NewWithConfig returned error for explicit sandbox root: %v", err)
	}
	if info, err := os.Stat(sandboxRoot); err != nil || !info.IsDir() {
		t.Fatalf("sandbox root stat = %v/%v, want directory", info, err)
	}
}

func TestStoreReadsAndRemovesSandboxFromLegacySessionsRoot(t *testing.T) {
	ctx := context.Background()
	legacyRoot := filepath.Join(t.TempDir(), "sessions")
	store, err := NewWithConfig(&appconfig.Config{
		SandboxRoot:   legacyRoot,
		RuntimeDriver: driverpkg.RuntimeDriverDocker,
		DefaultImage:  "debian:bookworm-slim",
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyID := "legacy-session"
	legacyDir := store.SandboxDir(legacyID)
	if err := os.MkdirAll(filepath.Join(legacyDir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(Sandbox{Summary: SandboxSummary{
		ID: legacyID, Title: "Legacy sandbox", Driver: driverpkg.RuntimeDriverDocker,
		VMStatus: VMStatusStopped, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "metadata.json"), metadata, 0o644); err != nil {
		t.Fatal(err)
	}
	legacyCells := []byte(`[{"id":"cell-1","source":"hello","agent_session_id":"thread-1","agent_resume":{"session_id":"thread-1","session_state_path":"/state.json"}}]`)
	if err := os.WriteFile(filepath.Join(legacyDir, "state", "cells.json"), legacyCells, 0o644); err != nil {
		t.Fatal(err)
	}
	if sandbox, err := store.GetSandbox(ctx, legacyID); err != nil || sandbox.Summary.ID != legacyID {
		t.Fatalf("GetSandbox sandbox=%#v err=%v", sandbox, err)
	}
	// The legacy dir was written directly to disk after startup, so index it as
	// the startup rebuild would when discovering pre-existing sandbox dirs.
	store.rebuildIndex(ctx)
	if result, err := store.ListSandboxes(ctx, SandboxListOptions{}); err != nil || len(result.Sandboxes) != 1 || result.Sandboxes[0].Summary.ID != legacyID {
		t.Fatalf("ListSandboxes result=%#v err=%v", result, err)
	}
	if cells, err := store.ListCells(ctx, legacyID); err != nil || len(cells) != 1 || cells[0].AgentThreadID != "thread-1" || cells[0].AgentResume == nil || cells[0].AgentResume.ThreadID != "thread-1" {
		t.Fatalf("ListCells cells=%#v err=%v", cells, err)
	}
	if err := store.RemoveSandbox(ctx, legacyID); err != nil {
		t.Fatalf("RemoveSandbox returned error: %v", err)
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("legacy sandbox dir stat error = %v, want not exist", err)
	}
}

func testStoreLegacyWrappersAndMissingStateWorkflows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := newCoverageStore(t)
	sessionID := "legacy-session"
	sandboxDir := store.SandboxDir(sessionID)
	for _, dir := range []string{
		filepath.Join(sandboxDir, "state"),
		filepath.Join(sandboxDir, "vm"),
		filepath.Join(sandboxDir, "proxy"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s returned error: %v", dir, err)
		}
	}
	if got, want := store.VMStatePath(sessionID), filepath.Join(sandboxDir, "vm", "runtime.json"); got != want {
		t.Fatalf("VMStatePath = %q, want %q", got, want)
	}
	if got, want := store.LegacyVMStatePath(sessionID), filepath.Join(sandboxDir, "vm", "boxlite.json"); got != want {
		t.Fatalf("LegacyVMStatePath = %q, want %q", got, want)
	}
	if got, want := store.ProxyStatePath(sessionID), filepath.Join(sandboxDir, "proxy", "jupyter.json"); got != want {
		t.Fatalf("ProxyStatePath = %q, want %q", got, want)
	}
	if port, err := store.AllocateHostPortForJupyter(); err != nil || port == 0 {
		t.Fatalf("AllocateHostPortForJupyter port=%d err=%v", port, err)
	}

	fileRoot := filepath.Join(t.TempDir(), "sandbox-root-file")
	if err := os.WriteFile(fileRoot, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write file sandbox root: %v", err)
	}
	if _, err := NewWithConfig(&appconfig.Config{SandboxRoot: fileRoot}); err == nil {
		t.Fatalf("NewWithConfig file sandbox root returned nil error")
	}

	fromConfigStore := FromConfig(store.config)
	baseTime := time.Now().UTC().Add(-time.Hour).Round(0)
	session := &Sandbox{Summary: SandboxSummary{
		ID:        sessionID,
		Title:     "Legacy",
		Driver:    driverpkg.RuntimeDriverBoxlite,
		CreatedAt: baseTime,
		UpdatedAt: baseTime,
	}}
	if err := fromConfigStore.SaveSandbox(session); err != nil {
		t.Fatalf("SaveSession returned error: %v", err)
	}
	loaded, err := fromConfigStore.LoadSandbox(sessionID)
	if err != nil {
		t.Fatalf("LoadSession returned error: %v", err)
	}
	if loaded.Summary.ID != sessionID || loaded.Summary.Driver != driverpkg.RuntimeDriverBoxlite {
		t.Fatalf("LoadSession loaded %#v", loaded.Summary)
	}
	if err := fromConfigStore.SaveSandbox(&Sandbox{Summary: SandboxSummary{ID: "missing-dir"}}); err == nil {
		t.Fatalf("SaveSession missing dir returned nil error")
	}
	if err := fromConfigStore.SaveCells("missing-dir", nil); err == nil {
		t.Fatalf("SaveCells missing dir returned nil error")
	}
	if err := fromConfigStore.SaveEvents("missing-dir", nil); err == nil {
		t.Fatalf("SaveEvents missing dir returned nil error")
	}

	initialCells := []NotebookCell{
		{ID: "run-existing", Type: CellTypeAgent, Source: "already migrated", CreatedAt: baseTime.Add(2 * time.Minute)},
		{ID: "shell-later", Type: execution.CellTypeShell, Source: "echo later", CreatedAt: baseTime.Add(3 * time.Minute)},
	}
	if err := fromConfigStore.SaveCells(sessionID, initialCells); err != nil {
		t.Fatalf("SaveCells returned error: %v", err)
	}
	legacyRuns := []AgentRun{
		{
			ID:            "run-new",
			Agent:         "codex",
			Message:       "legacy prompt",
			Output:        "legacy output",
			ExitCode:      7,
			Success:       false,
			CreatedAt:     baseTime.Add(time.Minute),
			AgentThreadID: "legacy-agent-session",
			StopReason:    "stopped",
		},
		{ID: "run-existing", Agent: "codex", Message: "duplicate", CreatedAt: baseTime.Add(4 * time.Minute)},
	}
	legacyData, err := json.Marshal(legacyRuns)
	if err != nil {
		t.Fatalf("Marshal legacy runs returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxDir, "state", "agent_runs.json"), legacyData, 0o644); err != nil {
		t.Fatalf("write legacy agent runs returned error: %v", err)
	}
	cells, err := fromConfigStore.ListCells(ctx, sessionID)
	if err != nil {
		t.Fatalf("ListCells legacy merge returned error: %v", err)
	}
	if len(cells) != 3 || cells[0].ID != "run-new" || cells[1].ID != "run-existing" || cells[2].ID != "shell-later" {
		t.Fatalf("legacy merged cells = %#v", cells)
	}
	if cells[0].Type != CellTypeAgent || cells[0].Source != "legacy prompt" || cells[0].Output != "legacy output" || cells[0].ExitCode != 7 || cells[0].AgentThreadID != "legacy-agent-session" || cells[0].StopReason != "stopped" {
		t.Fatalf("legacy run cell = %#v", cells[0])
	}

	events := []SandboxEvent{{ID: "event-wrapper", Type: "session.wrapper", Level: "info", Message: "wrapper", CreatedAt: baseTime}}
	if err := fromConfigStore.SaveEvents(sessionID, events); err != nil {
		t.Fatalf("SaveEvents returned error: %v", err)
	}
	loadedEvents, err := fromConfigStore.ListEvents(ctx, sessionID)
	if err != nil {
		t.Fatalf("ListEvents wrapper returned error: %v", err)
	}
	if len(loadedEvents) != 1 || loadedEvents[0].ID != "event-wrapper" {
		t.Fatalf("loaded events = %#v", loadedEvents)
	}

	missingStateID := "missing-state"
	if err := os.MkdirAll(filepath.Join(store.SandboxDir(missingStateID), "state"), 0o755); err != nil {
		t.Fatalf("MkdirAll missing state returned error: %v", err)
	}
	if cells, err := fromConfigStore.ListCells(ctx, missingStateID); err != nil || len(cells) != 0 {
		t.Fatalf("ListCells missing file cells=%#v err=%v", cells, err)
	}
	if events, err := fromConfigStore.ListEvents(ctx, missingStateID); err != nil || len(events) != 0 {
		t.Fatalf("ListEvents missing file events=%#v err=%v", events, err)
	}
	if err := os.WriteFile(filepath.Join(store.SandboxDir(missingStateID), "state", "agent_runs.json"), nil, 0o644); err != nil {
		t.Fatalf("write empty agent runs returned error: %v", err)
	}
	if runs, err := fromConfigStore.loadAgentRuns(missingStateID); err != nil || len(runs) != 0 {
		t.Fatalf("loadAgentRuns empty runs=%#v err=%v", runs, err)
	}
	if err := fromConfigStore.SaveCells(missingStateID, nil); err != nil {
		t.Fatalf("SaveCells missingState reset returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.SandboxDir(missingStateID), "state", "agent_runs.json"), []byte(`{bad json`), 0o644); err != nil {
		t.Fatalf("write corrupt agent runs returned error: %v", err)
	}
	if _, err := fromConfigStore.ListCells(ctx, missingStateID); err == nil {
		t.Fatalf("ListCells corrupt legacy agent runs returned nil error")
	}
}

func testStorePersistenceErrorAndUpdateBranches(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := newCoverageStore(t)
	session, err := store.CreateSandbox(ctx, "Persistence Branches", "", driverpkg.RuntimeDriverBoxlite, "", "", "", nil, nil, nil)
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
	cellDir := filepath.Join(store.sandboxDir(session.Summary.ID), "state", "cells", runningCell.ID)
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

	if err := store.AddEvent(ctx, session.Summary.ID, SandboxEvent{ID: "event-1", Type: "session.tested", Level: "info", Message: "tested", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("AddEvent returned error: %v", err)
	}
	loaded, err := store.GetSandbox(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if loaded.Summary.CellCount != 2 || loaded.Summary.EventCount != 1 {
		t.Fatalf("loaded counts = cells %d events %d", loaded.Summary.CellCount, loaded.Summary.EventCount)
	}

	if err := os.WriteFile(filepath.Join(store.sandboxDir(session.Summary.ID), "state", "cells.json"), []byte(`{bad json`), 0o644); err != nil {
		t.Fatalf("write corrupt cells: %v", err)
	}
	if _, err := store.ListCells(ctx, session.Summary.ID); err == nil {
		t.Fatalf("ListCells corrupt returned nil error")
	}
	if err := store.saveCells(session.Summary.ID, nil); err != nil {
		t.Fatalf("saveCells reset returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.sandboxDir(session.Summary.ID), "state", "events.json"), []byte(`{bad json`), 0o644); err != nil {
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
	if err := os.WriteFile(filepath.Join(store.sandboxDir(session.Summary.ID), "metadata.json"), []byte(`{"summary":{"driver":"bad"}}`), 0o644); err != nil {
		t.Fatalf("write invalid metadata: %v", err)
	}
	if _, err := store.GetSandbox(ctx, session.Summary.ID); err == nil {
		t.Fatalf("GetSession invalid metadata returned nil error")
	}
}

func testStoreAgentRunLegacyVMAndListWorkflows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := newCoverageStore(t)

	session, err := store.CreateSandbox(ctx, "", "", driverpkg.RuntimeDriverBoxlite, "", "ws-1", "script:timer", &SandboxWorkspace{
		ID:         "ws-1",
		Name:       "Workspace",
		Type:       "file",
		ConfigJSON: "{}",
	}, []SandboxEnvVar{{Name: "PLAIN", Value: "value"}}, []SandboxTag{{Name: "kind", Value: "loader"}})
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
		ID:            "run-1",
		Agent:         "codex",
		Message:       "hello",
		Output:        "world",
		ExitCode:      0,
		Success:       true,
		Running:       true,
		CreatedAt:     createdAt,
		AgentThreadID: "agent-session-1",
		StopReason:    "completed",
	}); err != nil {
		t.Fatalf("AddAgentRun returned error: %v", err)
	}
	cells, err := store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListCells returned error: %v", err)
	}
	if len(cells) != 1 || cells[0].Type != execution.CellTypeAgent || cells[0].AgentThreadID != "agent-session-1" || !cells[0].Running {
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

	if err := store.AddEvent(ctx, session.Summary.ID, SandboxEvent{ID: "evt-1", Type: "session.started", Level: "info", Message: "started", CreatedAt: createdAt}); err != nil {
		t.Fatalf("AddEvent returned error: %v", err)
	}
	events, err := store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if len(events) != 1 || events[0].ID != "evt-1" {
		t.Fatalf("events = %#v", events)
	}

	listed, err := store.ListSandboxes(ctx, SandboxListOptions{TitleQuery: "agent-compose", Driver: driverpkg.RuntimeDriverBoxlite, VMStatus: domain.VMStatusPending, Limit: 1})
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if listed.TotalCount != 1 || len(listed.Sandboxes) != 1 || listed.HasMore {
		t.Fatalf("listed sessions = %#v", listed)
	}
	stored, err := store.loadSandbox(session.Summary.ID)
	if err != nil {
		t.Fatalf("loadSandbox returned error: %v", err)
	}
	stored.Summary.GuestImage = ""
	if err := store.saveSandbox(stored); err != nil {
		t.Fatalf("saveSandbox returned error: %v", err)
	}
	loaded, err := store.GetSandbox(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if loaded.Summary.EventCount != 1 || loaded.Summary.CellCount != 1 || loaded.Summary.GuestImage != "legacy:latest" {
		t.Fatalf("loaded session summary = %#v", loaded.Summary)
	}
}

func newCoverageStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewWithConfig(&appconfig.Config{
		SandboxRoot:          filepath.Join(t.TempDir(), "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "default-box:latest",
		ImageRegistry:        "registry.test",
		GuestHomePath:        "/home/agent-compose",
		MicrosandboxHome:     filepath.Join(t.TempDir(), "microsandbox"),
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
	})
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
