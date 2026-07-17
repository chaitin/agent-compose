package sessionstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

type sandboxRecorderStub struct {
	recorded     map[string]*domain.Sandbox
	deleted      []string
	err          error
	upsertErrors map[string]error
}

func (s *sandboxRecorderStub) UpsertSandbox(_ context.Context, sandbox *domain.Sandbox) error {
	if s.err != nil {
		return s.err
	}
	if err := s.upsertErrors[sandbox.Summary.ID]; err != nil {
		return err
	}
	copy := *sandbox
	copy.Summary = sandbox.Summary
	copy.Summary.Tags = append([]domain.SandboxTag(nil), sandbox.Summary.Tags...)
	s.recorded[sandbox.Summary.ID] = &copy
	return nil
}

func (s *sandboxRecorderStub) DeleteSandbox(_ context.Context, id string) error {
	if s.err != nil {
		return s.err
	}
	delete(s.recorded, id)
	s.deleted = append(s.deleted, id)
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
	if err := store.RemoveSandbox(context.Background(), created.Summary.ID); err != nil {
		t.Fatalf("remove sandbox: %v", err)
	}
	if recorder.recorded[created.Summary.ID] != nil || len(recorder.deleted) != 1 || recorder.deleted[0] != created.Summary.ID {
		t.Fatalf("deleted records = %#v, remaining = %#v", recorder.deleted, recorder.recorded)
	}
}

func TestRemoveSandboxRetriesRecordDeletionAfterDirectoryRemoval(t *testing.T) {
	recorder := &sandboxRecorderStub{recorded: make(map[string]*domain.Sandbox)}
	store, err := NewWithConfigAndRecorder(sandboxRecordTestConfig(t), recorder)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	created, err := store.CreateSandbox(context.Background(), "retry record deletion", "", driverpkg.RuntimeDriverBoxlite, "", "", "api", nil, nil, nil)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	recorder.err = fmt.Errorf("database unavailable")
	err = store.RemoveSandbox(context.Background(), created.Summary.ID)
	if err == nil {
		t.Fatal("remove sandbox returned nil error when record deletion failed")
	}
	if _, statErr := os.Stat(store.SandboxDir(created.Summary.ID)); !os.IsNotExist(statErr) {
		t.Fatalf("sandbox directory stat error = %v, want not exist", statErr)
	}
	if recorder.recorded[created.Summary.ID] == nil {
		t.Fatal("sandbox record was deleted despite recorder failure")
	}

	recorder.err = nil
	if err := store.RemoveSandbox(context.Background(), created.Summary.ID); err != nil {
		t.Fatalf("retry remove sandbox: %v", err)
	}
	if recorder.recorded[created.Summary.ID] != nil {
		t.Fatal("sandbox record remains after retry")
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

func TestMigrateSandboxRecordsContinuesAfterUpsertFailure(t *testing.T) {
	config := sandboxRecordTestConfig(t)
	filesystemStore, err := NewWithConfig(config)
	if err != nil {
		t.Fatalf("create filesystem store: %v", err)
	}
	first, err := filesystemStore.CreateSandbox(context.Background(), "first", "", driverpkg.RuntimeDriverBoxlite, "", "", "api", nil, nil, nil)
	if err != nil {
		t.Fatalf("create first sandbox: %v", err)
	}
	second, err := filesystemStore.CreateSandbox(context.Background(), "second", "", driverpkg.RuntimeDriverBoxlite, "", "", "api", nil, nil, nil)
	if err != nil {
		t.Fatalf("create second sandbox: %v", err)
	}
	recorder := &sandboxRecorderStub{
		recorded:     make(map[string]*domain.Sandbox),
		upsertErrors: map[string]error{first.Summary.ID: fmt.Errorf("invalid record")},
	}
	if _, err := NewWithConfigAndRecorder(config, recorder); err != nil {
		t.Fatalf("migration blocked store initialization: %v", err)
	}
	if recorder.recorded[second.Summary.ID] == nil {
		t.Fatalf("second sandbox %q was not migrated after first failure", second.Summary.ID)
	}
}

func TestCreateSandboxSucceedsWhenRecorderFails(t *testing.T) {
	recorder := &sandboxRecorderStub{recorded: make(map[string]*domain.Sandbox), err: fmt.Errorf("database unavailable")}
	store, err := NewWithConfigAndRecorder(sandboxRecordTestConfig(t), recorder)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	created, err := store.CreateSandbox(context.Background(), "record failure", "", driverpkg.RuntimeDriverBoxlite, "", "", "api", nil, nil, nil)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := store.GetSandbox(context.Background(), created.Summary.ID); err != nil {
		t.Fatalf("load filesystem sandbox after recorder failure: %v", err)
	}
	if recorder.recorded[created.Summary.ID] != nil {
		t.Fatal("failed recorder unexpectedly stored sandbox")
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
