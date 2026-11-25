package runhistory

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

type preparedDirectory struct {
	parent   *os.Root
	name     string
	pending  string
	original os.FileInfo
	kind     string
}

func pendingHistoryName(runID string) string { return ".history-removing-" + runID }

func (a FSArtifacts) Prepare(ctx context.Context, scope Scope) (PreparedArtifacts, error) {
	if err := a.validate(ctx, scope); err != nil {
		return nil, err
	}
	var set preparedSet
	for _, run := range scope.Runs {
		if !ownsRunDirectory(run) {
			continue
		}
		p, err := a.prepareDirectory([]string{"runs"}, run.RunID, "run")
		if err != nil {
			_ = set.Close() // Preserve the preparation error.
			return nil, err
		}
		set = append(set, p)
		if strings.TrimSpace(a.RunLogArchiveRoot) == "" {
			continue
		}
		// An archived log belongs to the run as much as the original did.
		archive, err := prepareDirectoryUnder(a.RunLogArchiveRoot, false, []string{"runs"}, run.RunID, "run log archive")
		if err != nil {
			_ = set.Close() // Preserve the preparation error.
			return nil, err
		}
		set = append(set, archive)
	}
	if scope.SchedulerRun != nil {
		run := scope.SchedulerRun
		p, err := a.prepareDirectory([]string{"schedulers", run.SchedulerID, "runs"}, run.ID, "scheduler")
		if err != nil {
			_ = set.Close() // Preserve the preparation error.
			return nil, err
		}
		set = append(set, p)
	}
	return set, nil
}

// ownsRunDirectory reports whether the run keeps its history in a directory
// of its own under the data root (a run log) rather than inside its sandbox.
func ownsRunDirectory(run domain.ProjectRunRecord) bool {
	return run.LogFormat == runlog.FormatJSONL && identity.IsID(run.RunID)
}

// prepareDirectory pins parent (below the data root) and records the state
// of the child directory name, so Detach can move exactly that directory
// aside. A missing parent or child means there is nothing to remove.
func (a FSArtifacts) prepareDirectory(parentParts []string, name, kind string) (*preparedDirectory, error) {
	// The data root always exists for a running daemon; a missing one means a
	// misconfigured or unmounted root, where absence proves nothing. Missing
	// directories below it are ordinary and mean there is nothing to remove.
	return prepareDirectoryUnder(a.DataRoot, true, parentParts, name, kind)
}

// prepareDirectoryUnder prepares name below rootPath/parentParts. A root that
// is created lazily, such as an archive root nothing was archived to yet, is
// not required: its absence means there is nothing to remove.
func prepareDirectoryUnder(rootPath string, requireRoot bool, parentParts []string, name, kind string) (*preparedDirectory, error) {
	root, err := os.OpenRoot(rootPath)
	if errors.Is(err, os.ErrNotExist) {
		if !requireRoot {
			return &preparedDirectory{name: name, pending: pendingHistoryName(name), kind: kind}, nil
		}
		return nil, artifactPrecondition(kind+" data root is unavailable", err)
	}
	if err != nil {
		return nil, err
	}
	parent, err := pinArtifactParent(root, parentParts)
	if err != nil {
		return nil, err
	}
	p := &preparedDirectory{parent: parent, name: name, pending: pendingHistoryName(name), kind: kind}
	if parent == nil {
		return p, nil
	}
	for _, entryName := range []string{p.name, p.pending} {
		entry, err := parent.Lstat(entryName)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || entry.Mode()&os.ModeSymlink != 0 || !entry.IsDir() {
			_ = p.Close() // Preserve the validation error; no files were changed.
			return nil, artifactPrecondition(kind+" artifact must be a directory, not a symlink", err)
		}
		if entryName == p.name {
			p.original = entry
		}
	}
	return p, nil
}

// preparedSet removes several prepared directories as one operation.
type preparedSet []*preparedDirectory

func (s preparedSet) Detach(ctx context.Context) error {
	for _, p := range s {
		if err := p.Detach(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s preparedSet) Remove(ctx context.Context) error {
	for _, p := range s {
		if err := p.Remove(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s preparedSet) VerifyRemoved(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, p := range s {
		if err := p.VerifyRemoved(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s preparedSet) Close() error {
	var errs []error
	for _, p := range s {
		errs = append(errs, p.Close())
	}
	return errors.Join(errs...)
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
		return artifactPrecondition(p.kind+" artifact changed during preparation", err)
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
			return artifactPrecondition(p.kind+" history artifacts remain; retry with the same identity", err)
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
