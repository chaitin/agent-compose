package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"
)

type sandboxStore struct {
	db *sql.DB
}

func (s *sandboxStore) ensureSandboxSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS sandboxes (
			id TEXT PRIMARY KEY,
			short_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			trigger_source TEXT NOT NULL DEFAULT '',
			driver TEXT NOT NULL DEFAULT '',
			vm_status TEXT NOT NULL DEFAULT '',
			guest_image TEXT NOT NULL DEFAULT '',
			pull_policy TEXT NOT NULL DEFAULT '',
			runtime_ref TEXT NOT NULL DEFAULT '',
			workspace_path TEXT NOT NULL DEFAULT '',
			proxy_path TEXT NOT NULL DEFAULT '',
			cell_count INTEGER NOT NULL DEFAULT 0,
			event_count INTEGER NOT NULL DEFAULT 0,
			tags_json TEXT NOT NULL DEFAULT '[]',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_sandboxes_updated ON sandboxes(updated_at DESC, id DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_sandboxes_vm_status_updated ON sandboxes(vm_status, updated_at DESC, id DESC);`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create sandboxes schema: %w", err)
		}
	}
	return nil
}

// UpsertSandbox records the queryable Sandbox summary in SQLite. Sandbox
// directories remain the source for workspace and runtime files.
func (s *sandboxStore) UpsertSandbox(ctx context.Context, sandbox *domain.Sandbox) error {
	if sandbox == nil {
		return fmt.Errorf("sandbox is required")
	}
	summary := sandbox.Summary
	if strings.TrimSpace(summary.ID) == "" {
		return fmt.Errorf("sandbox id is required")
	}
	tagsJSON, err := json.Marshal(summary.Tags)
	if err != nil {
		return fmt.Errorf("encode sandbox tags: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sandboxes (
		id, short_id, title, trigger_source, driver, vm_status, guest_image,
		pull_policy, runtime_ref, workspace_path, proxy_path, cell_count,
		event_count, tags_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		short_id = excluded.short_id,
		title = excluded.title,
		trigger_source = excluded.trigger_source,
		driver = excluded.driver,
		vm_status = excluded.vm_status,
		guest_image = excluded.guest_image,
		pull_policy = excluded.pull_policy,
		runtime_ref = excluded.runtime_ref,
		workspace_path = excluded.workspace_path,
		proxy_path = excluded.proxy_path,
		cell_count = excluded.cell_count,
		event_count = excluded.event_count,
		tags_json = excluded.tags_json,
		created_at = excluded.created_at,
		updated_at = excluded.updated_at`,
		summary.ID,
		summary.ShortID,
		summary.Title,
		summary.TriggerSource,
		summary.Driver,
		summary.VMStatus,
		summary.GuestImage,
		summary.PullPolicy,
		summary.RuntimeRef,
		summary.WorkspacePath,
		summary.ProxyPath,
		summary.CellCount,
		summary.EventCount,
		string(tagsJSON),
		summary.CreatedAt.UnixMilli(),
		summary.UpdatedAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("upsert sandbox %s: %w", summary.ID, err)
	}
	return nil
}
