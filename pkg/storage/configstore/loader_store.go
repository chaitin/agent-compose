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

func (s *loaderStore) CreateLoader(ctx context.Context, item Loader) (Loader, error) {
	normalized, err := loaders.NormalizeLoader(item, true)
	if err != nil {
		return Loader{}, err
	}
	envJSON, err := loaders.EncodeEnvItems(normalized.EnvItems)
	if err != nil {
		return Loader{}, err
	}
	volumesJSON, err := loaders.EncodeVolumeMountSpecs(normalized.Volumes)
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
        id, name, description, runtime, script, workspace_id, agent_id, driver, guest_image, default_agent, sandbox_policy, concurrency_policy, capset_ids, env_json, volumes_json,
        managed_project_id, managed_project_revision, managed_agent_name, managed_scheduler_id, enabled, last_error, created_at, updated_at
    ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
		normalized.Summary.SandboxPolicy,
		normalized.Summary.ConcurrencyPolicy,
		capsetIDsJSON,
		envJSON,
		volumesJSON,
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
	volumesJSON, err := loaders.EncodeVolumeMountSpecs(normalized.Volumes)
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
        name = ?, description = ?, runtime = ?, script = ?, workspace_id = ?, agent_id = ?, driver = ?, guest_image = ?, default_agent = ?, sandbox_policy = ?,
        concurrency_policy = ?, capset_ids = ?, env_json = ?, volumes_json = ?, managed_project_id = ?, managed_project_revision = ?, managed_agent_name = ?, managed_scheduler_id = ?,
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
		normalized.Summary.SandboxPolicy,
		normalized.Summary.ConcurrencyPolicy,
		capsetIDsJSON,
		envJSON,
		volumesJSON,
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
		(SELECT COUNT(*) FROM loader_run WHERE loader_id = ? AND trigger_id <> ''),
		(SELECT COUNT(*) FROM loader_event e JOIN loader_run r ON r.loader_id = e.loader_id AND r.run_id = e.run_id WHERE e.loader_id = ? AND r.trigger_id <> ''),
		(SELECT MAX(started_at) FROM loader_run WHERE loader_id = ? AND trigger_id <> '')`, summary.ID, summary.ID, summary.ID, summary.ID)
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
        loader_id, event_id, run_id, trigger_id, type, level, message, payload_json, linked_sandbox_id, linked_cell_id, linked_agent_thread_id, created_at
    ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(event.LoaderID),
		strings.TrimSpace(event.ID),
		strings.TrimSpace(event.RunID),
		strings.TrimSpace(event.TriggerID),
		strings.TrimSpace(event.Type),
		strings.TrimSpace(event.Level),
		strings.TrimSpace(event.Message),
		strings.TrimSpace(event.PayloadJSON),
		strings.TrimSpace(event.LinkedSandboxID),
		strings.TrimSpace(event.LinkedCellID),
		strings.TrimSpace(event.LinkedAgentThreadID),
		event.CreatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("insert loader event %s/%s: %w", event.LoaderID, event.ID, err)
	}
	return nil
}

func (s *loaderStore) ListLoaderEvents(ctx context.Context, loaderID string, limit int) ([]domain.LoaderEvent, error) {
	return s.ListLoaderEventsBefore(ctx, loaderID, time.Time{}, "", limit)
}

func (s *loaderStore) ListLoaderEventsBefore(ctx context.Context, loaderID string, before time.Time, beforeID string, limit int) ([]domain.LoaderEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	query := loaders.SelectLoaderEventSQL() + ` WHERE loader_id = ?`
	args := []any{strings.TrimSpace(loaderID)}
	if !before.IsZero() {
		query += ` AND (created_at < ? OR (created_at = ? AND event_id < ?))`
		millis := before.UTC().UnixMilli()
		args = append(args, millis, millis, strings.TrimSpace(beforeID))
	}
	query += ` ORDER BY created_at DESC, event_id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
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

func (s *loaderStore) GetLoaderBinding(ctx context.Context, loaderID, triggerID string) (domain.LoaderBinding, bool, error) {
	row := s.db.QueryRowContext(ctx, loaders.SelectLoaderBindingSQL()+` WHERE loader_id = ? AND trigger_id = ?`, strings.TrimSpace(loaderID), strings.TrimSpace(triggerID))
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
	binding.TriggerID = strings.TrimSpace(binding.TriggerID)
	binding.SandboxID = strings.TrimSpace(binding.SandboxID)
	if binding.LoaderID == "" || binding.SandboxID == "" {
		return fmt.Errorf("loader binding requires loader id and sandbox id")
	}
	now := time.Now().UTC()
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = now
	}
	binding.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `INSERT INTO loader_binding(loader_id, trigger_id, sandbox_id, created_at, updated_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(loader_id, trigger_id) DO UPDATE SET sandbox_id = excluded.sandbox_id, updated_at = excluded.updated_at`, binding.LoaderID, binding.TriggerID, binding.SandboxID, binding.CreatedAt.Unix(), binding.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert loader binding: %w", err)
	}
	return nil
}
