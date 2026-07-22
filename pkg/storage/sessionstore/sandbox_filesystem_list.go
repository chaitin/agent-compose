package sessionstore

import (
	"context"
	"fmt"
	"os"
	"sort"

	domain "agent-compose/pkg/model"
)

// listSandboxesFromFilesystem preserves the original listing contract for
// lightweight stores created through FromConfig, which intentionally do not
// own an index database lifecycle.
func (s *Store) listSandboxesFromFilesystem(ctx context.Context, options SandboxListOptions) (SandboxListResult, error) {
	entries, err := os.ReadDir(s.config.SandboxRoot)
	if err != nil {
		return SandboxListResult{}, fmt.Errorf("read sandbox root: %w", err)
	}
	var sandboxes []*Sandbox
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return SandboxListResult{}, err
		}
		if !entry.IsDir() {
			continue
		}
		sandbox, err := s.loadSandbox(entry.Name())
		if err != nil {
			continue
		}
		s.hydrateSandboxGuestImage(sandbox)
		if !domain.SandboxMatchesListOptions(sandbox, options) || sandboxAtOrAfterCursor(sandbox, options) {
			continue
		}
		sandboxes = append(sandboxes, sandbox)
	}
	sort.Slice(sandboxes, func(i, j int) bool {
		if sandboxes[i].Summary.UpdatedAt.Equal(sandboxes[j].Summary.UpdatedAt) {
			return sandboxes[i].Summary.ID > sandboxes[j].Summary.ID
		}
		return sandboxes[i].Summary.UpdatedAt.After(sandboxes[j].Summary.UpdatedAt)
	})
	total := len(sandboxes)
	offset, limit := domain.NormalizeSandboxListBounds(options.Offset, options.Limit)
	page := domain.PaginateSandboxes(sandboxes, offset, limit)
	nextOffset := min(offset+len(page), total)
	return SandboxListResult{
		Sandboxes:  page,
		TotalCount: total,
		HasMore:    nextOffset < total,
		NextOffset: nextOffset,
	}, nil
}

func sandboxAtOrAfterCursor(sandbox *Sandbox, options SandboxListOptions) bool {
	if options.BeforeUpdatedAt.IsZero() {
		return false
	}
	return sandbox.Summary.UpdatedAt.After(options.BeforeUpdatedAt) ||
		(sandbox.Summary.UpdatedAt.Equal(options.BeforeUpdatedAt) && sandbox.Summary.ID >= options.BeforeID)
}
