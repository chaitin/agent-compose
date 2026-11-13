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
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type SandboxDirs interface {
	SandboxDir(string) string
}

type FSArtifacts struct {
	DataRoot           string
	SandboxRoot        string
	SandboxArchiveRoot string
	SandboxDirs        SandboxDirs
}

func (a FSArtifacts) Remove(ctx context.Context, scope Scope) error {
	// Sandbox files also contain notebook, home, and audit state. Their own
	// removal lifecycle must finish first; never unlink cells from a retained
	// or shared sandbox just to hide a run from history.
	for _, sandboxID := range scope.SandboxIDs {
		if !identity.IsID(sandboxID) || a.SandboxDirs == nil {
			return artifactPrecondition("cannot verify sandbox removal", nil)
		}
		path := a.SandboxDirs.SandboxDir(sandboxID)
		if path == "" {
			return artifactPrecondition("sandbox directory is unavailable", nil)
		}
		if err := requireAbsent(path); err != nil {
			return err
		}
		if a.SandboxArchiveRoot != "" {
			if err := requireAbsent(filepath.Join(a.SandboxArchiveRoot, sandboxID)); err != nil {
				return err
			}
		}
	}
	if err := requireDatedSandboxesAbsent(ctx, a.SandboxRoot, scope.SandboxIDs); err != nil {
		return err
	}
	for _, run := range scope.Runs {
		for _, recorded := range []string{run.LogsPath, run.ArtifactsDir} {
			if recorded == "" {
				continue
			}
			if run.SandboxID == "" || a.SandboxDirs == nil {
				return artifactPrecondition("run artifact has no verifiable sandbox owner", nil)
			}
			if err := a.verifyRunArtifactAbsent(run.SandboxID, recorded); err != nil {
				return err
			}
		}
	}
	if scope.SchedulerRun == nil {
		return ctx.Err()
	}
	run := scope.SchedulerRun
	if !identity.IsID(run.SchedulerID) || !identity.IsID(run.ID) || strings.TrimSpace(a.DataRoot) == "" {
		return artifactPrecondition("cannot verify scheduler artifact owner", nil)
	}
	rootPath, err := filepath.Abs(a.DataRoot)
	if err != nil {
		return err
	}
	relative := filepath.Join("schedulers", run.SchedulerID, "runs", run.ID)
	target := filepath.Join(rootPath, relative)
	if run.ArtifactsDir != "" {
		recorded, err := filepath.Abs(run.ArtifactsDir)
		if err != nil || recorded != target {
			return artifactPrecondition("recorded scheduler artifacts do not match the canonical run path", err)
		}
	}
	resolvedTarget, err := resolveRecordedPath(target)
	if err != nil {
		return err
	}
	for _, path := range scope.ProtectedPaths {
		if path == "" {
			continue
		}
		protected, err := resolveRecordedPath(path)
		if err != nil {
			return fmt.Errorf("verify shared artifact path: %w", err)
		}
		if pathsOverlap(resolvedTarget, protected) {
			return domain.ClassifyError(domain.ErrReferenced, "scheduler artifacts are referenced by another run", nil)
		}
	}
	root, err := os.OpenRoot(rootPath)
	if errors.Is(err, os.ErrNotExist) {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return removeRunDirectory(ctx, root, strings.Split(relative, string(filepath.Separator)))
}

// Pin each parent by an open descriptor and verify its inode. A concurrent
// symlink swap must not redirect removal to another run, even within DataRoot.
func removeRunDirectory(ctx context.Context, root *os.Root, parts []string) error {
	entry, err := root.Lstat(parts[0])
	if errors.Is(err, os.ErrNotExist) {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if entry.Mode()&os.ModeSymlink != 0 || !entry.IsDir() {
		return artifactPrecondition("scheduler artifact path must contain only directories, not symlinks", nil)
	}
	if len(parts) == 1 {
		if err := ctx.Err(); err != nil {
			return err
		}
		return root.RemoveAll(parts[0])
	}
	child, err := root.OpenRoot(parts[0])
	if err != nil {
		return err
	}
	defer func() { _ = child.Close() }()
	opened, err := child.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(entry, opened) {
		return artifactPrecondition("scheduler artifact parent changed during removal", nil)
	}
	return removeRunDirectory(ctx, child, parts[1:])
}

func requireAbsent(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return artifactPrecondition("sandbox data or audit archive remains; complete sandbox removal before removing history", nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("verify sandbox material removal: %w", err)
	}
	return nil
}

func (a FSArtifacts) verifyRunArtifactAbsent(sandboxID, recorded string) error {
	path, err := filepath.Abs(recorded)
	if err != nil {
		return err
	}
	base, err := filepath.Abs(a.SandboxDirs.SandboxDir(sandboxID))
	if err != nil {
		return err
	}
	if a.SandboxRoot != "" {
		root, err := filepath.Abs(a.SandboxRoot)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || !filepath.IsLocal(relative) {
			return artifactPrecondition("recorded run artifact is outside the sandbox root", nil)
		}
		parts := strings.Split(relative, string(filepath.Separator))
		switch {
		case len(parts) > 1 && parts[0] == sandboxID:
			base = filepath.Join(root, sandboxID)
		case len(parts) > 4 && parts[3] == sandboxID:
			if _, err := time.Parse("2006/01/02", strings.Join(parts[:3], "/")); err != nil {
				return artifactPrecondition("invalid sandbox date partition", err)
			}
			base = filepath.Join(root, filepath.Join(parts[:4]...))
		default:
			return artifactPrecondition("recorded run artifact does not match its sandbox id", nil)
		}
	}
	relative, err := filepath.Rel(base, path)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return artifactPrecondition("recorded run artifact is outside its sandbox", nil)
	}
	// The sandbox layout cache forgets a dated path after removal. Validate
	// the recorded base independently instead of requiring today's fallback
	// flat path to match historical paths.
	if err := requireAbsent(base); err != nil {
		return err
	}
	return requireAbsent(path)
}

func artifactPrecondition(message string, cause error) error {
	return domain.ClassifyError(domain.ErrFailedPrecondition, message, cause)
}

func pathsOverlap(a, b string) bool {
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && (relative == "." || filepath.IsLocal(relative)) {
			return true
		}
	}
	return false
}

// Resolve existing ancestors too: a recorded log may not have been created yet,
// but a symlinked parent can still make it a reference into another run's tree.
func resolveRecordedPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := absolute
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(current) == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = filepath.Dir(current)
	}
}
