package cache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-compose/pkg/imagecache"
)

type MaterializedRemover struct {
	Cache        *imagecache.Cache
	Dependencies MaterializedDependencyProvider
}

func (r MaterializedRemover) Remove(ctx context.Context, item Item) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.Cache == nil {
		return fmt.Errorf("runtime cache materialized remover requires image cache")
	}
	if err := validateMaterializedRemoveItem(item); err != nil {
		return err
	}
	expectedID, err := GenerateCacheID(item)
	if err != nil {
		return err
	}
	if item.CacheID != expectedID {
		return fmt.Errorf("%w: cache id does not match inventory item", ErrInvalidCacheID)
	}
	unlock, err := r.Cache.LockContext(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if item.Kind == KindMicrosandboxBaseDisk && r.Dependencies != nil {
		dependencies, _, err := r.Dependencies.MaterializedDependencies(ctx)
		if err != nil {
			return fmt.Errorf("load microsandbox base disk references: %w", err)
		}
		var references int
		for _, dependency := range dependencies {
			// Unresolved ownership metadata cannot name the disk it protects,
			// so it cannot be proven not to be this one. Deleting a base disk
			// still backing a live overlay is unrecoverable; refusing to delete
			// one that is merely hard to account for is not.
			if dependency.Unresolved {
				return fmt.Errorf("microsandbox base disk %s cannot be removed: %s", item.Path, dependency.Reason)
			}
			if filepath.Clean(strings.TrimSpace(dependency.Path)) == filepath.Clean(item.Path) {
				references++
			}
		}
		if references > 0 {
			return fmt.Errorf("microsandbox base disk %s is referenced by %d sandbox(es)", item.Path, references)
		}
	}

	return r.removeLocked(ctx, item)
}

func (r MaterializedRemover) removeLocked(ctx context.Context, item Item) error {
	safe, err := ValidateCachePath(r.Cache.MaterializationRoot(), item.Path)
	if err != nil {
		return err
	}
	paths := []string{safe.CanonicalTarget}
	switch item.Kind {
	case KindMaterializedOCILayout:
		paths = append(paths, filepath.Join(safe.CanonicalParent, materializedOCIReadyName))
	case KindMaterializedRootFS:
		paths = append(paths, filepath.Join(safe.CanonicalParent, materializedRootFSReadyName))
	case KindMicrosandboxBaseDisk:
		paths = append(paths, strings.TrimSuffix(safe.CanonicalTarget, ".qcow2")+".json")
	case KindMaterializedTempDir:
	default:
		return fmt.Errorf("unsupported materialized cache kind %q", item.Kind)
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func validateMaterializedRemoveItem(item Item) error {
	if item.Domain != DomainMaterializedImageCache {
		return fmt.Errorf("materialized remover cannot remove domain %q", item.Domain)
	}
	if item.CacheID == "" {
		return fmt.Errorf("%w: cache id is required", ErrInvalidCacheID)
	}
	if _, err := ParseCacheID(item.CacheID); err != nil {
		return err
	}
	return nil
}
