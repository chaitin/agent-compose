package volumes_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/volumes"
)

func TestIntegrationManagerPersistsProjectVolumeLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	store := configstore.FromDB(db)
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

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
	if err := manager.UpsertProjectVolume(ctx, "project-1", "cache", projectVolume.ID, false); err != nil {
		t.Fatalf("UpsertProjectVolume: %v", err)
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
