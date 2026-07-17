package sessionstore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

type sandboxRecorderStub struct {
	recorded map[string]*domain.Sandbox
	err      error
}

func (s *sandboxRecorderStub) UpsertSandbox(_ context.Context, sandbox *domain.Sandbox) error {
	if s.err != nil {
		return s.err
	}
	copy := *sandbox
	copy.Summary = sandbox.Summary
	copy.Summary.Tags = append([]domain.SandboxTag(nil), sandbox.Summary.Tags...)
	s.recorded[sandbox.Summary.ID] = &copy
	return nil
}

func TestCreateSandboxRecordsNewSandbox(t *testing.T) {
	recorder := &sandboxRecorderStub{recorded: make(map[string]*domain.Sandbox)}
	store, err := NewWithConfigAndRecorder(sandboxRecordTestConfig(t), recorder)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	created, err := store.CreateSandbox(context.Background(), "record me", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", "api", nil, nil, []domain.SandboxTag{{Name: "origin", Value: "test"}})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	recorded := recorder.recorded[created.Summary.ID]
	if recorded == nil {
		t.Fatalf("sandbox %q was not recorded", created.Summary.ID)
	}
	if recorded.Summary.Title != created.Summary.Title || recorded.Summary.VMStatus != domain.VMStatusPending {
		t.Fatalf("recorded summary = %#v, want %#v", recorded.Summary, created.Summary)
	}

	created.Summary.VMStatus = domain.VMStatusRunning
	created.Summary.CellCount = 1
	if err := store.UpdateSandbox(context.Background(), created); err != nil {
		t.Fatalf("update sandbox: %v", err)
	}
	recorded = recorder.recorded[created.Summary.ID]
	if recorded.Summary.VMStatus != domain.VMStatusRunning || recorded.Summary.CellCount != 1 {
		t.Fatalf("recorded updated summary = %#v", recorded.Summary)
	}
	if err := store.AddEvent(context.Background(), created.Summary.ID, domain.SandboxEvent{ID: "event-1", Type: "test"}); err != nil {
		t.Fatalf("add event: %v", err)
	}
	recorded = recorder.recorded[created.Summary.ID]
	if recorded.Summary.EventCount != 1 {
		t.Fatalf("recorded event count = %d, want 1", recorded.Summary.EventCount)
	}
}

func TestMigrateSandboxRecordsScansExistingMetadata(t *testing.T) {
	config := sandboxRecordTestConfig(t)
	filesystemStore, err := NewWithConfig(config)
	if err != nil {
		t.Fatalf("create filesystem store: %v", err)
	}
	existing, err := filesystemStore.CreateSandbox(context.Background(), "existing", "", driverpkg.RuntimeDriverBoxlite, "existing:latest", "", "api", nil, nil, nil)
	if err != nil {
		t.Fatalf("create existing sandbox: %v", err)
	}

	recorder := &sandboxRecorderStub{recorded: make(map[string]*domain.Sandbox)}
	if _, err := NewWithConfigAndRecorder(config, recorder); err != nil {
		t.Fatalf("create store with migration recorder: %v", err)
	}
	recorded := recorder.recorded[existing.Summary.ID]
	if recorded == nil {
		t.Fatalf("existing sandbox %q was not migrated", existing.Summary.ID)
	}
	if recorded.Summary.GuestImage != "existing:latest" {
		t.Fatalf("migrated image = %q, want existing:latest", recorded.Summary.GuestImage)
	}
}

func TestCreateSandboxReturnsRecorderFailure(t *testing.T) {
	recorder := &sandboxRecorderStub{recorded: make(map[string]*domain.Sandbox), err: fmt.Errorf("database unavailable")}
	store, err := NewWithConfigAndRecorder(sandboxRecordTestConfig(t), recorder)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if _, err := store.CreateSandbox(context.Background(), "record failure", "", driverpkg.RuntimeDriverBoxlite, "", "", "api", nil, nil, nil); err == nil {
		t.Fatal("create sandbox succeeded despite recorder failure")
	}
}

func sandboxRecordTestConfig(t *testing.T) *appconfig.Config {
	t.Helper()
	root := t.TempDir()
	return &appconfig.Config{
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "default:latest",
		MicrosandboxHome:     filepath.Join(root, "microsandbox"),
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/jupyter",
	}
}
