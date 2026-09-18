package sandboxstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/idset"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

const sandboxSummaryQueryBatchSize = 500

// ListSandboxSummaries returns summaries for the requested sandbox IDs. The
// SQLite projection serves the normal path in batches; filesystem reads are a
// best-effort fallback when the rebuildable projection is unavailable.
func (s *Store) ListSandboxSummaries(ctx context.Context, sandboxIDs []string) (map[string]domain.SandboxSummary, error) {
	ids := idset.Normalize(sandboxIDs)
	if len(ids) == 0 {
		return map[string]domain.SandboxSummary{}, nil
	}

	indexedIDs, fallbackIDs := s.partitionSandboxSummaryIDs(ids)
	summaries := make(map[string]domain.SandboxSummary, len(ids))
	var cacheErr error
	if len(indexedIDs) > 0 {
		// Leave time for filesystem fallback when the shared connection pool is
		// busy. Optional summaries must not wait for cache repair or pool recovery.
		queryCtx, cancel := context.WithTimeout(ctx, time.Second)
		indexed, err := s.index.listSummaries(queryCtx, indexedIDs, s.sandboxDir)
		cancel()
		if err != nil {
			cacheErr = err
			fallbackIDs = append(fallbackIDs, indexedIDs...)
		} else {
			for id, summary := range indexed {
				summaries[id] = summary
			}
		}
	}
	// A write may have failed while the projection was being queried. Override
	// any newly dirty results from authoritative metadata as well.
	for _, id := range indexedIDs {
		if s.indexRepairs != nil && s.indexRepairs.revision(id) != 0 {
			delete(summaries, id)
			fallbackIDs = append(fallbackIDs, id)
		}
	}
	fallback, filesystemErr := s.listSandboxSummariesFromFilesystem(ctx, idset.Normalize(fallbackIDs))
	for id, summary := range fallback {
		summaries[id] = summary
	}
	return summaries, errors.Join(cacheErr, filesystemErr)
}

func (s *Store) partitionSandboxSummaryIDs(ids []string) (indexed, fallback []string) {
	for _, id := range ids {
		if s.index == nil || (s.indexRepairs != nil && s.indexRepairs.revision(id) != 0) {
			fallback = append(fallback, id)
		} else {
			indexed = append(indexed, id)
		}
	}
	return indexed, fallback
}

func (s *Store) listSandboxSummariesFromFilesystem(ctx context.Context, ids []string) (map[string]domain.SandboxSummary, error) {
	summaries := make(map[string]domain.SandboxSummary, len(ids))
	var firstErr error
	failed := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return summaries, errors.Join(firstErr, err)
		}
		sandbox, err := s.GetSandbox(ctx, id)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, domain.ErrNotFound) {
				continue
			}
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if sandbox != nil {
			summaries[id] = sandbox.Summary
		}
	}
	if failed > 0 {
		return summaries, fmt.Errorf("load %d sandbox summaries: %w", failed, firstErr)
	}
	return summaries, nil
}

func (x *sandboxCache) listSummaries(ctx context.Context, ids []string, sandboxDir func(string) string) (map[string]domain.SandboxSummary, error) {
	summaries := make(map[string]domain.SandboxSummary, len(ids))
	for start := 0; start < len(ids); start += sandboxSummaryQueryBatchSize {
		end := min(start+sandboxSummaryQueryBatchSize, len(ids))
		batch := ids[start:end]
		args := make([]any, len(batch))
		for index, id := range batch {
			args[index] = id
		}
		rows, err := x.db.QueryContext(ctx, `SELECT `+sandboxSelectCols+` FROM sandboxes WHERE id IN (`+sandboxSummaryPlaceholders(len(batch))+`)`, args...)
		if err != nil {
			return nil, sandboxCacheError("query sandbox summaries", err)
		}
		for rows.Next() {
			item, err := scanSandboxCacheRow(rows.Scan)
			if err != nil {
				return nil, errors.Join(err, closeSandboxCacheRows(rows))
			}
			if sandboxDir != nil {
				item.Summary.WorkspacePath = filepath.Join(sandboxDir(item.Summary.ID), "workspace")
			}
			summaries[item.Summary.ID] = item.Summary
		}
		if err := rows.Err(); err != nil {
			return nil, errors.Join(fmt.Errorf("iterate sandbox summaries: %w", err), closeSandboxCacheRows(rows))
		}
		if err := closeSandboxCacheRows(rows); err != nil {
			return nil, err
		}
	}
	return summaries, nil
}

func sandboxSummaryPlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}
