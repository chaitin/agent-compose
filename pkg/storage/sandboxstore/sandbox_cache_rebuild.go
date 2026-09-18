package sandboxstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

func (s *Store) rebuildIndex(ctx context.Context) {
	if err := s.completeIndexRebuild(ctx); err != nil {
		slog.Warn("sandbox listing cache rebuild incomplete; retrying on next startup", "error", err)
	}
}

func (s *Store) completeIndexRebuild(ctx context.Context) error {
	if err := s.runIndexRebuild(ctx); err != nil {
		return err
	}
	return s.index.markComplete(ctx)
}

// runIndexRebuild does the actual repopulation. It returns a non-nil error when
// the rebuild did not fully finish (context cancelled, root unreadable, an index
// upsert failed, or reconcile failed), which the caller uses to decide whether
// the index may be marked complete.
func (s *Store) runIndexRebuild(ctx context.Context) error {
	if s.index == nil {
		return nil
	}

	locations, err := s.layout.discover()
	if err != nil {
		return err
	}
	validIDs := make(map[string]struct{}, len(locations))
	var sandboxes []*Sandbox
	for _, location := range locations {
		if err := ctx.Err(); err != nil {
			return err
		}
		sandbox, loadErr := s.loadSandboxFromDir(location.id, location.path)
		if loadErr != nil {
			// Not a loadable sandbox (corrupt/foreign dir): skip, not a failure.
			continue
		}
		sandboxes = append(sandboxes, sandbox)
		validIDs[sandbox.Summary.ID] = struct{}{}
	}
	projectIDs, err := s.resolveSandboxProjectIDs(ctx, sandboxes)
	if err != nil {
		return fmt.Errorf("resolve sandbox project projection: %w", err)
	}
	for _, sandbox := range sandboxes {
		if upsertErr := s.index.Reconcile(ctx, sandbox, projectIDs[sandbox.Summary.ID]); upsertErr != nil {
			return fmt.Errorf("upsert %s: %w", sandbox.Summary.ID, upsertErr)
		}
	}

	// Rows are retained only for metadata that was successfully loaded. A
	// directory with missing or malformed metadata is not a listable sandbox.
	rows, err := s.index.db.QueryContext(ctx, `SELECT id FROM sandboxes`)
	if err != nil {
		return sandboxCacheError("query rows during reconcile", err)
	}
	var orphans []string
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return sandboxCacheError("scan row during reconcile", errors.Join(scanErr, closeSandboxCacheRows(rows)))
		}
		if _, ok := validIDs[id]; !ok {
			orphans = append(orphans, id)
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return sandboxCacheError("iterate rows during reconcile", errors.Join(rowsErr, closeSandboxCacheRows(rows)))
	}
	if err := closeSandboxCacheRows(rows); err != nil {
		return sandboxCacheError("close rows during reconcile", err)
	}
	for _, id := range orphans {
		if delErr := s.index.Delete(ctx, id); delErr != nil {
			return fmt.Errorf("prune sandbox listing cache row %s: %w", id, delErr)
		}
	}
	return nil
}

func (s *Store) retrySandboxCacheRebuild(ctx context.Context, cause error) error {
	slog.Warn("sandbox listing cache reconciliation failed; clearing projection before retry", "error", cause)
	index := s.index
	if index == nil || index.db == nil {
		return fmt.Errorf("retry sandbox listing cache rebuild: database is unavailable")
	}
	if err := index.invalidate(ctx); err != nil {
		return err
	}
	if err := s.completeIndexRebuild(ctx); err != nil {
		return fmt.Errorf("retry sandbox listing cache rebuild: %w", err)
	}
	return nil
}
