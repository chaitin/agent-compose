package sandboxstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

type sandboxIndexOperation string

const (
	sandboxIndexRefresh sandboxIndexOperation = "refresh"
	sandboxIndexDelete  sandboxIndexOperation = "delete"
)

func (s *Store) recordIndex(session *Sandbox) {
	if s.index == nil || session == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sandboxCacheWriteTimeout)
	defer cancel()
	if err := s.syncSandboxIndex(ctx, session.Summary.ID, sandboxIndexRefresh); err != nil {
		s.markIndexRepair(session.Summary.ID)
	}
}

func (s *Store) deleteIndexRow(id string) error {
	if s.index == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), sandboxCacheWriteTimeout)
	defer cancel()
	if err := s.syncSandboxIndex(ctx, id, sandboxIndexDelete); err != nil {
		s.markIndexRepair(id)
		return fmt.Errorf("delete sandbox listing cache row %s: %w", id, err)
	}
	return nil
}

func (s *Store) markIndexRepair(id string) {
	if s.indexRepairs != nil {
		s.indexRepairs.mark(id)
	}
}

func (s *Store) syncSandboxIndex(ctx context.Context, id string, operation sandboxIndexOperation) (err error) {
	observation := newSandboxIndexObservation(s.index.db)
	defer func() { observation.finish(ctx, id, operation, err) }()
	var revision uint64
	if s.indexRepairs != nil {
		release, acquireErr := s.indexRepairs.acquire(ctx, sandboxLockKey(id))
		observation.gateWait = time.Since(observation.started)
		if acquireErr != nil {
			return fmt.Errorf("wait for sandbox index update: %w", acquireErr)
		}
		defer release()
		revision = s.indexRepairs.revision(id)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if operation == sandboxIndexDelete {
		started := time.Now()
		err = s.index.Delete(ctx, id)
		observation.write = time.Since(started)
	} else {
		err = s.refreshSandboxIndex(ctx, id, observation)
	}
	if err == nil && s.indexRepairs != nil {
		s.indexRepairs.clear(id, revision)
	}
	return err
}

func (s *Store) refreshSandboxIndex(ctx context.Context, id string, observation *sandboxIndexObservation) error {
	started := time.Now()
	// Read after acquiring the per-ID gate: queued retries never carry an old
	// Sandbox value across a newer metadata commit or deletion.
	sandbox, err := s.loadSandbox(id)
	observation.metadataRead = time.Since(started)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, errInvalidSandboxMetadata) {
		// Match startup reconciliation: invalid metadata is not listable. Only
		// retire the pending revision after its stale projection is removed;
		// a database failure still needs a retry. Keep the source file intact.
		started = time.Now()
		deleteErr := s.index.Delete(ctx, id)
		observation.write = time.Since(started)
		if deleteErr != nil {
			return deleteErr
		}
		if errors.Is(err, errInvalidSandboxMetadata) {
			slog.WarnContext(ctx, "invalid sandbox metadata excluded from listing cache", "sandbox_id", id, "error", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	started = time.Now()
	projectIDs, err := s.resolveSandboxProjectIDs(ctx, []*Sandbox{sandbox})
	observation.projectLookup = time.Since(started)
	if err != nil {
		return err
	}
	started = time.Now()
	err = s.index.Upsert(ctx, sandbox, projectIDs[sandbox.Summary.ID])
	observation.write = time.Since(started)
	return err
}
