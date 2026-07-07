package configstore

import (
	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// loaderStore owns loader definitions, triggers, runs, and loader events.
type loaderStore struct {
	db *sql.DB
}

func (s *loaderStore) ensureLoaderSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS loader (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL,
            description TEXT NOT NULL DEFAULT '',
            runtime TEXT NOT NULL DEFAULT 'scheduler',
            script TEXT NOT NULL,
            workspace_id TEXT NOT NULL DEFAULT '',
            agent_id TEXT NOT NULL DEFAULT '',
            driver TEXT NOT NULL DEFAULT '',
            guest_image TEXT NOT NULL DEFAULT '',
            default_agent TEXT NOT NULL DEFAULT 'codex',
            session_policy TEXT NOT NULL DEFAULT 'sticky',
            concurrency_policy TEXT NOT NULL DEFAULT 'skip',
            capset_ids TEXT NOT NULL DEFAULT '[]',
            env_json TEXT NOT NULL DEFAULT '[]',
            enabled INTEGER NOT NULL DEFAULT 1,
            last_error TEXT NOT NULL DEFAULT '',
            created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
            updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
        );`,
		`CREATE TABLE IF NOT EXISTS loader_trigger (
            loader_id TEXT NOT NULL,
            trigger_id TEXT NOT NULL,
            kind TEXT NOT NULL,
            topic TEXT NOT NULL DEFAULT '',
            interval_ms INTEGER NOT NULL DEFAULT 0,
            enabled INTEGER NOT NULL DEFAULT 1,
            auto_id INTEGER NOT NULL DEFAULT 0,
            spec_json TEXT NOT NULL DEFAULT '{}',
            next_fire_at INTEGER NOT NULL DEFAULT 0,
            last_fired_at INTEGER NOT NULL DEFAULT 0,
            PRIMARY KEY(loader_id, trigger_id),
            FOREIGN KEY(loader_id) REFERENCES loader(id) ON DELETE CASCADE
        );`,
		`CREATE INDEX IF NOT EXISTS idx_loader_trigger_schedule ON loader_trigger(enabled, kind, next_fire_at);`,
		`CREATE TABLE IF NOT EXISTS loader_run (
            loader_id TEXT NOT NULL,
            run_id TEXT NOT NULL,
            trigger_id TEXT NOT NULL DEFAULT '',
            trigger_kind TEXT NOT NULL DEFAULT '',
            trigger_source TEXT NOT NULL DEFAULT '',
            status TEXT NOT NULL DEFAULT '',
            started_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
            completed_at INTEGER NOT NULL DEFAULT 0,
            duration_ms INTEGER NOT NULL DEFAULT 0,
            error TEXT NOT NULL DEFAULT '',
            result_json TEXT NOT NULL DEFAULT '',
            payload_json TEXT NOT NULL DEFAULT '',
            source_script_sha256 TEXT NOT NULL DEFAULT '',
            artifacts_dir TEXT NOT NULL DEFAULT '',
            PRIMARY KEY(loader_id, run_id),
            FOREIGN KEY(loader_id) REFERENCES loader(id) ON DELETE CASCADE
        );`,
		`CREATE INDEX IF NOT EXISTS idx_loader_run_started ON loader_run(loader_id, started_at DESC);`,
		`CREATE TABLE IF NOT EXISTS loader_event (
            loader_id TEXT NOT NULL,
            event_id TEXT NOT NULL,
            run_id TEXT NOT NULL DEFAULT '',
            trigger_id TEXT NOT NULL DEFAULT '',
            type TEXT NOT NULL,
            level TEXT NOT NULL DEFAULT 'info',
            message TEXT NOT NULL DEFAULT '',
            payload_json TEXT NOT NULL DEFAULT '',
            linked_session_id TEXT NOT NULL DEFAULT '',
            linked_cell_id TEXT NOT NULL DEFAULT '',
            linked_agent_session_id TEXT NOT NULL DEFAULT '',
            created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
            PRIMARY KEY(loader_id, event_id),
            FOREIGN KEY(loader_id) REFERENCES loader(id) ON DELETE CASCADE
        );`,
		`CREATE INDEX IF NOT EXISTS idx_loader_event_created ON loader_event(loader_id, created_at DESC);`,
		`CREATE TABLE IF NOT EXISTS loader_state (
            loader_id TEXT NOT NULL,
            key TEXT NOT NULL,
            value_json TEXT NOT NULL,
            updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
            PRIMARY KEY(loader_id, key),
            FOREIGN KEY(loader_id) REFERENCES loader(id) ON DELETE CASCADE
        );`,
		`CREATE TABLE IF NOT EXISTS loader_binding (
            loader_id TEXT PRIMARY KEY,
            session_id TEXT NOT NULL,
            created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
            updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
        );`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create loader schema: %w", err)
		}
	}
	if err := s.ensureLoaderAgentIDColumn(ctx); err != nil {
		return err
	}
	if err := s.migrateLoaderTimestampPrecision(ctx); err != nil {
		return err
	}
	if err := s.ensureLoaderCapabilityColumn(ctx); err != nil {
		return err
	}
	if err := s.ensureLoaderManagedColumns(ctx); err != nil {
		return err
	}
	return nil
}

func (s *loaderStore) EnsureLoaderSchema(ctx context.Context) error {
	return s.ensureLoaderSchema(ctx)
}

func (s *loaderStore) ensureLoaderManagedColumns(ctx context.Context) error {
	columns := []struct {
		name       string
		definition string
	}{
		{name: "managed_project_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "managed_project_revision", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "managed_agent_name", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "managed_scheduler_id", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, s.db, "loader", column.name, column.definition); err != nil {
			return fmt.Errorf("ensure loader managed column %s: %w", column.name, err)
		}
	}
	return nil
}

func (s *loaderStore) ensureLoaderCapabilityColumn(ctx context.Context) error {
	columnTypes, err := TableColumnTypes(ctx, s.db, "loader")
	if err != nil {
		return err
	}
	if _, ok := columnTypes["capset_ids"]; ok {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE loader ADD COLUMN capset_ids TEXT NOT NULL DEFAULT '[]'`); err != nil {
		return fmt.Errorf("migrate loader capability column: %w", err)
	}
	return nil
}

func (s *loaderStore) ensureLoaderAgentIDColumn(ctx context.Context) error {
	if err := ensureColumn(ctx, s.db, "loader", "agent_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure loader agent_id column: %w", err)
	}
	return nil
}

func (s *loaderStore) migrateLoaderTimestampPrecision(ctx context.Context) error {
	statements := []string{
		fmt.Sprintf(`UPDATE loader_trigger SET next_fire_at = next_fire_at * 1000 WHERE next_fire_at > 0 AND next_fire_at < %d`, storedUnixMillisecondThreshold),
		fmt.Sprintf(`UPDATE loader_trigger SET last_fired_at = last_fired_at * 1000 WHERE last_fired_at > 0 AND last_fired_at < %d`, storedUnixMillisecondThreshold),
		fmt.Sprintf(`UPDATE loader_run SET started_at = started_at * 1000 WHERE started_at > 0 AND started_at < %d`, storedUnixMillisecondThreshold),
		fmt.Sprintf(`UPDATE loader_run SET completed_at = completed_at * 1000 WHERE completed_at > 0 AND completed_at < %d`, storedUnixMillisecondThreshold),
		fmt.Sprintf(`UPDATE loader_event SET created_at = created_at * 1000 WHERE created_at > 0 AND created_at < %d`, storedUnixMillisecondThreshold),
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate loader timestamp precision: %w", err)
		}
	}
	return nil
}

func (s *loaderStore) CreateLoader(ctx context.Context, item Loader) (Loader, error) {
	normalized, err := loaders.NormalizeLoader(item, true)
	if err != nil {
		return Loader{}, err
	}
	envJSON, err := loaders.EncodeEnvItems(normalized.EnvItems)
	if err != nil {
		return Loader{}, err
	}
	capsetIDsJSON, err := capabilities.EncodeCapsetIDs(normalized.Summary.CapsetIDs)
	if err != nil {
		return Loader{}, err
	}
	now := time.Now().UTC()
	normalized.Summary.CreatedAt = now
	normalized.Summary.UpdatedAt = now
	_, err = s.db.ExecContext(ctx, `INSERT INTO loader(
        id, name, description, runtime, script, workspace_id, agent_id, driver, guest_image, default_agent, session_policy, concurrency_policy, capset_ids, env_json,
        managed_project_id, managed_project_revision, managed_agent_name, managed_scheduler_id, enabled, last_error, created_at, updated_at
    ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		normalized.Summary.ID,
		normalized.Summary.Name,
		normalized.Summary.Description,
		normalized.Summary.Runtime,
		normalized.Script,
		normalized.Summary.WorkspaceID,
		normalized.Summary.AgentID,
		normalized.Summary.Driver,
		normalized.Summary.GuestImage,
		normalized.Summary.DefaultAgent,
		normalized.Summary.SessionPolicy,
		normalized.Summary.ConcurrencyPolicy,
		capsetIDsJSON,
		envJSON,
		normalized.Summary.ManagedProjectID,
		normalized.Summary.ManagedRevision,
		normalized.Summary.ManagedAgentName,
		normalized.Summary.ManagedSchedulerID,
		BoolToInt(normalized.Summary.Enabled),
		normalized.Summary.LastError,
		normalized.Summary.CreatedAt.Unix(),
		normalized.Summary.UpdatedAt.Unix(),
	)
	if err != nil {
		return Loader{}, fmt.Errorf("insert loader %s: %w", normalized.Summary.ID, err)
	}
	return normalized, nil
}

func (s *loaderStore) CreateSchedulerExecution(ctx context.Context, item Loader) (Loader, error) {
	return s.CreateLoader(ctx, item)
}

func (s *loaderStore) UpdateLoader(ctx context.Context, item Loader) (Loader, error) {
	normalized, err := loaders.NormalizeLoader(item, false)
	if err != nil {
		return Loader{}, err
	}
	existing, err := s.GetLoader(ctx, normalized.Summary.ID)
	if err != nil {
		return Loader{}, err
	}
	if normalized.Summary.ManagedProjectID == "" && normalized.Summary.ManagedAgentName == "" && normalized.Summary.ManagedSchedulerID == "" && normalized.Summary.ManagedRevision == 0 {
		normalized.Summary.ManagedProjectID = existing.Summary.ManagedProjectID
		normalized.Summary.ManagedRevision = existing.Summary.ManagedRevision
		normalized.Summary.ManagedAgentName = existing.Summary.ManagedAgentName
		normalized.Summary.ManagedSchedulerID = existing.Summary.ManagedSchedulerID
	}
	envJSON, err := loaders.EncodeEnvItems(normalized.EnvItems)
	if err != nil {
		return Loader{}, err
	}
	capsetIDsJSON, err := capabilities.EncodeCapsetIDs(normalized.Summary.CapsetIDs)
	if err != nil {
		return Loader{}, err
	}
	normalized.Summary.CreatedAt = existing.Summary.CreatedAt
	normalized.Summary.UpdatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE loader SET
        name = ?, description = ?, runtime = ?, script = ?, workspace_id = ?, agent_id = ?, driver = ?, guest_image = ?, default_agent = ?, session_policy = ?,
        concurrency_policy = ?, capset_ids = ?, env_json = ?, managed_project_id = ?, managed_project_revision = ?, managed_agent_name = ?, managed_scheduler_id = ?,
        enabled = ?, last_error = ?, updated_at = ?
        WHERE id = ?`,
		normalized.Summary.Name,
		normalized.Summary.Description,
		normalized.Summary.Runtime,
		normalized.Script,
		normalized.Summary.WorkspaceID,
		normalized.Summary.AgentID,
		normalized.Summary.Driver,
		normalized.Summary.GuestImage,
		normalized.Summary.DefaultAgent,
		normalized.Summary.SessionPolicy,
		normalized.Summary.ConcurrencyPolicy,
		capsetIDsJSON,
		envJSON,
		normalized.Summary.ManagedProjectID,
		normalized.Summary.ManagedRevision,
		normalized.Summary.ManagedAgentName,
		normalized.Summary.ManagedSchedulerID,
		BoolToInt(normalized.Summary.Enabled),
		normalized.Summary.LastError,
		normalized.Summary.UpdatedAt.Unix(),
		normalized.Summary.ID,
	)
	if err != nil {
		return Loader{}, fmt.Errorf("update loader %s: %w", normalized.Summary.ID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return Loader{}, domain.ResourceError(domain.ErrNotFound, "loader", normalized.Summary.ID, fmt.Sprintf("loader %s not found", normalized.Summary.ID), nil)
	}
	normalized.Summary.TriggerCount = existing.Summary.TriggerCount
	normalized.Summary.RunCount = existing.Summary.RunCount
	normalized.Summary.EventCount = existing.Summary.EventCount
	normalized.Summary.LatestRunAt = existing.Summary.LatestRunAt
	return normalized, nil
}

func (s *loaderStore) UpdateSchedulerExecution(ctx context.Context, item Loader) (Loader, error) {
	return s.UpdateLoader(ctx, item)
}

func (s *loaderStore) UpsertManagedLoader(ctx context.Context, item Loader) (Loader, error) {
	normalized, err := loaders.NormalizeLoader(item, false)
	if err != nil {
		return Loader{}, err
	}
	if normalized.Summary.ManagedProjectID == "" || normalized.Summary.ManagedAgentName == "" || normalized.Summary.ManagedSchedulerID == "" {
		return Loader{}, fmt.Errorf("managed project id, agent name, and scheduler id are required")
	}
	if existing, found, err := s.getLoaderIfExists(ctx, normalized.Summary.ID); err != nil {
		return Loader{}, err
	} else if found {
		normalized.Summary.CreatedAt = existing.Summary.CreatedAt
		return s.UpdateLoader(ctx, normalized)
	}
	return s.CreateLoader(ctx, normalized)
}

func (s *loaderStore) UpsertSchedulerExecution(ctx context.Context, item Loader) (Loader, error) {
	return s.UpsertManagedLoader(ctx, item)
}

func (s *loaderStore) getLoaderIfExists(ctx context.Context, loaderID string) (Loader, bool, error) {
	item, err := s.GetLoader(ctx, loaderID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Loader{}, false, nil
		}
		return Loader{}, false, err
	}
	return item, true, nil
}

func (s *loaderStore) GetLoaderIfExists(ctx context.Context, loaderID string) (Loader, bool, error) {
	return s.getLoaderIfExists(ctx, loaderID)
}

func (s *loaderStore) GetSchedulerExecutionIfExists(ctx context.Context, executionID string) (Loader, bool, error) {
	return s.getLoaderIfExists(ctx, executionID)
}

func (s *loaderStore) DeleteLoader(ctx context.Context, loaderID string) error {
	loaderID = strings.TrimSpace(loaderID)
	if loaderID == "" {
		return fmt.Errorf("loader id is required")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM loader WHERE id = ?`, loaderID)
	if err != nil {
		return fmt.Errorf("delete loader %s: %w", loaderID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.ResourceError(domain.ErrNotFound, "loader", loaderID, fmt.Sprintf("loader %s not found", loaderID), nil)
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM loader_binding WHERE loader_id = ?`, loaderID)
	return nil
}

func (s *loaderStore) DeleteSchedulerExecution(ctx context.Context, executionID string) error {
	return s.DeleteLoader(ctx, executionID)
}

func (s *loaderStore) DisableLoadersByDefaultAgent(ctx context.Context, agentID string) (int, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return 0, fmt.Errorf("agent id is required")
	}
	now := time.Now().UTC().Unix()
	result, err := s.db.ExecContext(ctx, `UPDATE loader SET enabled = 0, updated_at = ? WHERE (agent_id = ? OR default_agent = ?) AND enabled = 1`, now, agentID, agentID)
	if err != nil {
		return 0, fmt.Errorf("disable loaders for agent %s: %w", agentID, err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}

func (s *loaderStore) ListLoaderSummaries(ctx context.Context) ([]domain.LoaderSummary, error) {
	rows, err := s.db.QueryContext(ctx, loaders.SelectLoaderSummarySQL()+`
        ORDER BY l.updated_at DESC, l.created_at DESC, l.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query loaders: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.LoaderSummary, 0)
	for rows.Next() {
		item, err := loaders.ScanLoaderSummary(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate loaders: %w", err)
	}
	return items, nil
}

func (s *loaderStore) GetLoader(ctx context.Context, loaderID string) (Loader, error) {
	loaderID = strings.TrimSpace(loaderID)
	if loaderID == "" {
		return Loader{}, fmt.Errorf("loader id is required")
	}
	row := s.db.QueryRowContext(ctx, loaders.SelectLoaderSQL()+` WHERE id = ?`, loaderID)
	item, err := loaders.ScanLoader(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Loader{}, domain.ResourceError(domain.ErrNotFound, "loader", loaderID, fmt.Sprintf("loader %s not found", loaderID), err)
		}
		return Loader{}, err
	}
	if err := s.hydrateLoaderSummaryCounts(ctx, &item.Summary); err != nil {
		return Loader{}, err
	}
	triggers, err := s.listLoaderTriggers(ctx, loaderID)
	if err != nil {
		return Loader{}, err
	}
	item.Triggers = triggers
	return item, nil
}

func (s *loaderStore) GetSchedulerExecution(ctx context.Context, executionID string) (Loader, error) {
	return s.GetLoader(ctx, executionID)
}

func (s *loaderStore) ListLoaders(ctx context.Context) ([]Loader, error) {
	summaries, err := s.ListLoaderSummaries(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]Loader, 0, len(summaries))
	for _, summary := range summaries {
		item, err := s.GetLoader(ctx, summary.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *loaderStore) ListSchedulerExecutions(ctx context.Context) ([]Loader, error) {
	return s.ListLoaders(ctx)
}

func (s *loaderStore) ListManagedLoaders(ctx context.Context, projectID string) ([]Loader, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM loader WHERE managed_project_id = ? ORDER BY managed_agent_name ASC, managed_scheduler_id ASC, id ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query managed loaders %s: %w", projectID, err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan managed loader id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed loaders %s: %w", projectID, err)
	}
	items := make([]Loader, 0, len(ids))
	for _, id := range ids {
		item, err := s.GetLoader(ctx, id)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *loaderStore) hydrateLoaderSummaryCounts(ctx context.Context, summary *domain.LoaderSummary) error {
	if summary == nil || strings.TrimSpace(summary.ID) == "" {
		return nil
	}
	row := s.db.QueryRowContext(ctx, `SELECT
        (SELECT COUNT(*) FROM loader_trigger WHERE loader_id = ?),
        (SELECT COUNT(*) FROM loader_run WHERE loader_id = ?),
        (SELECT COUNT(*) FROM loader_event WHERE loader_id = ?),
        (SELECT MAX(started_at) FROM loader_run WHERE loader_id = ?)`, summary.ID, summary.ID, summary.ID, summary.ID)
	var triggerCount int
	var runCount int
	var eventCount int
	var latestRunAtRaw any
	if err := row.Scan(&triggerCount, &runCount, &eventCount, &latestRunAtRaw); err != nil {
		return fmt.Errorf("load loader summary counts: %w", err)
	}
	summary.TriggerCount = triggerCount
	summary.RunCount = runCount
	summary.EventCount = eventCount
	summary.LatestRunAt = ParseStoredTime(latestRunAtRaw)
	return nil
}

func (s *loaderStore) ReplaceLoaderTriggers(ctx context.Context, loaderID string, triggers []domain.LoaderTrigger) ([]domain.LoaderTrigger, error) {
	loaderID = strings.TrimSpace(loaderID)
	if loaderID == "" {
		return nil, fmt.Errorf("loader id is required")
	}
	existing, err := s.listLoaderTriggers(ctx, loaderID)
	if err != nil {
		return nil, err
	}
	existingByID := make(map[string]domain.LoaderTrigger, len(existing))
	for _, item := range existing {
		existingByID[item.ID] = item
	}

	normalized := make([]domain.LoaderTrigger, 0, len(triggers))
	seen := make(map[string]struct{}, len(triggers))
	now := time.Now().UTC()
	for _, trigger := range triggers {
		current, err := loaders.NormalizeLoaderTrigger(loaderID, trigger)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[current.ID]; ok {
			return nil, fmt.Errorf("duplicate loader trigger id %q", current.ID)
		}
		seen[current.ID] = struct{}{}
		sameSchedule := false
		if previous, ok := existingByID[current.ID]; ok {
			current.Enabled = previous.Enabled
			current.LastFiredAt = previous.LastFiredAt
			if current.Kind == previous.Kind && current.Topic == previous.Topic && current.IntervalMs == previous.IntervalMs && current.SpecJSON == previous.SpecJSON {
				current.NextFireAt = previous.NextFireAt
				sameSchedule = true
			}
		}
		if !current.Enabled {
			current.NextFireAt = time.Time{}
		} else {
			switch current.Kind {
			case domain.LoaderTriggerKindInterval:
				if current.NextFireAt.IsZero() {
					current.NextFireAt = domain.LoaderTriggerScheduledAt(now, current.IntervalMs)
				}
			case domain.LoaderTriggerKindTimeout:
				if !sameSchedule {
					current.NextFireAt = domain.LoaderTriggerScheduledAt(now, current.IntervalMs)
				}
			case domain.LoaderTriggerKindCron:
				if !sameSchedule || current.NextFireAt.IsZero() {
					nextFireAt, err := loaders.LoaderTriggerNextFireAt(now, current, false)
					if err != nil {
						return nil, err
					}
					current.NextFireAt = nextFireAt
				}
			}
		}
		normalized = append(normalized, current)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin loader trigger tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM loader_trigger WHERE loader_id = ?`, loaderID); err != nil {
		return nil, fmt.Errorf("reset loader triggers: %w", err)
	}
	for _, trigger := range normalized {
		if _, err := tx.ExecContext(ctx, `INSERT INTO loader_trigger(
            loader_id, trigger_id, kind, topic, interval_ms, enabled, auto_id, spec_json, next_fire_at, last_fired_at
        ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			trigger.LoaderID,
			trigger.ID,
			trigger.Kind,
			trigger.Topic,
			trigger.IntervalMs,
			BoolToInt(trigger.Enabled),
			BoolToInt(trigger.AutoID),
			trigger.SpecJSON,
			domain.NonZeroTimeUnixMilli(trigger.NextFireAt),
			domain.NonZeroTimeUnixMilli(trigger.LastFiredAt),
		); err != nil {
			return nil, fmt.Errorf("insert loader trigger %s/%s: %w", loaderID, trigger.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE loader SET updated_at = ? WHERE id = ?`, time.Now().UTC().Unix(), loaderID); err != nil {
		return nil, fmt.Errorf("touch loader after trigger replace: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit loader trigger tx: %w", err)
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Kind != normalized[j].Kind {
			return normalized[i].Kind < normalized[j].Kind
		}
		return normalized[i].ID < normalized[j].ID
	})
	return normalized, nil
}

func (s *loaderStore) ReplaceSchedulerExecutionTriggers(ctx context.Context, executionID string, triggers []domain.LoaderTrigger) ([]domain.LoaderTrigger, error) {
	return s.ReplaceLoaderTriggers(ctx, executionID, triggers)
}

func (s *loaderStore) listLoaderTriggers(ctx context.Context, loaderID string) ([]domain.LoaderTrigger, error) {
	rows, err := s.db.QueryContext(ctx, loaders.SelectLoaderTriggerSQL()+` WHERE loader_id = ? ORDER BY kind ASC, trigger_id ASC`, loaderID)
	if err != nil {
		return nil, fmt.Errorf("query loader triggers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.LoaderTrigger, 0)
	for rows.Next() {
		item, err := loaders.ScanLoaderTrigger(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate loader triggers: %w", err)
	}
	return items, nil
}

func (s *loaderStore) SetLoaderEnabled(ctx context.Context, loaderID string, enabled bool) error {
	loaderID = strings.TrimSpace(loaderID)
	if loaderID == "" {
		return fmt.Errorf("loader id is required")
	}
	var (
		triggers []domain.LoaderTrigger
		err      error
	)
	if enabled {
		triggers, err = s.listLoaderTriggers(ctx, loaderID)
		if err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin loader enable tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `UPDATE loader SET enabled = ?, updated_at = ? WHERE id = ?`, BoolToInt(enabled), now.Unix(), loaderID)
	if err != nil {
		return fmt.Errorf("update loader enabled state: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.ResourceError(domain.ErrNotFound, "loader", loaderID, fmt.Sprintf("loader %s not found", loaderID), nil)
	}
	if enabled {
		for _, trigger := range triggers {
			if !trigger.Enabled || !domain.LoaderTriggerUsesSchedule(trigger.Kind) {
				continue
			}
			nextFireAt, err := loaders.LoaderTriggerNextFireAt(now, trigger, false)
			if err != nil {
				return fmt.Errorf("schedule loader trigger %s/%s: %w", loaderID, trigger.ID, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE loader_trigger SET next_fire_at = ? WHERE loader_id = ? AND trigger_id = ?`, domain.NonZeroTimeUnixMilli(nextFireAt), loaderID, trigger.ID); err != nil {
				return fmt.Errorf("schedule loader trigger %s/%s: %w", loaderID, trigger.ID, err)
			}
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE loader_trigger SET next_fire_at = 0 WHERE loader_id = ? AND kind IN (?, ?, ?)`, loaderID, domain.LoaderTriggerKindInterval, domain.LoaderTriggerKindTimeout, domain.LoaderTriggerKindCron); err != nil {
			return fmt.Errorf("pause loader scheduled triggers: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit loader enable tx: %w", err)
	}
	return nil
}

func (s *loaderStore) SetSchedulerExecutionEnabled(ctx context.Context, executionID string, enabled bool) error {
	return s.SetLoaderEnabled(ctx, executionID, enabled)
}

func (s *loaderStore) SetLoaderTriggerEnabled(ctx context.Context, loaderID, triggerID string, enabled bool) error {
	loaderID = strings.TrimSpace(loaderID)
	triggerID = strings.TrimSpace(triggerID)
	if loaderID == "" || triggerID == "" {
		return fmt.Errorf("loader trigger id is required")
	}
	row := s.db.QueryRowContext(ctx, loaders.SelectLoaderTriggerSQL()+` WHERE loader_id = ? AND trigger_id = ?`, loaderID, triggerID)
	trigger, err := loaders.ScanLoaderTrigger(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			id := loaderID + "/" + triggerID
			return domain.ResourceError(domain.ErrNotFound, "loader trigger", id, fmt.Sprintf("loader trigger %s not found", id), err)
		}
		return err
	}
	nextFireAt := int64(0)
	if enabled && domain.LoaderTriggerUsesSchedule(trigger.Kind) {
		scheduledAt, err := loaders.LoaderTriggerNextFireAt(time.Now().UTC(), trigger, false)
		if err != nil {
			return fmt.Errorf("schedule loader trigger %s/%s: %w", loaderID, triggerID, err)
		}
		nextFireAt = domain.NonZeroTimeUnixMilli(scheduledAt)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE loader_trigger SET enabled = ?, next_fire_at = ? WHERE loader_id = ? AND trigger_id = ?`, BoolToInt(enabled), nextFireAt, loaderID, triggerID)
	if err != nil {
		return fmt.Errorf("update loader trigger enabled state: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		id := loaderID + "/" + triggerID
		return domain.ResourceError(domain.ErrNotFound, "loader trigger", id, fmt.Sprintf("loader trigger %s not found", id), nil)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE loader SET updated_at = ? WHERE id = ?`, time.Now().UTC().Unix(), loaderID)
	return nil
}

func (s *loaderStore) SetSchedulerExecutionTriggerEnabled(ctx context.Context, executionID, triggerID string, enabled bool) error {
	return s.SetLoaderTriggerEnabled(ctx, executionID, triggerID, enabled)
}

func (s *loaderStore) UpdateLoaderLastError(ctx context.Context, loaderID, lastError string) error {
	loaderID = strings.TrimSpace(loaderID)
	if loaderID == "" {
		return fmt.Errorf("loader id is required")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE loader SET last_error = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(lastError), time.Now().UTC().Unix(), loaderID)
	if err != nil {
		return fmt.Errorf("update loader last error: %w", err)
	}
	return nil
}

func (s *loaderStore) MarkLoaderTriggerFired(ctx context.Context, loaderID, triggerID string, lastFiredAt, nextFireAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE loader_trigger SET last_fired_at = ?, next_fire_at = ? WHERE loader_id = ? AND trigger_id = ?`, domain.NonZeroTimeUnixMilli(lastFiredAt), domain.NonZeroTimeUnixMilli(nextFireAt), strings.TrimSpace(loaderID), strings.TrimSpace(triggerID))
	if err != nil {
		return fmt.Errorf("update loader trigger fire state: %w", err)
	}
	return nil
}

func (s *loaderStore) CreateLoaderRun(ctx context.Context, run domain.LoaderRunSummary) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO loader_run(
        loader_id, run_id, trigger_id, trigger_kind, trigger_source, status, started_at, completed_at, duration_ms, error, result_json, payload_json, source_script_sha256, artifacts_dir
    ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(run.LoaderID),
		strings.TrimSpace(run.ID),
		strings.TrimSpace(run.TriggerID),
		strings.TrimSpace(run.TriggerKind),
		strings.TrimSpace(run.TriggerSource),
		domain.NormalizeLoaderRunStatus(run.Status),
		run.StartedAt.UTC().UnixMilli(),
		domain.NonZeroTimeUnixMilli(run.CompletedAt),
		run.DurationMs,
		run.Error,
		run.ResultJSON,
		run.PayloadJSON,
		run.SourceScriptHash,
		strings.TrimSpace(run.ArtifactsDir),
	)
	if err != nil {
		return fmt.Errorf("insert loader run %s/%s: %w", run.LoaderID, run.ID, err)
	}
	return nil
}

func (s *loaderStore) UpdateLoaderRun(ctx context.Context, run domain.LoaderRunSummary) error {
	result, err := s.db.ExecContext(ctx, `UPDATE loader_run SET
        trigger_id = ?, trigger_kind = ?, trigger_source = ?, status = ?, started_at = ?, completed_at = ?, duration_ms = ?, error = ?, result_json = ?, payload_json = ?, source_script_sha256 = ?, artifacts_dir = ?
        WHERE loader_id = ? AND run_id = ?`,
		strings.TrimSpace(run.TriggerID),
		strings.TrimSpace(run.TriggerKind),
		strings.TrimSpace(run.TriggerSource),
		domain.NormalizeLoaderRunStatus(run.Status),
		run.StartedAt.UTC().UnixMilli(),
		domain.NonZeroTimeUnixMilli(run.CompletedAt),
		run.DurationMs,
		run.Error,
		run.ResultJSON,
		run.PayloadJSON,
		run.SourceScriptHash,
		strings.TrimSpace(run.ArtifactsDir),
		strings.TrimSpace(run.LoaderID),
		strings.TrimSpace(run.ID),
	)
	if err != nil {
		return fmt.Errorf("update loader run %s/%s: %w", run.LoaderID, run.ID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		id := strings.TrimSpace(run.LoaderID) + "/" + strings.TrimSpace(run.ID)
		return domain.ResourceError(domain.ErrNotFound, "loader run", id, fmt.Sprintf("loader run %s not found", id), nil)
	}
	return nil
}

func (s *loaderStore) GetLoaderRun(ctx context.Context, loaderID, runID string) (domain.LoaderRunSummary, error) {
	row := s.db.QueryRowContext(ctx, loaders.SelectLoaderRunSQL()+` WHERE loader_id = ? AND run_id = ?`, strings.TrimSpace(loaderID), strings.TrimSpace(runID))
	item, err := loaders.ScanLoaderRun(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			id := strings.TrimSpace(loaderID) + "/" + strings.TrimSpace(runID)
			return domain.LoaderRunSummary{}, domain.ResourceError(domain.ErrNotFound, "loader run", id, fmt.Sprintf("loader run %s not found", id), err)
		}
		return domain.LoaderRunSummary{}, err
	}
	return item, nil
}

func (s *loaderStore) ListLoaderRuns(ctx context.Context, loaderID string, limit int) ([]domain.LoaderRunSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, loaders.SelectLoaderRunSQL()+` WHERE loader_id = ? ORDER BY started_at DESC, run_id DESC LIMIT ?`, strings.TrimSpace(loaderID), limit)
	if err != nil {
		return nil, fmt.Errorf("query loader runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.LoaderRunSummary, 0)
	for rows.Next() {
		item, err := loaders.ScanLoaderRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate loader runs: %w", err)
	}
	return items, nil
}

func (s *loaderStore) ListRecentLoaderRuns(ctx context.Context, limit int) ([]domain.LoaderRunSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, loaders.SelectLoaderRunSQL()+` ORDER BY started_at DESC, loader_id DESC, run_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent loader runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.LoaderRunSummary, 0)
	for rows.Next() {
		item, err := loaders.ScanLoaderRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent loader runs: %w", err)
	}
	return items, nil
}

func (s *loaderStore) AddLoaderEvent(ctx context.Context, event domain.LoaderEvent) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO loader_event(
        loader_id, event_id, run_id, trigger_id, type, level, message, payload_json, linked_session_id, linked_cell_id, linked_agent_session_id, created_at
    ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(event.LoaderID),
		strings.TrimSpace(event.ID),
		strings.TrimSpace(event.RunID),
		strings.TrimSpace(event.TriggerID),
		strings.TrimSpace(event.Type),
		strings.TrimSpace(event.Level),
		strings.TrimSpace(event.Message),
		strings.TrimSpace(event.PayloadJSON),
		strings.TrimSpace(event.LinkedSessionID),
		strings.TrimSpace(event.LinkedCellID),
		strings.TrimSpace(event.LinkedAgentSessionID),
		event.CreatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("insert loader event %s/%s: %w", event.LoaderID, event.ID, err)
	}
	return nil
}

func (s *loaderStore) AddSchedulerExecutionEvent(ctx context.Context, event domain.LoaderEvent) error {
	return s.AddLoaderEvent(ctx, event)
}

func (s *loaderStore) ListLoaderEvents(ctx context.Context, loaderID string, limit int) ([]domain.LoaderEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, loaders.SelectLoaderEventSQL()+` WHERE loader_id = ? ORDER BY created_at DESC, event_id DESC LIMIT ?`, strings.TrimSpace(loaderID), limit)
	if err != nil {
		return nil, fmt.Errorf("query loader events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.LoaderEvent, 0)
	for rows.Next() {
		item, err := loaders.ScanLoaderEvent(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate loader events: %w", err)
	}
	return items, nil
}

func (s *loaderStore) GetLoaderState(ctx context.Context, loaderID, key string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT value_json FROM loader_state WHERE loader_id = ? AND key = ?`, strings.TrimSpace(loaderID), strings.TrimSpace(key))
	var value string
	if err := row.Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("query loader state: %w", err)
	}
	return value, true, nil
}

func (s *loaderStore) SetLoaderState(ctx context.Context, loaderID, key, valueJSON string) error {
	loaderID = strings.TrimSpace(loaderID)
	key = strings.TrimSpace(key)
	if loaderID == "" || key == "" {
		return fmt.Errorf("loader state key is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO loader_state(loader_id, key, value_json, updated_at) VALUES(?, ?, ?, ?)
        ON CONFLICT(loader_id, key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, loaderID, key, strings.TrimSpace(valueJSON), time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("upsert loader state: %w", err)
	}
	return nil
}

func (s *loaderStore) DeleteLoaderState(ctx context.Context, loaderID, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM loader_state WHERE loader_id = ? AND key = ?`, strings.TrimSpace(loaderID), strings.TrimSpace(key))
	if err != nil {
		return fmt.Errorf("delete loader state: %w", err)
	}
	return nil
}

func (s *loaderStore) GetLoaderBinding(ctx context.Context, loaderID string) (domain.LoaderBinding, bool, error) {
	row := s.db.QueryRowContext(ctx, loaders.SelectLoaderBindingSQL()+` WHERE loader_id = ?`, strings.TrimSpace(loaderID))
	item, err := loaders.ScanLoaderBinding(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.LoaderBinding{}, false, nil
		}
		return domain.LoaderBinding{}, false, err
	}
	return item, true, nil
}

func (s *loaderStore) UpsertLoaderBinding(ctx context.Context, binding domain.LoaderBinding) error {
	binding.LoaderID = strings.TrimSpace(binding.LoaderID)
	binding.SessionID = strings.TrimSpace(binding.SessionID)
	if binding.LoaderID == "" || binding.SessionID == "" {
		return fmt.Errorf("loader binding requires loader id and session id")
	}
	now := time.Now().UTC()
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = now
	}
	binding.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `INSERT INTO loader_binding(loader_id, session_id, created_at, updated_at) VALUES(?, ?, ?, ?)
        ON CONFLICT(loader_id) DO UPDATE SET session_id = excluded.session_id, updated_at = excluded.updated_at`, binding.LoaderID, binding.SessionID, binding.CreatedAt.Unix(), binding.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert loader binding: %w", err)
	}
	return nil
}
