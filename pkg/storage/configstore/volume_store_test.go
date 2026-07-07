package configstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	domain "agent-compose/pkg/model"
)

func TestVolumeStoreCRUDAndReferences(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = db.Close() }()
	store := FromDB(db)
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	created, err := store.CreateVolume(ctx, domain.VolumeRecord{ID: "vol-1", Name: "cache", Driver: domain.VolumeDriverLocal, Path: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if created.Name != "cache" || created.Driver != domain.VolumeDriverLocal {
		t.Fatalf("created volume = %#v", created)
	}
	if _, err := store.CreateVolume(ctx, domain.VolumeRecord{ID: "vol-dup", Name: "cache", Driver: domain.VolumeDriverLocal, Path: t.TempDir()}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate CreateVolume err = %v, want ErrAlreadyExists", err)
	}
	loaded, err := store.GetVolume(ctx, "cache")
	if err != nil {
		t.Fatalf("GetVolume by name: %v", err)
	}
	if loaded.ID != created.ID {
		t.Fatalf("loaded volume = %#v, want id %s", loaded, created.ID)
	}
	listed, err := store.ListVolumes(ctx, VolumeListOptions{Query: "cac"})
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("ListVolumes = %#v", listed)
	}
	if err := store.UpsertProjectVolume(ctx, "project-1", "cache", created.ID, false); err != nil {
		t.Fatalf("UpsertProjectVolume: %v", err)
	}
	projectVolumes, err := store.ListProjectVolumes(ctx, "project-1")
	if err != nil {
		t.Fatalf("ListProjectVolumes: %v", err)
	}
	if projectVolumes["cache"].ID != created.ID {
		t.Fatalf("project volumes = %#v", projectVolumes)
	}
	refs, err := store.FindVolumeConfigReferences(ctx, created.ID)
	if err != nil {
		t.Fatalf("FindVolumeConfigReferences: %v", err)
	}
	if len(refs) != 1 || refs[0].ResourceType != "project_volume" {
		t.Fatalf("refs = %#v", refs)
	}
	if err := store.RemoveVolume(ctx, created.ID); !errors.Is(err, domain.ErrReferenced) {
		t.Fatalf("RemoveVolume referenced err = %v, want ErrReferenced", err)
	}
	if err := store.RemoveProjectVolumes(ctx, "project-1"); err != nil {
		t.Fatalf("RemoveProjectVolumes: %v", err)
	}
	refs, err = store.FindVolumeConfigReferences(ctx, created.ID)
	if err != nil {
		t.Fatalf("FindVolumeConfigReferences after RemoveProjectVolumes: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("refs after RemoveProjectVolumes = %#v", refs)
	}
}
