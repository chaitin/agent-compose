package runhistory

import (
	"context"
	"errors"
	"os"
)

type preparedDirectory struct {
	parent   *os.Root
	name     string
	pending  string
	original os.FileInfo
}

func pendingHistoryName(runID string) string { return ".history-removing-" + runID }

func (a FSArtifacts) Prepare(ctx context.Context, scope Scope) (PreparedArtifacts, error) {
	if err := a.validate(ctx, scope); err != nil {
		return nil, err
	}
	p := &preparedDirectory{}
	if scope.SchedulerRun == nil {
		return p, nil
	}
	run := scope.SchedulerRun
	// The data root always exists for a running daemon; a missing one means a
	// misconfigured or unmounted root, where absence proves nothing. Missing
	// directories below it are ordinary and mean there is nothing to remove.
	root, err := os.OpenRoot(a.DataRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, artifactPrecondition("scheduler data root is unavailable", err)
	}
	if err != nil {
		return nil, err
	}
	parent, err := pinArtifactParent(root, []string{"schedulers", run.SchedulerID, "runs"})
	if err != nil {
		return nil, err
	}
	p.parent, p.name, p.pending = parent, run.ID, pendingHistoryName(run.ID)
	if parent == nil {
		return p, nil
	}
	for _, name := range []string{p.name, p.pending} {
		entry, err := parent.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || entry.Mode()&os.ModeSymlink != 0 || !entry.IsDir() {
			_ = p.Close() // Preserve the validation error; no files were changed.
			return nil, artifactPrecondition("scheduler artifact must be a directory, not a symlink", err)
		}
		if name == p.name {
			p.original = entry
		}
	}
	return p, nil
}

// Pin each parent and compare its inode, so a symlink swap cannot redirect the
// later rename or cleanup. The returned root owns the last descriptor only.
func pinArtifactParent(root *os.Root, parts []string) (*os.Root, error) {
	for _, part := range parts {
		entry, err := root.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			return nil, root.Close()
		}
		if err != nil || entry.Mode()&os.ModeSymlink != 0 || !entry.IsDir() {
			_ = root.Close() // Preserve the unsafe-parent error.
			return nil, artifactPrecondition("scheduler artifact parents must be directories, not symlinks", err)
		}
		child, err := root.OpenRoot(part)
		_ = root.Close() // The child descriptor independently pins this directory.
		if err != nil {
			return nil, err
		}
		opened, err := child.Stat(".")
		if err != nil || !os.SameFile(entry, opened) {
			_ = child.Close() // Preserve the inode validation error.
			return nil, artifactPrecondition("scheduler artifact parent changed during preparation", err)
		}
		root = child
	}
	return root, nil
}

func (p *preparedDirectory) Detach(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.parent == nil || p.original == nil {
		return nil
	}
	current, err := p.parent.Lstat(p.name)
	if errors.Is(err, os.ErrNotExist) {
		return nil // Another exact remover may already have isolated it.
	}
	if err != nil || !os.SameFile(p.original, current) {
		return artifactPrecondition("scheduler artifact changed during preparation", err)
	}
	if entry, err := p.parent.Lstat(p.pending); !errors.Is(err, os.ErrNotExist) {
		if err != nil || !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 {
			return artifactPrecondition("pending history cleanup must be a directory", err)
		}
		// Resume the old cleanup first. Final verification retains history if
		// the canonical directory remains; a same-identity retry isolates it.
		return nil
	}
	return p.parent.Rename(p.name, p.pending)
}

func (p *preparedDirectory) Remove(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.parent == nil {
		return nil
	}
	return p.parent.RemoveAll(p.pending)
}

func (p *preparedDirectory) VerifyRemoved(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.parent == nil {
		return nil
	}
	for _, name := range []string{p.name, p.pending} {
		if _, err := p.parent.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return artifactPrecondition("scheduler history artifacts remain; retry with the same identity", err)
		}
	}
	return nil
}

func (p *preparedDirectory) Close() error {
	if p.parent == nil {
		return nil
	}
	return p.parent.Close()
}

// Remove is also used by focused filesystem tests without a database owner.
func (a FSArtifacts) Remove(ctx context.Context, scope Scope) error {
	p, err := a.Prepare(ctx, scope)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }() // Cleanup does not change the operation result.
	if err := p.Detach(ctx); err != nil {
		return err
	}
	if err := p.Remove(ctx); err != nil {
		return err
	}
	return p.VerifyRemoved(ctx)
}
