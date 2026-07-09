package volumes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	domain "agent-compose/pkg/model"
)

type fakeStore struct {
	items       map[string]domain.VolumeRecord
	createErr   error
	removeErr   error
	refs        []domain.VolumeReference
	removedKeys []string
}

func (s *fakeStore) CreateVolume(_ context.Context, item domain.VolumeRecord) (domain.VolumeRecord, error) {
	if s.createErr != nil {
		return domain.VolumeRecord{}, s.createErr
	}
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
	if s.removeErr != nil {
		return s.removeErr
	}
	if len(s.refs) > 0 {
		return domain.ResourceError(domain.ErrReferenced, "volume", key, "referenced", nil)
	}
	s.removedKeys = append(s.removedKeys, key)
	item := s.items[key]
	delete(s.items, item.ID)
	delete(s.items, item.Name)
	return nil
}

func (s *fakeStore) DeleteVolume(_ context.Context, key string) error {
	if s.removeErr != nil {
		return s.removeErr
	}
	s.removedKeys = append(s.removedKeys, key)
	item := s.items[key]
	delete(s.items, item.ID)
	delete(s.items, item.Name)
	return nil
}

func (s *fakeStore) FindVolumeConfigReferences(context.Context, string) ([]domain.VolumeReference, error) {
	return append([]domain.VolumeReference(nil), s.refs...), nil
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
	if _, err := uuid.Parse(created.ID); err != nil || strings.Contains(created.ID, "cache") {
		t.Fatalf("volume internal id = %q, want opaque UUID not derived from name", created.ID)
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
	if !strings.HasPrefix(mounts[0].ID, "mount-") || len(mounts[0].ID) != len("mount-")+24 || strings.Contains(mounts[0].ID, "fixtures") {
		t.Fatalf("bind mount id = %q, want opaque stable hash id", mounts[0].ID)
	}
	if mounts[1].VolumeID != created.ID || mounts[1].Target != "/cache" {
		t.Fatalf("volume mount = %#v", mounts[1])
	}
	if !strings.HasPrefix(mounts[1].ID, "mount-") || len(mounts[1].ID) != len("mount-")+24 || strings.Contains(mounts[1].ID, "cache") {
		t.Fatalf("volume mount id = %q, want opaque stable hash id", mounts[1].ID)
	}
	if _, err := os.Stat(mounts[1].HostPath); err != nil {
		t.Fatalf("volume host path missing: %v", err)
	}
}

func TestManagerResolveNamedVolumeMultipleTargetsNestedAndReadOnly(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	manager := NewManager(store, LocalDriver{DataRoot: t.TempDir()})
	shared, err := manager.Create(ctx, domain.VolumeRecord{Name: "shared-cache"})
	if err != nil {
		t.Fatalf("Create shared-cache: %v", err)
	}
	nested, err := manager.Create(ctx, domain.VolumeRecord{Name: "nested-cache"})
	if err != nil {
		t.Fatalf("Create nested-cache: %v", err)
	}
	readonly, err := manager.Create(ctx, domain.VolumeRecord{Name: "readonly-cache"})
	if err != nil {
		t.Fatalf("Create readonly-cache: %v", err)
	}
	mounts, warnings, err := manager.ResolveMounts(ctx, []domain.VolumeMountSpec{
		{Source: "shared-cache", Target: "/mnt/shared-a"},
		{Source: "shared-cache", Target: "/mnt/shared-b"},
		{Source: "nested-cache", Target: "/mnt/nested/parent/child"},
		{Source: "readonly-cache", Target: "/mnt/readonly", ReadOnly: true},
	}, ResolveOptions{})
	if err != nil {
		t.Fatalf("ResolveMounts: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	got := map[string]domain.SandboxVolumeMount{}
	for _, mount := range mounts {
		got[mount.Target] = mount
		if !strings.HasPrefix(mount.ID, "mount-") || len(mount.ID) != len("mount-")+24 {
			t.Fatalf("mount id = %q, want stable hash id", mount.ID)
		}
	}
	if len(got) != 4 {
		t.Fatalf("mount targets = %#v, want 4 distinct targets", got)
	}
	if got["/mnt/shared-a"].VolumeID != shared.ID || got["/mnt/shared-b"].VolumeID != shared.ID {
		t.Fatalf("shared mounts = %#v %#v, want same volume id %s", got["/mnt/shared-a"], got["/mnt/shared-b"], shared.ID)
	}
	if got["/mnt/shared-a"].HostPath != got["/mnt/shared-b"].HostPath {
		t.Fatalf("shared host paths differ: %q vs %q", got["/mnt/shared-a"].HostPath, got["/mnt/shared-b"].HostPath)
	}
	if got["/mnt/shared-a"].ID == got["/mnt/shared-b"].ID {
		t.Fatalf("same source with different targets should have different mount ids: %#v %#v", got["/mnt/shared-a"], got["/mnt/shared-b"])
	}
	if got["/mnt/nested/parent/child"].VolumeID != nested.ID {
		t.Fatalf("nested mount = %#v, want volume id %s", got["/mnt/nested/parent/child"], nested.ID)
	}
	if got["/mnt/readonly"].VolumeID != readonly.ID || !got["/mnt/readonly"].ReadOnly {
		t.Fatalf("readonly mount = %#v, want readonly volume id %s", got["/mnt/readonly"], readonly.ID)
	}
}

func TestManagerResolveProjectVolumeMappingTakesPrecedence(t *testing.T) {
	ctx := context.Background()
	dataRoot := t.TempDir()
	globalStore := &fakeStore{}
	manager := NewManager(globalStore, LocalDriver{DataRoot: dataRoot})
	global, err := manager.Create(ctx, domain.VolumeRecord{Name: "cache"})
	if err != nil {
		t.Fatalf("Create global cache: %v", err)
	}
	projectPath := filepath.Join(dataRoot, "project-cache")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project path: %v", err)
	}
	project := domain.VolumeRecord{
		ID:        "project-volume-id",
		Name:      "project_cache",
		Driver:    domain.VolumeDriverLocal,
		Path:      projectPath,
		ProjectID: "project-1",
	}
	mounts, warnings, err := manager.ResolveMounts(ctx, []domain.VolumeMountSpec{
		{Source: "cache", Target: "/cache"},
	}, ResolveOptions{ProjectVolumes: map[string]domain.VolumeRecord{"cache": project}})
	if err != nil {
		t.Fatalf("ResolveMounts: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	if len(mounts) != 1 {
		t.Fatalf("mounts = %#v, want one", mounts)
	}
	if mounts[0].VolumeID != project.ID || mounts[0].HostPath != projectPath {
		t.Fatalf("mount = %#v, want project volume %s path %s; global was %s", mounts[0], project.ID, projectPath, global.ID)
	}
}

func TestManagerResolveMountsReportsWarningsAndMissingVolumes(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	manager := NewManager(store, LocalDriver{DataRoot: t.TempDir()})
	created, err := manager.Create(ctx, domain.VolumeRecord{Name: "cache"})
	if err != nil {
		t.Fatalf("Create cache: %v", err)
	}
	mounts, warnings, err := manager.ResolveMounts(ctx, []domain.VolumeMountSpec{
		{Source: "cache", Target: "/data/cache"},
		{Source: "cache", Target: "/root/.cache"},
	}, ResolveOptions{})
	if err != nil {
		t.Fatalf("ResolveMounts warnings case: %v", err)
	}
	if len(mounts) != 2 || mounts[0].VolumeID != created.ID || mounts[1].VolumeID != created.ID {
		t.Fatalf("mounts = %#v", mounts)
	}
	if len(warnings) != 2 ||
		!strings.Contains(warnings[0], "/data/cache") ||
		!strings.Contains(warnings[1], "/root/.cache") {
		t.Fatalf("warnings = %#v, want reserved target warnings", warnings)
	}
	if _, _, err := manager.ResolveMounts(ctx, []domain.VolumeMountSpec{
		{Source: "missing-cache", Target: "/cache"},
	}, ResolveOptions{}); err == nil {
		t.Fatal("ResolveMounts missing volume returned nil error")
	}
}

func TestBindResolverResolvesRelativeAbsoluteAndSymlinkDirectories(t *testing.T) {
	root := t.TempDir()
	relativeDir := filepath.Join(root, "relative")
	absoluteDir := filepath.Join(root, "absolute")
	if err := os.MkdirAll(relativeDir, 0o755); err != nil {
		t.Fatalf("mkdir relative dir: %v", err)
	}
	if err := os.MkdirAll(absoluteDir, 0o755); err != nil {
		t.Fatalf("mkdir absolute dir: %v", err)
	}
	linkPath := filepath.Join(root, "link")
	if err := os.Symlink(relativeDir, linkPath); err != nil {
		t.Fatalf("symlink relative dir: %v", err)
	}
	resolver := BindResolver{ProjectRoot: root}
	if got, err := resolver.Resolve("./relative"); err != nil || got != relativeDir {
		t.Fatalf("Resolve relative = %q err=%v, want %q", got, err, relativeDir)
	}
	if got, err := resolver.Resolve(absoluteDir); err != nil || got != absoluteDir {
		t.Fatalf("Resolve absolute = %q err=%v, want %q", got, err, absoluteDir)
	}
	if got, err := resolver.Resolve("./link"); err != nil || got != relativeDir {
		t.Fatalf("Resolve symlink = %q err=%v, want evaluated %q", got, err, relativeDir)
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

func TestManagerCreateCleansManagedPathWhenStoreCreateFails(t *testing.T) {
	ctx := context.Background()
	dataRoot := t.TempDir()
	store := &fakeStore{createErr: fmt.Errorf("store unavailable")}
	manager := NewManager(store, LocalDriver{DataRoot: dataRoot})
	_, err := manager.Create(ctx, domain.VolumeRecord{Name: "cache"})
	if err == nil {
		t.Fatal("Create returned nil error")
	}
	managedRoot := filepath.Join(dataRoot, "volumes", domain.VolumeDriverLocal)
	entries, err := os.ReadDir(managedRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read managed root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("managed root entries after failed create = %#v, want none", entries)
	}
}

func TestManagerRemoveKeepsStoreRecordWhenDriverRemoveFails(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	record, err := store.CreateVolume(ctx, domain.VolumeRecord{ID: "vol-cache", Name: "cache", Driver: domain.VolumeDriverLocal, Path: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	driverErr := fmt.Errorf("driver remove failed")
	manager := NewManager(store, fakeDriver{name: domain.VolumeDriverLocal, removeErr: driverErr})
	err = manager.Remove(ctx, record.Name, false)
	if !errors.Is(err, driverErr) {
		t.Fatalf("Remove err = %v, want %v", err, driverErr)
	}
	if len(store.removedKeys) != 0 {
		t.Fatalf("store removed keys = %#v, want none", store.removedKeys)
	}
	if loaded, err := store.GetVolume(ctx, record.ID); err != nil || loaded.ID != record.ID {
		t.Fatalf("volume record after failed remove = %#v err=%v", loaded, err)
	}
}

func TestManagerRemoveRejectsActiveSandboxVolumeReferences(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	record, err := store.CreateVolume(ctx, domain.VolumeRecord{ID: "vol-cache", Name: "cache", Driver: domain.VolumeDriverLocal, Path: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	manager := NewManager(store, LocalDriver{DataRoot: t.TempDir()})
	manager.Sandboxes = &fakeSessionStore{sessions: []*domain.Sandbox{{
		Summary: domain.SandboxSummary{ID: "session-1", Title: "using cache"},
		VolumeMounts: []domain.SandboxVolumeMount{{
			ID:       "mount-cache",
			Type:     domain.VolumeMountTypeVolume,
			Source:   "cache",
			Target:   "/cache",
			VolumeID: record.ID,
			HostPath: record.Path,
		}},
	}}}
	if err := manager.Remove(ctx, record.Name, true); !errors.Is(err, domain.ErrReferenced) {
		t.Fatalf("Remove active sandbox volume err = %v, want ErrReferenced", err)
	}
	if len(store.removedKeys) != 0 {
		t.Fatalf("store removed keys = %#v, want none", store.removedKeys)
	}
	pruned, err := manager.Prune(ctx, domain.VolumeListOptions{}, true)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned.Skipped) != 1 || pruned.Skipped[0].ID != record.ID || len(pruned.Removed) != 0 {
		t.Fatalf("prune result = %#v, want skipped active sandbox volume", pruned)
	}
}

func TestManagerRemoveForceSkipsConfigReferencesButNotSandboxReferences(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		refs: []domain.VolumeReference{{ResourceType: "project_volume", ResourceID: "project-1", Name: "cache"}},
	}
	record, err := store.CreateVolume(ctx, domain.VolumeRecord{ID: "vol-cache", Name: "cache", Driver: domain.VolumeDriverLocal, Path: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	manager := NewManager(store, LocalDriver{DataRoot: t.TempDir()})
	if err := manager.Remove(ctx, record.Name, false); !errors.Is(err, domain.ErrReferenced) {
		t.Fatalf("Remove without force err = %v, want ErrReferenced", err)
	}
	if len(store.removedKeys) != 0 {
		t.Fatalf("store removed keys after non-force remove = %#v, want none", store.removedKeys)
	}
	if err := manager.Remove(ctx, record.Name, true); err != nil {
		t.Fatalf("Remove with force returned error: %v", err)
	}
	if len(store.removedKeys) != 1 || store.removedKeys[0] != record.ID {
		t.Fatalf("store removed keys after force remove = %#v, want %s", store.removedKeys, record.ID)
	}
}

func TestManagerRemoveForceBypassesStoreConfigReferenceRecheck(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{
		refs: []domain.VolumeReference{{ResourceType: "project_volume", ResourceID: "project-1", Name: "cache"}},
	}
	record, err := store.CreateVolume(ctx, domain.VolumeRecord{ID: "vol-cache", Name: "cache", Driver: domain.VolumeDriverLocal, Path: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	manager := NewManager(store, LocalDriver{DataRoot: t.TempDir()})
	if err := manager.Remove(ctx, record.Name, true); err != nil {
		t.Fatalf("Remove with force returned error: %v", err)
	}
	if len(store.removedKeys) != 1 || store.removedKeys[0] != record.ID {
		t.Fatalf("store removed keys after force remove = %#v, want %s", store.removedKeys, record.ID)
	}
}

func TestManagerFindSandboxReferencesUsesPagination(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{}
	record, err := store.CreateVolume(ctx, domain.VolumeRecord{ID: "vol-cache", Name: "cache", Driver: domain.VolumeDriverLocal, Path: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	sessions := make([]*domain.Sandbox, 0, 501)
	for i := range 501 {
		session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: fmt.Sprintf("session-%d", i)}}
		if i == 500 {
			session.VolumeMounts = []domain.SandboxVolumeMount{{VolumeID: record.ID, Target: "/cache"}}
		}
		sessions = append(sessions, session)
	}
	sessionStore := &fakeSessionStore{sessions: sessions}
	manager := NewManager(store, LocalDriver{DataRoot: t.TempDir()})
	manager.Sandboxes = sessionStore
	if err := manager.Remove(ctx, record.Name, false); !errors.Is(err, domain.ErrReferenced) {
		t.Fatalf("Remove active sandbox volume err = %v, want ErrReferenced", err)
	}
	if len(sessionStore.options) != 2 ||
		sessionStore.options[0].Limit != 500 ||
		sessionStore.options[0].Offset != 0 ||
		sessionStore.options[1].Limit != 500 ||
		sessionStore.options[1].Offset != 500 {
		t.Fatalf("sandbox list options = %#v, want paged offsets 0 and 500", sessionStore.options)
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

type fakeDriver struct {
	name      string
	removeErr error
}

func (d fakeDriver) Name() string {
	return d.name
}

func (d fakeDriver) Create(_ context.Context, record domain.VolumeRecord) (domain.VolumeRecord, error) {
	return record, nil
}

func (d fakeDriver) Inspect(_ context.Context, record domain.VolumeRecord) (domain.VolumeRecord, error) {
	return record, nil
}

func (d fakeDriver) Remove(context.Context, domain.VolumeRecord) error {
	return d.removeErr
}

func (d fakeDriver) ResolveMountSource(_ context.Context, record domain.VolumeRecord) (string, error) {
	return record.Path, nil
}

type fakeSessionStore struct {
	sessions []*domain.Sandbox
	options  []domain.SandboxListOptions
	err      error
}

func (s *fakeSessionStore) ListSandboxes(_ context.Context, options domain.SandboxListOptions) (domain.SandboxListResult, error) {
	s.options = append(s.options, options)
	if s.err != nil {
		return domain.SandboxListResult{}, s.err
	}
	offset := options.Offset
	if offset < 0 {
		offset = 0
	}
	limit := options.Limit
	if limit <= 0 {
		limit = len(s.sessions)
	}
	if offset > len(s.sessions) {
		offset = len(s.sessions)
	}
	end := offset + limit
	if end > len(s.sessions) {
		end = len(s.sessions)
	}
	return domain.SandboxListResult{
		Sandboxes:  append([]*domain.Sandbox(nil), s.sessions[offset:end]...),
		TotalCount: len(s.sessions),
		HasMore:    end < len(s.sessions),
		NextOffset: end,
	}, nil
}
