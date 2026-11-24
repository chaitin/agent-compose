package sandboxstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chaitin/agent-compose/ent/dao"
	sandboxdao "github.com/chaitin/agent-compose/ent/dao/sandbox"
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
	if filesystemErr != nil {
		return summaries, errors.Join(cacheErr, filesystemErr)
	}
	// Successful fallback has recovered the cache failure. Confirmed absent
	// sandboxes may omit a summary; neither case makes enrichment incomplete.
	return summaries, nil
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
		entities, err := x.client.Sandbox.Query().
			Select(sandboxSummarySelectFields...).
			Where(sandboxdao.IDIn(batch...)).
			All(ctx)
		if err != nil {
			return nil, sandboxCacheError("query sandbox summaries", err)
		}
		for _, entity := range entities {
			item := sandboxFromEntity(entity)
			if sandboxDir != nil {
				item.Summary.WorkspacePath = filepath.Join(sandboxDir(item.Summary.ID), "workspace")
			}
			summaries[item.Summary.ID] = item.Summary
		}
	}
	return summaries, nil
}

// sandboxFromEntity maps the sandboxes columns selected by
// sandboxSummarySelectFields to the same domain shape scanSandboxCacheRow
// returns for the SQL listing path. Timestamps keep the cache's nanosecond
// representation.
func sandboxFromEntity(entity *dao.Sandbox) *domain.Sandbox {
	return &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            entity.ID,
			ShortID:       entity.ShortID,
			Title:         entity.Title,
			TriggerSource: entity.TriggerSource,
			Driver:        entity.Driver,
			VMStatus:      entity.VMStatus,
			WorkspacePath: entity.WorkspacePath,
			CreatedAt:     sandboxCacheTime(entity.CreatedAt),
			UpdatedAt:     sandboxCacheTime(entity.UpdatedAt),
		},
		WorkspaceID: entity.WorkspaceID,
		Workspace: &domain.SandboxWorkspace{
			ID:   entity.NestedWorkspaceID,
			Name: entity.WorkspaceName,
			Type: entity.WorkspaceType,
		},
	}
}
