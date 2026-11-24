package sandboxstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	sandboxdao "github.com/chaitin/agent-compose/ent/dao/sandbox"
	"github.com/chaitin/agent-compose/ent/dao/sandboxprojectionmeta"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/storage/storeutil"

	"github.com/chaitin/agent-compose/pkg/storage/dialect"
)

const sandboxCacheVersion = 2

var errSandboxCache = errors.New("sandbox listing cache failure")

// sandboxCache is a rebuildable SQLite cache of sandbox summaries. The
// filesystem (SandboxRoot/<id>/metadata.json) stays authoritative; this index
// exists so ListSandboxes can answer with an indexed query instead of scanning
// every sandbox directory.
type sandboxCache struct {
	db      *sql.DB
	dialect dialect.Dialect
	client  *dao.Client
	ownsDB  bool
}

// newEntClient builds an ent client over the shared application connection.
// The client is a thin dialect-aware view of the caller-owned pool: it is never
// closed, and the sandbox listing cache owns no resources through it.
func newEntClient(db *sql.DB, d dialect.Dialect) *dao.Client {
	return dao.NewClient(dao.Driver(entsql.OpenDB(d.EntDialect(), db)))
}

func openSandboxCacheDB(ctx context.Context, db *sql.DB) (*sandboxCache, bool, error) {
	return openSandboxCacheDBWithDialect(ctx, db, dialect.SQLite())
}

func openSandboxCacheDBWithDialect(ctx context.Context, db *sql.DB, d dialect.Dialect) (*sandboxCache, bool, error) {
	if db == nil {
		return nil, false, fmt.Errorf("sandbox listing database is required")
	}
	d = dialect.OrSQLite(d)
	idx := &sandboxCache{db: db, dialect: d, client: newEntClient(db, d)}
	if err := idx.quickCheck(ctx); err != nil {
		return nil, false, err
	}
	var version int
	meta, err := idx.client.SandboxProjectionMeta.Query().
		Where(sandboxprojectionmeta.IDEQ(1)).
		Only(ctx)
	switch {
	case err == nil:
		version = meta.Version
	case !dao.IsNotFound(err):
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
	tx, err := x.client.Tx(ctx)
	if err != nil {
		return sandboxCacheError("begin invalidation", err)
	}
	defer func() {
		if operationErr != nil {
			_ = tx.Rollback()
		}
	}()
	client := tx.Client()
	if _, err := client.Sandbox.Delete().Exec(ctx); err != nil {
		return sandboxCacheError("clear projection", err)
	}
	if _, err := client.SandboxProjectionMeta.Delete().Exec(ctx); err != nil {
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
	// Kept as SQL: the health probe is backend-specific DDL/PRAGMA that ent does
	// not model.
	if err := x.db.QueryRowContext(ctx, x.dialect.QuickCheckQuery()).Scan(&quickCheck); err != nil {
		return sandboxCacheError("quick check", err)
	}
	if quickCheck != "ok" {
		return sandboxCacheError("quick check", fmt.Errorf("result is %q", quickCheck))
	}
	return nil
}

func (x *sandboxCache) validateSchema(ctx context.Context) (err error) {
	// LIMIT 0 executes the column list without producing rows, so the query
	// fails exactly when the cache schema has drifted from the expected columns.
	// Kept as SQL: this intentionally probes the physical column list, which an
	// ent query would rewrite into its generated column set.
	rows, err := x.db.QueryContext(ctx, `SELECT `+sandboxCacheValidationCols+` FROM sandboxes LIMIT 0`)
	if err != nil {
		return sandboxCacheError("validate schema", err)
	}
	// The cursor is never drained, so database/sql does not release its
	// connection on its own; closing here keeps the pool free for the caller.
	defer func() { storeutil.ReportCloseWith(rows.Close(), &err, "schema validation query", sandboxCacheError) }()
	if err := rows.Err(); err != nil {
		return sandboxCacheError("validate schema", err)
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
	sandboxType := sandboxes.TypeFromTriggerSource(s.TriggerSource)
	titleSearch := strings.ToLower(s.Title)
	triggerSourceSearch := strings.ToLower(s.TriggerSource)
	driverSearch := strings.ToLower(strings.TrimSpace(s.Driver))
	vmStatusSearch := strings.ToUpper(strings.TrimSpace(s.VMStatus))
	projectIDSearch := strings.ToLower(projectID)
	workspacePathSearch := strings.ToLower(strings.TrimSpace(s.WorkspacePath))
	workspaceIDSearch := strings.ToLower(strings.TrimSpace(wsID))
	nestedWorkspaceIDSearch := strings.ToLower(strings.TrimSpace(nestedWSID))
	workspaceNameSearch := strings.ToLower(strings.TrimSpace(wsName))
	workspaceTypeSearch := strings.ToLower(strings.TrimSpace(wsType))
	// All projected columns take the inserted value on conflict, matching the
	// previous INSERT ... ON CONFLICT(id) DO UPDATE SET <col> = excluded.<col>.
	if err := x.client.Sandbox.Create().
		SetID(s.ID).
		SetShortID(s.ShortID).
		SetTitle(s.Title).
		SetTriggerSource(s.TriggerSource).
		SetDriver(s.Driver).
		SetVMStatus(s.VMStatus).
		SetProjectID(projectID).
		SetWorkspacePath(s.WorkspacePath).
		SetWorkspaceID(wsID).
		SetNestedWorkspaceID(nestedWSID).
		SetWorkspaceName(wsName).
		SetWorkspaceType(wsType).
		SetCreatedAt(sandboxCacheUnixNano(s.CreatedAt)).
		SetUpdatedAt(sandboxCacheUnixNano(s.UpdatedAt)).
		SetSandboxType(sandboxType).
		SetTitleSearch(titleSearch).
		SetTriggerSourceSearch(triggerSourceSearch).
		SetDriverSearch(driverSearch).
		SetVMStatusSearch(vmStatusSearch).
		SetProjectIDSearch(projectIDSearch).
		SetWorkspacePathSearch(workspacePathSearch).
		SetWorkspaceIDSearch(workspaceIDSearch).
		SetNestedWorkspaceIDSearch(nestedWorkspaceIDSearch).
		SetWorkspaceNameSearch(workspaceNameSearch).
		SetWorkspaceTypeSearch(workspaceTypeSearch).
		OnConflict(entsql.ConflictColumns(sandboxdao.FieldID)).
		Update(func(u *dao.SandboxUpsert) {
			u.UpdateShortID()
			u.UpdateTitle()
			u.UpdateTriggerSource()
			u.UpdateDriver()
			u.UpdateVMStatus()
			u.UpdateProjectID()
			u.UpdateWorkspacePath()
			u.UpdateWorkspaceID()
			u.UpdateNestedWorkspaceID()
			u.UpdateWorkspaceName()
			u.UpdateWorkspaceType()
			u.UpdateCreatedAt()
			u.UpdateUpdatedAt()
			u.UpdateSandboxType()
			u.UpdateTitleSearch()
			u.UpdateTriggerSourceSearch()
			u.UpdateDriverSearch()
			u.UpdateVMStatusSearch()
			u.UpdateProjectIDSearch()
			u.UpdateWorkspacePathSearch()
			u.UpdateWorkspaceIDSearch()
			u.UpdateNestedWorkspaceIDSearch()
			u.UpdateWorkspaceNameSearch()
			u.UpdateWorkspaceTypeSearch()
		}).
		Exec(ctx); err != nil {
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
	if _, err := x.client.Sandbox.Delete().Where(sandboxdao.IDEQ(id)).Exec(ctx); err != nil {
		return sandboxCacheError("delete sandbox listing cache "+id, err)
	}
	return nil
}

// markComplete records that a full rebuild has finished by stamping the schema
// version. openSandboxCache deliberately leaves the version unset on a rebuild
// so that an interrupted rebuild (crash, shutdown, transient error) is retried
// on the next startup instead of leaving the index permanently missing rows.
func (x *sandboxCache) markComplete(ctx context.Context) error {
	if x == nil || x.db == nil || x.client == nil {
		return nil
	}
	if err := x.client.SandboxProjectionMeta.Create().
		SetID(1).
		SetVersion(sandboxCacheVersion).
		OnConflict(entsql.ConflictColumns(sandboxprojectionmeta.FieldID)).
		Update(func(u *dao.SandboxProjectionMetaUpsert) {
			u.UpdateVersion()
		}).
		Exec(ctx); err != nil {
		return sandboxCacheError("mark sandbox listing cache complete", err)
	}
	return nil
}

const sandboxCacheValidationCols = `id, short_id, title, trigger_source, driver, vm_status, project_id,
	workspace_path, workspace_id, nested_workspace_id, workspace_name, workspace_type, created_at, updated_at,
	sandbox_type, title_search, trigger_source_search, driver_search, vm_status_search, project_id_search,
	workspace_path_search, workspace_id_search, nested_workspace_id_search, workspace_name_search, workspace_type_search`
