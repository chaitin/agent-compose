package runhistory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/identity"
)

// Layout discovery requires metadata.json, so its cache cannot prove that a
// sandbox's private data is absent. Check every date partition independently,
// including incomplete sandboxes with no recorded logs or artifact paths.
func requireDatedSandboxesAbsent(ctx context.Context, root string, sandboxIDs []string) error {
	if len(sandboxIDs) == 0 {
		return ctx.Err()
	}
	if strings.TrimSpace(root) == "" {
		return artifactPrecondition("sandbox root is unavailable", nil)
	}
	ids := make(map[string]bool, len(sandboxIDs))
	for _, id := range sandboxIDs {
		hash, err := identity.Hash(id)
		if err != nil {
			return artifactPrecondition("cannot verify sandbox removal", err)
		}
		ids[hash] = true
	}
	return checkSandboxDateDirectory(ctx, root, "", 0, ids)
}

func checkSandboxDateDirectory(ctx context.Context, root, relative string, depth int, ids map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(root, relative))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("verify sandbox date partition: %w", err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		next := filepath.Join(relative, entry.Name())
		layout := [3]string{"2006", "2006/01", "2006/01/02"}[depth]
		date, err := time.Parse(layout, filepath.ToSlash(next))
		if err != nil || date.Format(layout) != filepath.ToSlash(next) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return artifactPrecondition("sandbox date partition must not be a symlink", nil)
		}
		if !entry.IsDir() {
			continue
		}
		if depth < 2 {
			if err := checkSandboxDateDirectory(ctx, root, next, depth+1, ids); err != nil {
				return err
			}
			continue
		}
		for id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := requireAbsent(filepath.Join(root, next, id)); err != nil {
				return err
			}
		}
	}
	return nil
}
