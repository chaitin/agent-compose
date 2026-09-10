package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/cache"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestResolverWithResolvedProtectsProjectionFromPrune(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeSkill(t, source, "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}
	cacheSource := cache.SkillSource{Root: resolver.CacheRoot}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "sandbox", "workspace")}}
	consuming := make(chan cache.Item, 1)
	release := make(chan struct{})
	finished := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		finished <- resolver.WithResolved(ctx, []domain.AgentSkill{{Name: "pdf", Provider: "file", Path: source}}, func(resolved []ResolvedSkill) error {
			listed, err := cacheSource.List(ctx)
			if err != nil {
				return err
			}
			var item cache.Item
			for _, candidate := range listed.Items {
				if candidate.Kind == cache.KindSkillArtifact {
					item = candidate
				}
			}
			consuming <- item
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			_, err = execution.WriteAgentSkills(ctx, nil, session, resolver.Projected(resolved), nil)
			return err
		})
	}()
	var item cache.Item
	select {
	case item = <-consuming:
	case err := <-finished:
		t.Fatalf("resolution stopped before consuming: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if item.CacheID == "" {
		close(release)
		t.Fatal("resolved artifact missing from cache inventory")
	}
	lock, err := os.OpenFile(filepath.Join(resolver.CacheRoot, cacheRootLockName), os.O_RDWR, 0o600)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		close(release)
		t.Fatalf("cache prune could take exclusive ownership during projection: %v", err)
	}
	removed := make(chan error, 1)
	go func() { removed <- cacheSource.Remove(ctx, item) }()
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("private projection failed while prune was waiting: %v", err)
	}
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(item.Path); !os.IsNotExist(err) {
		t.Fatalf("released cache artifact was not pruned: %v", err)
	}
	actual, err := execution.FingerprintAgentSkill(ctx, filepath.Join(execution.HostAgentSkillsDir(session), "pdf"))
	want, sourceErr := execution.FingerprintAgentSkill(ctx, source)
	if err != nil || sourceErr != nil || actual != want {
		t.Fatalf("private tree failed after cache prune: actual=%s want=%s errors=%v/%v", actual, want, err, sourceErr)
	}
}

func TestResolverLeaseReleasesOnConsumerErrorAndHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeSkill(t, source, "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}
	specs := []domain.AgentSkill{{Name: "pdf", Provider: "file", Path: source}}
	sentinel := errors.New("consumer failed")
	if err := resolver.WithResolved(context.Background(), specs, func([]ResolvedSkill) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("consumer failure = %v", err)
	}
	unlock, err := lockSkillCacheFile(context.Background(), filepath.Join(resolver.CacheRoot, cacheRootLockName), syscall.LOCK_EX)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lockSkillCacheFile(ctx, filepath.Join(resolver.CacheRoot, cacheRootLockName), syscall.LOCK_SH); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cache lease wait = %v", err)
	}
}

func TestResolverFingerprintCacheSeparatesMetadataAndExecutableIdentity(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeSkill(t, source, "pdf")
	resolver := Resolver{CacheRoot: filepath.Join(root, "cache"), LocalSourceRoots: []string{root}}
	specs := []domain.AgentSkill{{Name: "pdf", Provider: "file", Path: source}}
	first, err := resolver.Resolve(context.Background(), specs)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolver.Resolve(context.Background(), specs)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("metadata touch changed skill identity: first=%+v second=%+v error=%v", first, second, err)
	}
	for _, name := range []string{artifactManifestName, ".ready"} {
		if _, err := os.Stat(filepath.Join(first[0].LocalDir, name)); !os.IsNotExist(err) {
			t.Fatalf("cache bookkeeping %s leaked into delivered content: %v", name, err)
		}
	}
	if first[0].Fingerprint == "" || resolver.Projected(first)[0].Fingerprint != first[0].Fingerprint {
		t.Fatal("resolved fingerprint was lost at projection boundary")
	}
	for _, path := range []string{filepath.Join(source, "SKILL.md"), source} {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
		changed, err := resolver.Resolve(context.Background(), specs)
		if err != nil || changed[0].Fingerprint == second[0].Fingerprint || changed[0].LocalDir == second[0].LocalDir {
			t.Fatalf("executable permission change reused stale cache: %+v error=%v", changed, err)
		}
		second = changed
	}
	if reflect.TypeFor[ResolvedSkill]().NumField() != 3 {
		t.Fatal("new resolver fields require explicit projection contract coverage")
	}
}

func TestResolverCacheCancellationAndReadOnlyPartialCleanup(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "artifact")
	ctx, cancel := context.WithCancel(context.Background())
	err := ensureCachedDir(ctx, dst, func(tmp string) error {
		path := filepath.Join(tmp, "content")
		writeSkill(t, path, "pdf")
		if err := os.Chmod(path, 0o500); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cache fill = %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "artifact.lock" {
		t.Fatalf("canceled cache fill retained partial read-only content: %v", entries)
	}
	unlock, err := lockSkillCacheFile(context.Background(), dst+".lock", syscall.LOCK_EX)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := ensureCachedDir(ctx, dst, func(string) error { t.Fatal("canceled fill executed"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled per-artifact lock wait = %v", err)
	}
}
