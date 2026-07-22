package volumes_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
	storagesqlite "agent-compose/pkg/storage/sqlite"
	"agent-compose/pkg/volumes"
)

func TestIntegrationManagerPersistsProjectVolumeLifecycle(t *testing.T) {
	ctx := context.Background()
	database, err := storagesqlite.Open(":memory:", 0)
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	defer func() { _ = database.Close() }()

	store := configstore.FromDB(database.DB())

	dataRoot := t.TempDir()
	manager := volumes.NewManager(store, volumes.LocalDriver{DataRoot: dataRoot})
	global, created, err := manager.Ensure(ctx, domain.VolumeRecord{
		Name:   "cache",
		Labels: map[string]string{"scope": "global"},
	})
	if err != nil {
		t.Fatalf("Ensure global cache: %v", err)
	}
	if !created {
		t.Fatal("Ensure global cache created = false")
	}
	if !strings.HasPrefix(global.Path, filepath.Join(dataRoot, "volumes", domain.VolumeDriverLocal)) {
		t.Fatalf("global volume path = %q, want under data root %q", global.Path, dataRoot)
	}
	if _, err := os.Stat(global.Path); err != nil {
		t.Fatalf("global volume path missing: %v", err)
	}
	reused, created, err := manager.Ensure(ctx, domain.VolumeRecord{Name: "cache"})
	if err != nil {
		t.Fatalf("Ensure existing cache: %v", err)
	}
	if created || reused.ID != global.ID {
		t.Fatalf("Ensure existing cache = %#v created=%v, want reused %s", reused, created, global.ID)
	}

	projectVolume, err := manager.Create(ctx, domain.VolumeRecord{
		Name:      "project_cache",
		ProjectID: "project-1",
		Labels:    map[string]string{"scope": "project"},
	})
	if err != nil {
		t.Fatalf("Create project volume: %v", err)
	}
	if err := manager.ReplaceProjectVolumes(ctx, "project-1", map[string]domain.ProjectVolumeLink{
		"cache": {VolumeID: projectVolume.ID, External: false},
	}); err != nil {
		t.Fatalf("ReplaceProjectVolumes: %v", err)
	}
	projectVolumes, err := store.ListProjectVolumes(ctx, "project-1")
	if err != nil {
		t.Fatalf("ListProjectVolumes: %v", err)
	}
	mounts, warnings, err := manager.ResolveMounts(ctx, []domain.VolumeMountSpec{
		{Source: "cache", Target: "/cache"},
	}, volumes.ResolveOptions{ProjectVolumes: projectVolumes})
	if err != nil {
		t.Fatalf("ResolveMounts project cache: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	if len(mounts) != 1 || mounts[0].VolumeID != projectVolume.ID || mounts[0].HostPath != projectVolume.Path {
		t.Fatalf("mounts = %#v, want project volume %s path %s", mounts, projectVolume.ID, projectVolume.Path)
	}
	if err := manager.Remove(ctx, projectVolume.Name, false); !errors.Is(err, domain.ErrReferenced) {
		t.Fatalf("Remove referenced project volume err = %v, want ErrReferenced", err)
	}
	if err := manager.RemoveProjectVolumes(ctx, "project-1"); err != nil {
		t.Fatalf("RemoveProjectVolumes: %v", err)
	}
	if err := manager.Remove(ctx, projectVolume.Name, false); err != nil {
		t.Fatalf("Remove unreferenced project volume: %v", err)
	}
	if _, err := os.Stat(projectVolume.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project volume path stat err = %v, want not exist", err)
	}
}

func TestIntegrationManagerForceRemoveDeletesProjectLinksAndData(t *testing.T) {
	ctx := context.Background()
	database, err := storagesqlite.Open(":memory:", 0)
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	defer func() { _ = database.Close() }()

	store := configstore.FromDB(database.DB())

	manager := volumes.NewManager(store, volumes.LocalDriver{DataRoot: t.TempDir()})
	volume, err := manager.Create(ctx, domain.VolumeRecord{Name: "force-remove-cache"})
	if err != nil {
		t.Fatalf("Create volume: %v", err)
	}
	if err := os.WriteFile(filepath.Join(volume.Path, "value.txt"), []byte("preserve until removal\n"), 0o644); err != nil {
		t.Fatalf("write volume data: %v", err)
	}
	if err := manager.ReplaceProjectVolumes(ctx, "project-1", map[string]domain.ProjectVolumeLink{
		"cache": {VolumeID: volume.ID},
	}); err != nil {
		t.Fatalf("ReplaceProjectVolumes: %v", err)
	}

	if err := manager.Remove(ctx, volume.Name, false); !errors.Is(err, domain.ErrReferenced) {
		t.Fatalf("Remove without force err = %v, want ErrReferenced", err)
	}
	if _, err := os.Stat(volume.Path); err != nil {
		t.Fatalf("volume path after rejected removal: %v", err)
	}

	if err := manager.Remove(ctx, volume.Name, true); err != nil {
		t.Fatalf("Remove with force: %v", err)
	}
	if _, err := store.GetVolume(ctx, volume.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetVolume after force removal err = %v, want ErrNotFound", err)
	}
	projectVolumes, err := store.ListProjectVolumes(ctx, "project-1")
	if err != nil {
		t.Fatalf("ListProjectVolumes after force removal: %v", err)
	}
	if len(projectVolumes) != 0 {
		t.Fatalf("project volumes after force removal = %#v, want none", projectVolumes)
	}
	if _, err := os.Stat(volume.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("volume path stat after force removal err = %v, want not exist", err)
	}
}
