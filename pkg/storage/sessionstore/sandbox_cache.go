package sessionstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "agent-compose/pkg/model"
)

const sandboxCacheVersion = 2

var errSandboxCache = errors.New("sandbox listing cache failure")

// sandboxCache is a rebuildable SQLite cache of sandbox summaries. The
// filesystem (SandboxRoot/<id>/metadata.json) stays authoritative; this index
// exists so ListSandboxes can answer with an indexed query instead of scanning
// every sandbox directory.
type sandboxCache struct {
	db     *sql.DB
	ownsDB bool
}

func openSandboxCacheDB(ctx context.Context, db *sql.DB) (*sandboxCache, bool, error) {
	if db == nil {
		return nil, false, fmt.Errorf("sandbox listing database is required")
	}
	idx := &sandboxCache{db: db}
	if err := idx.quickCheck(ctx); err != nil {
		return nil, false, err
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE((SELECT version FROM sandbox_projection_meta WHERE id = 1), 0)`).Scan(&version); err != nil {
		return nil, false, sandboxCacheError("read schema version", err)
	}
	if err := idx.validateSchema(ctx); err != nil {
		return nil, false, err
	}

	needsRebuild := version != sandboxCacheVersion
	if needsRebuild {
		if err := idx.invalidate(ctx); err != nil {
			return nil, false, err
		}
	}
	// The schema version is intentionally NOT stamped here. It is stamped by
	// markComplete only after a full rebuild finishes, so an interrupted rebuild
	// leaves needsRebuild=true and is retried on the next startup rather than
	// leaving a partially-populated index treated as current.
	return idx, needsRebuild, nil
}

func (x *sandboxCache) invalidate(ctx context.Context) (operationErr error) {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return sandboxCacheError("begin invalidation", err)
	}
	defer func() {
		if operationErr != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sandboxes`); err != nil {
		return sandboxCacheError("clear projection", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sandbox_projection_meta`); err != nil {
		return sandboxCacheError("clear schema version", err)
	}
	if err := tx.Commit(); err != nil {
		return sandboxCacheError("commit invalidation", err)
	}
	return nil
}

func closeSandboxCacheDB(db *sql.DB, operationErr error) error {
	if err := db.Close(); err != nil {
		return errors.Join(operationErr, fmt.Errorf("close sandbox listing cache after failure: %w", err))
	}
	return operationErr
}

func (x *sandboxCache) Close() error {
	if x == nil || x.db == nil || !x.ownsDB {
		return nil
	}
	return x.db.Close()
}

func (x *sandboxCache) quickCheck(ctx context.Context) error {
	var quickCheck string
	if err := x.db.QueryRowContext(ctx, `PRAGMA quick_check(1)`).Scan(&quickCheck); err != nil {
		return sandboxCacheError("quick check", err)
	}
	if quickCheck != "ok" {
		return sandboxCacheError("quick check", fmt.Errorf("result is %q", quickCheck))
	}
	return nil
}

func (x *sandboxCache) validateSchema(ctx context.Context) error {
	rows, err := x.db.QueryContext(ctx, `SELECT `+sandboxCacheValidationCols+` FROM sandboxes LIMIT 0`)
	if err != nil {
		return sandboxCacheError("validate schema", err)
	}
	if err := rows.Close(); err != nil {
		return sandboxCacheError("close schema validation query", err)
	}
	return nil
}

func sandboxCacheError(operation string, err error) error {
	return fmt.Errorf("%w: %s: %w", errSandboxCache, operation, err)
}

// Upsert records the latest sandbox metadata committed by the store. Callers
// serialize metadata commits, so even an older timestamp is authoritative.
func (x *sandboxCache) Upsert(ctx context.Context, sb *domain.Sandbox, projectID string) error {
	return x.upsert(ctx, sb, strings.TrimSpace(projectID))
}

// Reconcile replaces an index row with authoritative filesystem state even if
// an earlier failed write left a newer, never-persisted timestamp in the index.
func (x *sandboxCache) Reconcile(ctx context.Context, sb *domain.Sandbox, projectID string) error {
	return x.upsert(ctx, sb, strings.TrimSpace(projectID))
}

func (x *sandboxCache) upsert(ctx context.Context, sb *domain.Sandbox, projectID string) error {
	if sb == nil || sb.Summary.ID == "" {
		return fmt.Errorf("sandbox id is required")
	}
	s := sb.Summary
	wsID := sb.WorkspaceID
	var nestedWSID, wsName, wsType string
	if sb.Workspace != nil {
		nestedWSID = sb.Workspace.ID
		wsName = sb.Workspace.Name
		wsType = sb.Workspace.Type
	}
	// Search columns use Go's Unicode case mapping so indexed filtering
	// preserves SandboxMatchesListOptions semantics; SQLite's LOWER only folds
	// ASCII characters.
	query := `
INSERT INTO sandboxes (id, short_id, title, trigger_source, driver, vm_status, project_id,
	workspace_path, workspace_id, nested_workspace_id, workspace_name, workspace_type, created_at, updated_at,
	sandbox_type, title_search, trigger_source_search, driver_search, vm_status_search, project_id_search,
	workspace_path_search, workspace_id_search, nested_workspace_id_search, workspace_name_search, workspace_type_search)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	short_id=excluded.short_id, title=excluded.title, trigger_source=excluded.trigger_source,
	driver=excluded.driver, vm_status=excluded.vm_status, project_id=excluded.project_id, workspace_path=excluded.workspace_path,
	workspace_id=excluded.workspace_id, nested_workspace_id=excluded.nested_workspace_id,
	workspace_name=excluded.workspace_name,
	workspace_type=excluded.workspace_type, created_at=excluded.created_at, updated_at=excluded.updated_at,
	sandbox_type=excluded.sandbox_type, title_search=excluded.title_search,
	trigger_source_search=excluded.trigger_source_search, driver_search=excluded.driver_search,
	vm_status_search=excluded.vm_status_search, project_id_search=excluded.project_id_search, workspace_path_search=excluded.workspace_path_search,
	workspace_id_search=excluded.workspace_id_search,
	nested_workspace_id_search=excluded.nested_workspace_id_search,
	workspace_name_search=excluded.workspace_name_search, workspace_type_search=excluded.workspace_type_search
`
	_, err := x.db.ExecContext(ctx, query,
		s.ID, s.ShortID, s.Title, s.TriggerSource, s.Driver, s.VMStatus, projectID,
		s.WorkspacePath, wsID, nestedWSID, wsName, wsType,
		sandboxCacheUnixNano(s.CreatedAt), sandboxCacheUnixNano(s.UpdatedAt),
		domain.SandboxTypeFromTriggerSource(s.TriggerSource), strings.ToLower(s.Title), strings.ToLower(s.TriggerSource),
		strings.ToLower(strings.TrimSpace(s.Driver)), strings.ToUpper(strings.TrimSpace(s.VMStatus)), strings.ToLower(projectID),
		strings.ToLower(strings.TrimSpace(s.WorkspacePath)), strings.ToLower(strings.TrimSpace(wsID)),
		strings.ToLower(strings.TrimSpace(nestedWSID)), strings.ToLower(strings.TrimSpace(wsName)),
		strings.ToLower(strings.TrimSpace(wsType)))
	if err != nil {
		return sandboxCacheError("upsert sandbox listing cache "+s.ID, err)
	}
	return nil
}

func sandboxCacheUnixNano(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixNano()
}

func sandboxCacheTime(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value).UTC()
}

func (x *sandboxCache) Delete(ctx context.Context, id string) error {
	if _, err := x.db.ExecContext(ctx, `DELETE FROM sandboxes WHERE id = ?`, id); err != nil {
		return sandboxCacheError("delete sandbox listing cache "+id, err)
	}
	return nil
}

// markComplete records that a full rebuild has finished by stamping the schema
// version. openSandboxCache deliberately leaves the version unset on a rebuild
// so that an interrupted rebuild (crash, shutdown, transient error) is retried
// on the next startup instead of leaving the index permanently missing rows.
func (x *sandboxCache) markComplete(ctx context.Context) error {
	if x == nil || x.db == nil {
		return nil
	}
	if _, err := x.db.ExecContext(ctx, `INSERT INTO sandbox_projection_meta(id, version) VALUES(1, ?)
		ON CONFLICT(id) DO UPDATE SET version = excluded.version`, sandboxCacheVersion); err != nil {
		return sandboxCacheError("mark sandbox listing cache complete", err)
	}
	return nil
}

const sandboxCacheValidationCols = `id, short_id, title, trigger_source, driver, vm_status, project_id,
	workspace_path, workspace_id, nested_workspace_id, workspace_name, workspace_type, created_at, updated_at,
	sandbox_type, title_search, trigger_source_search, driver_search, vm_status_search, project_id_search,
	workspace_path_search, workspace_id_search, nested_workspace_id_search, workspace_name_search, workspace_type_search`
