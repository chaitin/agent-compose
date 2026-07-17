package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	domain "agent-compose/pkg/model"
)

type sandboxStore struct {
	db *sql.DB
}

const sandboxSummaryColumns = `id, short_id, title, trigger_source, driver,
	vm_status, guest_image, pull_policy, runtime_ref, workspace_path, proxy_path,
	cell_count, event_count, tags_json, created_at, updated_at`

func (s *sandboxStore) ensureSandboxSchema(ctx context.Context) error {
	// created_at and updated_at are Unix nanoseconds. Nanosecond precision keeps
	// database ordering identical to the time.Time values in metadata.json.
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
	tags := summary.Tags
	if tags == nil {
		tags = []domain.SandboxTag{}
	}
	tagsJSON, err := json.Marshal(tags)
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
		updated_at = excluded.updated_at
	WHERE excluded.updated_at >= sandboxes.updated_at`,
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
		sandboxTimestampValue(summary.CreatedAt),
		sandboxTimestampValue(summary.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert sandbox %s: %w", summary.ID, err)
	}
	return nil
}

func (s *sandboxStore) GetSandboxSummary(ctx context.Context, id string) (domain.SandboxSummary, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.SandboxSummary{}, fmt.Errorf("sandbox id is required")
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+sandboxSummaryColumns+` FROM sandboxes WHERE id = ?`, id)
	summary, err := scanSandboxSummary(row.Scan)
	if err != nil {
		return domain.SandboxSummary{}, fmt.Errorf("get sandbox %s: %w", id, err)
	}
	return summary, nil
}

func (s *sandboxStore) ListSandboxSummaries(ctx context.Context, options domain.SandboxSummaryListOptions) (domain.SandboxSummaryListResult, error) {
	beforeID := strings.TrimSpace(options.BeforeID)
	if !options.BeforeUpdatedAt.IsZero() && beforeID == "" {
		return domain.SandboxSummaryListResult{}, fmt.Errorf("sandbox before id is required when before updated at is set")
	}
	if options.BeforeUpdatedAt.IsZero() && beforeID != "" {
		return domain.SandboxSummaryListResult{}, fmt.Errorf("sandbox before updated at is required when before id is set")
	}
	where := make([]string, 0, 3)
	args := make([]any, 0, 6)
	if driver := strings.ToLower(strings.TrimSpace(options.Driver)); driver != "" {
		where = append(where, "driver = ?")
		args = append(args, driver)
	}
	if status := strings.ToUpper(strings.TrimSpace(options.VMStatus)); status != "" {
		where = append(where, "vm_status = ?")
		args = append(args, status)
	}
	if !options.BeforeUpdatedAt.IsZero() {
		where = append(where, "(updated_at < ? OR (updated_at = ? AND id < ?))")
		updatedAt := sandboxTimestampValue(options.BeforeUpdatedAt)
		args = append(args, updatedAt, updatedAt, beforeID)
	}
	query := `SELECT ` + sandboxSummaryColumns + ` FROM sandboxes`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	limit := options.Limit
	if limit <= 0 {
		limit = domain.DefaultSandboxListLimit
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.SandboxSummaryListResult{}, fmt.Errorf("list sandboxes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	summaries := make([]domain.SandboxSummary, 0, limit+1)
	for rows.Next() {
		summary, err := scanSandboxSummary(rows.Scan)
		if err != nil {
			return domain.SandboxSummaryListResult{}, err
		}
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return domain.SandboxSummaryListResult{}, fmt.Errorf("iterate sandboxes: %w", err)
	}
	hasMore := len(summaries) > limit
	if hasMore {
		summaries = summaries[:limit]
	}
	return domain.SandboxSummaryListResult{Sandboxes: summaries, HasMore: hasMore}, nil
}

func (s *sandboxStore) DeleteSandbox(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("sandbox id is required")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sandboxes WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete sandbox %s: %w", id, err)
	}
	return nil
}

type sandboxSummaryScanner func(dest ...any) error

func scanSandboxSummary(scan sandboxSummaryScanner) (domain.SandboxSummary, error) {
	var summary domain.SandboxSummary
	var tagsJSON string
	var createdAt, updatedAt int64
	if err := scan(
		&summary.ID,
		&summary.ShortID,
		&summary.Title,
		&summary.TriggerSource,
		&summary.Driver,
		&summary.VMStatus,
		&summary.GuestImage,
		&summary.PullPolicy,
		&summary.RuntimeRef,
		&summary.WorkspacePath,
		&summary.ProxyPath,
		&summary.CellCount,
		&summary.EventCount,
		&tagsJSON,
		&createdAt,
		&updatedAt,
	); err != nil {
		return domain.SandboxSummary{}, fmt.Errorf("scan sandbox summary: %w", err)
	}
	if err := json.Unmarshal([]byte(tagsJSON), &summary.Tags); err != nil {
		return domain.SandboxSummary{}, fmt.Errorf("decode sandbox %s tags: %w", summary.ID, err)
	}
	if summary.Tags == nil {
		summary.Tags = []domain.SandboxTag{}
	}
	summary.CreatedAt = sandboxTimestampTime(createdAt)
	summary.UpdatedAt = sandboxTimestampTime(updatedAt)
	return summary, nil
}

func sandboxTimestampValue(value time.Time) int64 {
	return value.UnixNano()
}

func sandboxTimestampTime(value int64) time.Time {
	return time.Unix(0, value).UTC()
}
