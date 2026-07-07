package volumes

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	domain "agent-compose/pkg/model"
)

type fakeStore struct {
	items map[string]domain.VolumeRecord
}

func (s *fakeStore) CreateVolume(_ context.Context, item domain.VolumeRecord) (domain.VolumeRecord, error) {
	if s.items == nil {
		s.items = make(map[string]domain.VolumeRecord)
	}
	s.items[item.ID] = item
	s.items[item.Name] = item
	return item, nil
}

func (s *fakeStore) UpdateVolume(_ context.Context, item domain.VolumeRecord) (domain.VolumeRecord, error) {
	s.items[item.ID] = item
	s.items[item.Name] = item
	return item, nil
}

func (s *fakeStore) GetVolume(_ context.Context, key string) (domain.VolumeRecord, error) {
	item, ok := s.items[key]
	if !ok {
		return domain.VolumeRecord{}, domain.ResourceError(domain.ErrNotFound, "volume", key, "not found", nil)
	}
	return item, nil
}

func (s *fakeStore) GetVolumeIfExists(_ context.Context, key string) (domain.VolumeRecord, bool, error) {
	item, ok := s.items[key]
	return item, ok, nil
}

func (s *fakeStore) ListVolumes(context.Context, domain.VolumeListOptions) ([]domain.VolumeRecord, error) {
	seen := map[string]struct{}{}
	var items []domain.VolumeRecord
	for key, item := range s.items {
		if key != item.ID {
			continue
		}
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		items = append(items, item)
	}
	return items, nil
}

func (s *fakeStore) RemoveVolume(_ context.Context, key string) error {
	item := s.items[key]
	delete(s.items, item.ID)
	delete(s.items, item.Name)
	return nil
}

func (s *fakeStore) FindVolumeConfigReferences(context.Context, string) ([]domain.VolumeReference, error) {
	return nil, nil
}

func TestManagerResolveBindAndNamedVolumeMounts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	bindDir := filepath.Join(root, "fixtures")
	if err := os.MkdirAll(bindDir, 0o755); err != nil {
		t.Fatalf("mkdir bind dir: %v", err)
	}
	dataRoot := t.TempDir()
	store := &fakeStore{}
	manager := NewManager(store, LocalDriver{DataRoot: dataRoot})
	created, err := manager.Create(ctx, domain.VolumeRecord{Name: "cache"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mounts, warnings, err := manager.ResolveMounts(ctx, []domain.VolumeMountSpec{
		{Type: domain.VolumeMountTypeBind, Source: "./fixtures", Target: "/fixtures", ReadOnly: true},
		{Type: domain.VolumeMountTypeVolume, Source: "cache", Target: "/cache"},
	}, ResolveOptions{ProjectRoot: root})
	if err != nil {
		t.Fatalf("ResolveMounts: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	if len(mounts) != 2 {
		t.Fatalf("mounts = %#v", mounts)
	}
	if mounts[0].HostPath != bindDir || !mounts[0].ReadOnly {
		t.Fatalf("bind mount = %#v", mounts[0])
	}
	if mounts[1].VolumeID != created.ID || mounts[1].Target != "/cache" {
		t.Fatalf("volume mount = %#v", mounts[1])
	}
	if _, err := os.Stat(mounts[1].HostPath); err != nil {
		t.Fatalf("volume host path missing: %v", err)
	}
}

func TestManagerListAndPruneVolumes(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	manager := NewManager(store, LocalDriver{DataRoot: t.TempDir()})
	if _, err := manager.Create(ctx, domain.VolumeRecord{Name: "cache"}); err != nil {
		t.Fatalf("Create cache: %v", err)
	}
	if _, err := manager.Create(ctx, domain.VolumeRecord{Name: "state"}); err != nil {
		t.Fatalf("Create state: %v", err)
	}
	listed, err := manager.List(ctx, domain.VolumeListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed = %#v", listed)
	}
	dryRun, err := manager.Prune(ctx, domain.VolumeListOptions{}, false)
	if err != nil {
		t.Fatalf("Prune dry-run: %v", err)
	}
	if !dryRun.DryRun || len(dryRun.Matched) != 2 || len(dryRun.Removed) != 0 {
		t.Fatalf("dry-run prune = %#v", dryRun)
	}
	pruned, err := manager.Prune(ctx, domain.VolumeListOptions{}, true)
	if err != nil {
		t.Fatalf("Prune force: %v", err)
	}
	if pruned.DryRun || len(pruned.Removed) != 2 {
		t.Fatalf("force prune = %#v", pruned)
	}
	if listed, err := manager.List(ctx, domain.VolumeListOptions{}); err != nil || len(listed) != 0 {
		t.Fatalf("listed after prune = %#v err=%v", listed, err)
	}
}

func TestBindResolverRejectsMissingOrFileSource(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	resolver := BindResolver{ProjectRoot: root}
	if _, err := resolver.Resolve("./missing"); err == nil {
		t.Fatal("Resolve missing returned nil")
	}
	if _, err := resolver.Resolve("./file.txt"); err == nil {
		t.Fatal("Resolve file returned nil")
	}
}
