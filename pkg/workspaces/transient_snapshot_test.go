package workspaces

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func newTransientSnapshotFixture(t *testing.T, config *appconfig.Config) (domain.WorkspaceConfig, *domain.SandboxWorkspace, string) {
	t.Helper()
	workspace, err := CreateTransientFileSnapshot(context.Background(), config, domain.WorkspaceConfig{ID: "logical-workspace", Name: "Workspace", Type: "file", ConfigJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := FileWorkspaceContentRoot(config, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.SnapshotLease.Close() })
	return workspace, &domain.SandboxWorkspace{ID: workspace.ID, Name: workspace.Name, Type: workspace.Type, ConfigJSON: workspace.ConfigJSON, SnapshotID: workspace.SnapshotID, SnapshotLease: workspace.SnapshotLease}, root
}

type transientSnapshotReader struct {
	sandbox *domain.Sandbox
	err     error
}

func (s transientSnapshotReader) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.sandbox == nil {
		return nil, os.ErrNotExist
	}
	return s.sandbox, nil
}

func TestTransientSnapshotOwnershipLifecycle(t *testing.T) {
	for _, status := range []string{"unused", "pending", "failed", "ready", "legacy", "unknown", "read-error", "removed"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			config := &appconfig.Config{DataRoot: t.TempDir()}
			_, workspace, root := newTransientSnapshotFixture(t, config)
			reader := transientSnapshotReader{}
			retained := status == "pending" || status == "failed" || status == "legacy" || status == "unknown" || status == "read-error"
			if status != "unused" {
				if err := ClaimTransientSnapshot(ctx, config, workspace, "sandbox-owner"); err != nil {
					t.Fatal(err)
				}
				reader.sandbox = &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-owner"}, Workspace: workspace, WorkspaceProvisioning: &domain.SandboxWorkspaceProvisioning{Version: domain.SandboxWorkspaceProvisioningVersion, Status: status, UpdatedAt: time.Now()}}
				if status == "legacy" {
					reader.sandbox.WorkspaceProvisioning = nil
				}
				if status == "read-error" {
					reader.err = os.ErrPermission
				}
				if status == "removed" {
					reader.sandbox = nil
				}
			}
			err := ReleaseUnusedTransientSnapshot(ctx, config, reader, workspace)
			if status == "read-error" {
				if !errors.Is(err, os.ErrPermission) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(root)
			if retained && err != nil {
				t.Fatalf("retry source removed: %v", err)
			}
			if !retained && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unused source retained: %v", err)
			}
			// Deletion owns even failed/pending sources, and repeated cleanup is safe.
			if err := ReleaseTransientSnapshot(ctx, config, workspace); err != nil {
				t.Fatal(err)
			}
			if err := ReleaseTransientSnapshot(ctx, config, workspace); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTransientSnapshotCleanerRecoversOnlyOwnedOldOrReleasedGenerations(t *testing.T) {
	ctx := context.Background()
	config := &appconfig.Config{DataRoot: t.TempDir()}
	_, old, oldRoot := newTransientSnapshotFixture(t, config)
	_, pending, pendingRoot := newTransientSnapshotFixture(t, config)
	if err := ClaimTransientSnapshot(ctx, config, pending, "unindexed-sandbox"); err != nil {
		t.Fatal(err)
	}
	_, released, releasedRoot := newTransientSnapshotFixture(t, config)
	_, active, activeRoot := newTransientSnapshotFixture(t, config)
	root, err := openTransientSnapshotRoot(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	owner, err := readTransientSnapshotOwner(root, released.SnapshotID, released.ID)
	if err != nil {
		t.Fatal(err)
	}
	owner.Released = true
	if err := writeTransientSnapshotOwner(root, owner); err != nil {
		t.Fatal(err)
	}
	preset := filepath.Join(config.DataRoot, "workspaces", old.ID, "content", "keep.txt")
	writeProvisionerFileStateFile(t, preset, "preset", 0o600)
	legacy := filepath.Join(config.DataRoot, transientSnapshotDirectory, "unmarked", "keep.txt")
	writeProvisionerFileStateFile(t, legacy, "legacy", 0o600)
	for _, item := range []*domain.SandboxWorkspace{old, pending, released} {
		if err := CloseTransientSnapshotLease(item); err != nil {
			t.Fatal(err)
		}
	}
	reader := transientSnapshotReader{sandbox: &domain.Sandbox{Workspace: pending}}
	cleaner := &TransientSnapshotCleaner{Config: config, Store: reader}
	result, err := cleaner.Clean(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 2 || result.Skipped != 2 {
		t.Fatalf("cleanup result = %#v", result)
	}
	for _, path := range []string{oldRoot, releasedRoot} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old generation %s retained: %v", path, err)
		}
	}
	for _, path := range []string{activeRoot, pendingRoot, preset, legacy} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("live/preset content removed: %s: %v", path, err)
		}
	}
	if err := ReleaseUnusedTransientSnapshot(ctx, config, nil, active); err != nil {
		t.Fatal(err)
	}
}

func TestTransientSnapshotClaimIsAtomicAndExclusive(t *testing.T) {
	config := &appconfig.Config{DataRoot: t.TempDir()}
	_, workspace, _ := newTransientSnapshotFixture(t, config)
	const count = 12
	results := make(chan error, count)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(count)
	for i := 0; i < count; i++ {
		go func() {
			ready.Done()
			<-start
			results <- ClaimTransientSnapshot(context.Background(), config, workspace, time.Now().String())
		}()
	}
	ready.Wait()
	close(start)
	success := 0
	for i := 0; i < count; i++ {
		if <-results == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful consumers = %d", success)
	}
}

func TestTransientSnapshotRejectsUnownedPathsAndMissingContent(t *testing.T) {
	ctx := context.Background()
	config := &appconfig.Config{DataRoot: t.TempDir()}
	cfg, workspace, root := newTransientSnapshotFixture(t, config)
	foreign := *workspace
	foreign.ID = "another-workspace"
	if err := ReleaseTransientSnapshot(ctx, config, &foreign); err == nil {
		t.Fatal("accepted mismatched owner")
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileWorkspaceContent(config, cfg); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing generation silently recreated: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	generation := filepath.Dir(root)
	if err := os.RemoveAll(generation); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, generation); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseTransientSnapshot(ctx, config, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("release followed external symlink: %v", err)
	}
	workspace.SnapshotID = "../workspaces"
	if err := ReleaseTransientSnapshot(ctx, config, workspace); err == nil {
		t.Fatal("accepted traversal")
	}
}

func TestWorkspaceSnapshotFieldsHaveExplicitHashAndPersistenceContract(t *testing.T) {
	expected := []string{"ID", "Name", "Type", "ConfigJSON", "SnapshotID", "SnapshotLease"}
	typ := reflect.TypeFor[domain.SandboxWorkspace]()
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	if !reflect.DeepEqual(fields, expected) {
		t.Fatalf("review every new workspace field's hash/persistence contract: %v", fields)
	}
	workspace := &domain.SandboxWorkspace{ID: "logical", Name: "name", Type: "file", ConfigJSON: "{}", SnapshotID: "generation"}
	filtered := WorkspaceForConfigurationHash(workspace)
	if filtered.SnapshotID != "" || workspace.SnapshotID != "generation" {
		t.Fatal("generation hash filter mutated input")
	}
	filtered.SnapshotID = workspace.SnapshotID
	if !reflect.DeepEqual(filtered, workspace) {
		t.Fatal("hash filter lost semantic fields")
	}
	payload, err := json.Marshal(workspace)
	if err != nil {
		t.Fatal(err)
	}
	var persisted domain.SandboxWorkspace
	if err := json.Unmarshal(payload, &persisted); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&persisted, workspace) {
		t.Fatal("snapshot ownership does not survive persistence")
	}
	var config domain.WorkspaceConfig
	if err := json.Unmarshal([]byte(`{"snapshot_id":"external"}`), &config); err != nil {
		t.Fatal(err)
	}
	if config.SnapshotID != "" {
		t.Fatal("accepted external snapshot ownership")
	}
	config.SnapshotID = "internal"
	payload, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["snapshot_id"]; ok {
		t.Fatal("exposed internal snapshot generation")
	}
}
