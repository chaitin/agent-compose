package sandboxstore

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/chaitin/agent-compose/pkg/sandboxes"
)

func (s *Store) GetSandbox(_ context.Context, id string) (*Sandbox, error) {
	session, err := s.loadSandbox(strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	s.hydrateSandboxGuestImage(session)
	return session, nil
}

// ListSandboxes answers from the sandbox listing cache: filtering, sorting, and
// pagination run as an indexed SQL query over all sandboxes, then only the
// resulting page is loaded from disk for full fidelity (guest image, counts,
// tags, reclamation state). Keyset pagination loads at most a page; legacy
// offset pagination also validates the rows before the requested offset so
// stale index entries cannot shift the observable page.
func (s *Store) ListSandboxes(ctx context.Context, options SandboxListOptions) (SandboxListResult, error) {
	if s.index == nil {
		return s.listSandboxesFromFilesystem(ctx, options)
	}
	offset, limit := sandboxes.NormalizeListBounds(options.Offset, options.Limit)
	queryOffset := 0
	skipped := 0
	var page []*Sandbox
	total := 0
	for len(page) < limit {
		query := options
		query.Offset = queryOffset
		query.Limit = listRowsNeeded(offset-skipped, limit-len(page))
		indexed, indexedTotal, err := s.index.list(ctx, query, s.sandboxDir)
		if err != nil {
			return SandboxListResult{}, err
		}
		// TotalCount reflects the reconciled cache view. Supported writes update
		// it synchronously; out-of-band filesystem removals are deducted when a
		// page encounters and prunes the stale row, or on the next startup.
		total = indexedTotal
		if len(indexed) == 0 {
			break
		}
		loaded := s.loadSandboxesConcurrently(indexed)
		ghosts := 0
		for i, item := range indexed {
			full, loadErr := loaded[i].sandbox, loaded[i].err
			if loadErr != nil {
				if err := s.deleteIndexRow(item.Summary.ID); err != nil {
					return SandboxListResult{}, fmt.Errorf("prune unreadable sandbox listing cache row %s: %w", item.Summary.ID, err)
				}
				ghosts++
				continue
			}
			s.hydrateSandboxGuestImage(full)
			queryOffset++
			if skipped < offset {
				skipped++
				continue
			}
			page = append(page, full)
		}
		total -= ghosts
	}
	nextOffset := total
	if offset < total {
		nextOffset = offset + len(page)
	}
	return SandboxListResult{
		Sandboxes:  page,
		TotalCount: total,
		HasMore:    nextOffset < total,
		NextOffset: nextOffset,
	}, nil
}

// sandboxLoadConcurrency bounds how many metadata.json reads ListSandboxes
// issues at once per page batch, so a large page doesn't open unbounded
// concurrent file descriptors.
const sandboxLoadConcurrency = 8

type sandboxLoadResult struct {
	sandbox *Sandbox
	err     error
}

// loadSandboxesConcurrently hydrates each indexed row's full Sandbox from
// disk in parallel, preserving input order so callers can still apply
// offset/skip logic positionally. loadSandbox and the layout registration it
// performs are safe for concurrent use.
func (s *Store) loadSandboxesConcurrently(indexed []*Sandbox) []sandboxLoadResult {
	results := make([]sandboxLoadResult, len(indexed))
	sem := make(chan struct{}, sandboxLoadConcurrency)
	var wg sync.WaitGroup
	for i, item := range indexed {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			full, err := s.loadSandbox(id)
			results[i] = sandboxLoadResult{sandbox: full, err: err}
		}(i, item.Summary.ID)
	}
	wg.Wait()
	return results
}

func listRowsNeeded(skip, page int) int {
	maxInt := int(^uint(0) >> 1)
	if skip > maxInt-page {
		return maxInt
	}
	return skip + page
}
