package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storedtime"
	"sort"
	"strings"
	"time"
)

// schedulerStore owns scheduler execution state, triggers, runs, and events.
type schedulerStore struct {
	db *sql.DB
}

func (s *schedulerStore) ListSchedulerSummaries(ctx context.Context) ([]domain.SchedulerSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM project_scheduler ORDER BY updated_at DESC, created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query schedulers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schedulers: %w", err)
	}
	items := make([]domain.SchedulerSummary, 0, len(ids))
	for _, id := range ids {
		item, err := s.GetScheduler(ctx, id)
		if err != nil {
			return nil, err
		}
		items = append(items, item.Summary)
	}
	return items, nil
}

func (s *schedulerStore) GetScheduler(ctx context.Context, schedulerID string) (Scheduler, error) {
	return s.loadSchedulerDefinition(ctx, schedulerID)
}

func (s *schedulerStore) ListSchedulers(ctx context.Context) ([]Scheduler, error) {
	summaries, err := s.ListSchedulerSummaries(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]Scheduler, 0, len(summaries))
	for _, summary := range summaries {
		item, err := s.GetScheduler(ctx, summary.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *schedulerStore) hydrateSchedulerSummaryCounts(ctx context.Context, summary *domain.SchedulerSummary) error {
	if summary == nil || strings.TrimSpace(summary.ID) == "" {
		return nil
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM scheduler_trigger WHERE scheduler_id = ?),
		(SELECT COUNT(*) FROM scheduler_run WHERE scheduler_id = ? AND trigger_id <> ''),
		(SELECT COUNT(*) FROM scheduler_event e JOIN scheduler_run r ON r.scheduler_id = e.scheduler_id AND r.run_id = e.scheduler_run_id WHERE e.scheduler_id = ? AND r.trigger_id <> ''),
		(SELECT MAX(started_at) FROM scheduler_run WHERE scheduler_id = ? AND trigger_id <> '')`, summary.ID, summary.ID, summary.ID, summary.ID)
	var triggerCount int
	var runCount int
	var eventCount int
	var latestRunAtRaw any
	if err := row.Scan(&triggerCount, &runCount, &eventCount, &latestRunAtRaw); err != nil {
		return fmt.Errorf("load scheduler summary counts: %w", err)
	}
	summary.TriggerCount = triggerCount
	summary.RunCount = runCount
	summary.EventCount = eventCount
	summary.LatestRunAt = storedtime.ParseStoredTime(latestRunAtRaw)
	return nil
}

func (s *schedulerStore) ReplaceSchedulerTriggers(ctx context.Context, schedulerID string, triggers []domain.SchedulerTrigger) ([]domain.SchedulerTrigger, error) {
	schedulerID = strings.TrimSpace(schedulerID)
	if schedulerID == "" {
		return nil, fmt.Errorf("scheduler id is required")
	}
	existing, err := s.listSchedulerTriggers(ctx, schedulerID)
	if err != nil {
		return nil, err
	}
	existingByID := make(map[string]domain.SchedulerTrigger, len(existing))
	for _, item := range existing {
		existingByID[item.ID] = item
	}

	normalized := make([]domain.SchedulerTrigger, 0, len(triggers))
	seen := make(map[string]struct{}, len(triggers))
	now := time.Now().UTC()
	for _, trigger := range triggers {
		current, err := schedulers.NormalizeSchedulerTrigger(schedulerID, trigger)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[current.ID]; ok {
			return nil, fmt.Errorf("duplicate scheduler trigger id %q", current.ID)
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
			case domain.SchedulerTriggerKindInterval:
				if current.NextFireAt.IsZero() {
					current.NextFireAt = schedulers.TriggerScheduledAt(now, current.IntervalMs)
				}
			case domain.SchedulerTriggerKindTimeout:
				if !sameSchedule {
					current.NextFireAt = schedulers.TriggerScheduledAt(now, current.IntervalMs)
				}
			case domain.SchedulerTriggerKindCron:
				if !sameSchedule || current.NextFireAt.IsZero() {
					nextFireAt, err := schedulers.SchedulerTriggerNextFireAt(now, current, false)
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
		return nil, fmt.Errorf("begin scheduler trigger tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM scheduler_trigger WHERE scheduler_id = ?`, schedulerID); err != nil {
		return nil, fmt.Errorf("reset scheduler triggers: %w", err)
	}
	for _, trigger := range normalized {
		if _, err := tx.ExecContext(ctx, `INSERT INTO scheduler_trigger(
            scheduler_id, trigger_id, kind, topic, interval_ms, enabled, auto_id, spec_json, next_fire_at, last_fired_at
        ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			trigger.SchedulerID,
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
			return nil, fmt.Errorf("insert scheduler trigger %s/%s: %w", schedulerID, trigger.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE project_scheduler SET updated_at = ? WHERE id = ?`, time.Now().UTC().Unix(), schedulerID); err != nil {
		return nil, fmt.Errorf("touch scheduler after trigger replace: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit scheduler trigger tx: %w", err)
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Kind != normalized[j].Kind {
			return normalized[i].Kind < normalized[j].Kind
		}
		return normalized[i].ID < normalized[j].ID
	})
	return normalized, nil
}

func (s *schedulerStore) listSchedulerTriggers(ctx context.Context, schedulerID string) ([]domain.SchedulerTrigger, error) {
	rows, err := s.db.QueryContext(ctx, schedulers.SelectSchedulerTriggerSQL()+` WHERE scheduler_id = ? ORDER BY kind ASC, trigger_id ASC`, schedulerID)
	if err != nil {
		return nil, fmt.Errorf("query scheduler triggers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.SchedulerTrigger, 0)
	for rows.Next() {
		item, err := schedulers.ScanSchedulerTrigger(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scheduler triggers: %w", err)
	}
	return items, nil
}

func (s *schedulerStore) SetSchedulerEnabled(ctx context.Context, schedulerID string, enabled bool) error {
	schedulerID = strings.TrimSpace(schedulerID)
	if schedulerID == "" {
		return fmt.Errorf("scheduler id is required")
	}
	var (
		triggers []domain.SchedulerTrigger
		err      error
	)
	if enabled {
		triggers, err = s.listSchedulerTriggers(ctx, schedulerID)
		if err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin scheduler enable tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `UPDATE project_scheduler SET enabled = ?, updated_at = ? WHERE id = ?`, BoolToInt(enabled), now.Unix(), schedulerID)
	if err != nil {
		return fmt.Errorf("update scheduler enabled state: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.ResourceError(domain.ErrNotFound, "scheduler", schedulerID, fmt.Sprintf("scheduler %s not found", schedulerID), nil)
	}
	if enabled {
		for _, trigger := range triggers {
			if !trigger.Enabled || !schedulers.TriggerUsesSchedule(trigger.Kind) {
				continue
			}
			nextFireAt, err := schedulers.SchedulerTriggerNextFireAt(now, trigger, false)
			if err != nil {
				return fmt.Errorf("schedule scheduler trigger %s/%s: %w", schedulerID, trigger.ID, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE scheduler_trigger SET next_fire_at = ? WHERE scheduler_id = ? AND trigger_id = ?`, domain.NonZeroTimeUnixMilli(nextFireAt), schedulerID, trigger.ID); err != nil {
				return fmt.Errorf("schedule scheduler trigger %s/%s: %w", schedulerID, trigger.ID, err)
			}
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE scheduler_trigger SET next_fire_at = 0 WHERE scheduler_id = ? AND kind IN (?, ?, ?)`, schedulerID, domain.SchedulerTriggerKindInterval, domain.SchedulerTriggerKindTimeout, domain.SchedulerTriggerKindCron); err != nil {
			return fmt.Errorf("pause scheduler triggers: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit scheduler enable tx: %w", err)
	}
	return nil
}

func (s *schedulerStore) SetSchedulerTriggerEnabled(ctx context.Context, schedulerID, triggerID string, enabled bool) error {
	schedulerID = strings.TrimSpace(schedulerID)
	triggerID = strings.TrimSpace(triggerID)
	if schedulerID == "" || triggerID == "" {
		return fmt.Errorf("scheduler trigger id is required")
	}
	row := s.db.QueryRowContext(ctx, schedulers.SelectSchedulerTriggerSQL()+` WHERE scheduler_id = ? AND trigger_id = ?`, schedulerID, triggerID)
	trigger, err := schedulers.ScanSchedulerTrigger(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			id := schedulerID + "/" + triggerID
			return domain.ResourceError(domain.ErrNotFound, "scheduler trigger", id, fmt.Sprintf("scheduler trigger %s not found", id), err)
		}
		return err
	}
	nextFireAt := int64(0)
	if enabled && schedulers.TriggerUsesSchedule(trigger.Kind) {
		scheduledAt, err := schedulers.SchedulerTriggerNextFireAt(time.Now().UTC(), trigger, false)
		if err != nil {
			return fmt.Errorf("schedule scheduler trigger %s/%s: %w", schedulerID, triggerID, err)
		}
		nextFireAt = domain.NonZeroTimeUnixMilli(scheduledAt)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE scheduler_trigger SET enabled = ?, next_fire_at = ? WHERE scheduler_id = ? AND trigger_id = ?`, BoolToInt(enabled), nextFireAt, schedulerID, triggerID)
	if err != nil {
		return fmt.Errorf("update scheduler trigger enabled state: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		id := schedulerID + "/" + triggerID
		return domain.ResourceError(domain.ErrNotFound, "scheduler trigger", id, fmt.Sprintf("scheduler trigger %s not found", id), nil)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE project_scheduler SET updated_at = ? WHERE id = ?`, time.Now().UTC().Unix(), schedulerID)
	return nil
}

func (s *schedulerStore) UpdateSchedulerLastError(ctx context.Context, schedulerID, lastError string) error {
	schedulerID = strings.TrimSpace(schedulerID)
	if schedulerID == "" {
		return fmt.Errorf("scheduler id is required")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE project_scheduler SET last_error = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(lastError), time.Now().UTC().Unix(), schedulerID)
	if err != nil {
		return fmt.Errorf("update scheduler last error: %w", err)
	}
	return nil
}

func (s *schedulerStore) MarkSchedulerTriggerFired(ctx context.Context, schedulerID, triggerID string, lastFiredAt, nextFireAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE scheduler_trigger SET last_fired_at = ?, next_fire_at = ? WHERE scheduler_id = ? AND trigger_id = ?`, domain.NonZeroTimeUnixMilli(lastFiredAt), domain.NonZeroTimeUnixMilli(nextFireAt), strings.TrimSpace(schedulerID), strings.TrimSpace(triggerID))
	if err != nil {
		return fmt.Errorf("update scheduler trigger fire state: %w", err)
	}
	return nil
}

func (s *schedulerStore) SetSchedulerTriggerNextFireAt(ctx context.Context, schedulerID, triggerID string, nextFireAt time.Time) error {
	schedulerID = strings.TrimSpace(schedulerID)
	triggerID = strings.TrimSpace(triggerID)
	if schedulerID == "" || triggerID == "" {
		return fmt.Errorf("scheduler trigger id is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE scheduler_trigger SET next_fire_at = ? WHERE scheduler_id = ? AND trigger_id = ?`, domain.NonZeroTimeUnixMilli(nextFireAt), schedulerID, triggerID)
	if err != nil {
		return fmt.Errorf("update scheduler trigger next fire time: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		id := schedulerID + "/" + triggerID
		return domain.ResourceError(domain.ErrNotFound, "scheduler trigger", id, fmt.Sprintf("scheduler trigger %s not found", id), nil)
	}
	return nil
}

func (s *schedulerStore) CreateSchedulerRun(ctx context.Context, run domain.SchedulerRunSummary) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO scheduler_run(
        scheduler_id, run_id, trigger_id, trigger_kind, trigger_source, status, started_at, completed_at, duration_ms, error, result_json, payload_json, source_script_sha256, artifacts_dir
    ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(run.SchedulerID),
		strings.TrimSpace(run.ID),
		strings.TrimSpace(run.TriggerID),
		strings.TrimSpace(run.TriggerKind),
		strings.TrimSpace(run.TriggerSource),
		schedulers.NormalizeRunStatus(run.Status),
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
		return fmt.Errorf("insert scheduler run %s/%s: %w", run.SchedulerID, run.ID, err)
	}
	return nil
}

func (s *schedulerStore) UpdateSchedulerRun(ctx context.Context, run domain.SchedulerRunSummary) error {
	result, err := s.db.ExecContext(ctx, `UPDATE scheduler_run SET
        trigger_id = ?, trigger_kind = ?, trigger_source = ?, status = ?, started_at = ?, completed_at = ?, duration_ms = ?, error = ?, result_json = ?, payload_json = ?, source_script_sha256 = ?, artifacts_dir = ?
		WHERE scheduler_id = ? AND run_id = ?`,
		strings.TrimSpace(run.TriggerID),
		strings.TrimSpace(run.TriggerKind),
		strings.TrimSpace(run.TriggerSource),
		schedulers.NormalizeRunStatus(run.Status),
		run.StartedAt.UTC().UnixMilli(),
		domain.NonZeroTimeUnixMilli(run.CompletedAt),
		run.DurationMs,
		run.Error,
		run.ResultJSON,
		run.PayloadJSON,
		run.SourceScriptHash,
		strings.TrimSpace(run.ArtifactsDir),
		strings.TrimSpace(run.SchedulerID),
		strings.TrimSpace(run.ID),
	)
	if err != nil {
		return fmt.Errorf("update scheduler run %s/%s: %w", run.SchedulerID, run.ID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		id := strings.TrimSpace(run.SchedulerID) + "/" + strings.TrimSpace(run.ID)
		return domain.ResourceError(domain.ErrNotFound, "scheduler run", id, fmt.Sprintf("scheduler run %s not found", id), nil)
	}
	return nil
}

func (s *schedulerStore) GetSchedulerRun(ctx context.Context, schedulerID, runID string) (domain.SchedulerRunSummary, error) {
	row := s.db.QueryRowContext(ctx, schedulers.SelectSchedulerRunSQL()+` WHERE scheduler_id = ? AND run_id = ?`, strings.TrimSpace(schedulerID), strings.TrimSpace(runID))
	item, err := schedulers.ScanSchedulerRun(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			id := strings.TrimSpace(schedulerID) + "/" + strings.TrimSpace(runID)
			return domain.SchedulerRunSummary{}, domain.ResourceError(domain.ErrNotFound, "scheduler run", id, fmt.Sprintf("scheduler run %s not found", id), err)
		}
		return domain.SchedulerRunSummary{}, err
	}
	return item, nil
}

func (s *schedulerStore) ListSchedulerRuns(ctx context.Context, schedulerID string, limit int) ([]domain.SchedulerRunSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, schedulers.SelectSchedulerRunSQL()+` WHERE scheduler_id = ? ORDER BY started_at DESC, run_id DESC LIMIT ?`, strings.TrimSpace(schedulerID), limit)
	if err != nil {
		return nil, fmt.Errorf("query scheduler runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.SchedulerRunSummary, 0)
	for rows.Next() {
		item, err := schedulers.ScanSchedulerRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scheduler runs: %w", err)
	}
	return items, nil
}

func (s *schedulerStore) ListRecentSchedulerRuns(ctx context.Context, limit int) ([]domain.SchedulerRunSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, schedulers.SelectSchedulerRunSQL()+` ORDER BY started_at DESC, scheduler_id DESC, run_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent scheduler runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.SchedulerRunSummary, 0)
	for rows.Next() {
		item, err := schedulers.ScanSchedulerRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent scheduler runs: %w", err)
	}
	return items, nil
}

func (s *schedulerStore) AddSchedulerEvent(ctx context.Context, event domain.SchedulerEvent) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO scheduler_event(
        scheduler_id, event_id, scheduler_run_id, trigger_id, type, level, message, payload_json, linked_sandbox_id, linked_cell_id, linked_agent_thread_id, created_at
    ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(event.SchedulerID),
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
		return fmt.Errorf("insert scheduler event %s/%s: %w", event.SchedulerID, event.ID, err)
	}
	return nil
}

func (s *schedulerStore) ListSchedulerEvents(ctx context.Context, schedulerID string, limit int) ([]domain.SchedulerEvent, error) {
	return s.ListSchedulerEventsBefore(ctx, SchedulerEventsBeforeCursor{SchedulerID: schedulerID, Limit: limit})
}

// SchedulerEventsBeforeCursor paginates scheduler events strictly before the given
// (created_at, event_id) position; a zero Before lists from the most recent event.
type SchedulerEventsBeforeCursor struct {
	SchedulerID string
	Before      time.Time
	BeforeID    string
	Limit       int
}

func (s *schedulerStore) ListSchedulerEventsBefore(ctx context.Context, cursor SchedulerEventsBeforeCursor) ([]domain.SchedulerEvent, error) {
	schedulerID, before, beforeID, limit := cursor.SchedulerID, cursor.Before, cursor.BeforeID, cursor.Limit
	if limit <= 0 {
		limit = 100
	}
	query := schedulers.SelectSchedulerEventSQL() + ` WHERE scheduler_id = ?`
	args := []any{strings.TrimSpace(schedulerID)}
	if !before.IsZero() {
		query += ` AND (created_at < ? OR (created_at = ? AND event_id < ?))`
		millis := before.UTC().UnixMilli()
		args = append(args, millis, millis, strings.TrimSpace(beforeID))
	}
	query += ` ORDER BY created_at DESC, event_id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query scheduler events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.SchedulerEvent, 0)
	for rows.Next() {
		item, err := schedulers.ScanSchedulerEvent(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scheduler events: %w", err)
	}
	return items, nil
}

func (s *schedulerStore) GetSchedulerState(ctx context.Context, schedulerID, key string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT value_json FROM scheduler_state WHERE scheduler_id = ? AND key = ?`, strings.TrimSpace(schedulerID), strings.TrimSpace(key))
	var value string
	if err := row.Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("query scheduler state: %w", err)
	}
	return value, true, nil
}

func (s *schedulerStore) SetSchedulerState(ctx context.Context, schedulerID, key, valueJSON string) error {
	schedulerID = strings.TrimSpace(schedulerID)
	key = strings.TrimSpace(key)
	if schedulerID == "" || key == "" {
		return fmt.Errorf("scheduler state key is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO scheduler_state(scheduler_id, key, value_json, updated_at) VALUES(?, ?, ?, ?)
        ON CONFLICT(scheduler_id, key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, schedulerID, key, strings.TrimSpace(valueJSON), time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("upsert scheduler state: %w", err)
	}
	return nil
}

func (s *schedulerStore) DeleteSchedulerState(ctx context.Context, schedulerID, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM scheduler_state WHERE scheduler_id = ? AND key = ?`, strings.TrimSpace(schedulerID), strings.TrimSpace(key))
	if err != nil {
		return fmt.Errorf("delete scheduler state: %w", err)
	}
	return nil
}

func (s *schedulerStore) GetSchedulerBinding(ctx context.Context, schedulerID, triggerID string) (domain.SchedulerBinding, bool, error) {
	row := s.db.QueryRowContext(ctx, schedulers.SelectSchedulerBindingSQL()+` WHERE scheduler_id = ? AND trigger_id = ?`, strings.TrimSpace(schedulerID), strings.TrimSpace(triggerID))
	item, err := schedulers.ScanSchedulerBinding(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.SchedulerBinding{}, false, nil
		}
		return domain.SchedulerBinding{}, false, err
	}
	return item, true, nil
}

// GetSchedulerBindingForSandbox reports whether a sandbox is currently bound
// for sticky scheduler reuse. A bound sandbox must not be reclaimed as an
// orphan even when its stopped-runtime policy is remove: the scheduler keeps
// the binding so the next run can resume it.
func (s *schedulerStore) GetSchedulerBindingForSandbox(ctx context.Context, sandboxID string) (domain.SchedulerBinding, bool, error) {
	row := s.db.QueryRowContext(ctx, schedulers.SelectSchedulerBindingSQL()+` WHERE sandbox_id = ? LIMIT 1`, strings.TrimSpace(sandboxID))
	item, err := schedulers.ScanSchedulerBinding(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.SchedulerBinding{}, false, nil
		}
		return domain.SchedulerBinding{}, false, err
	}
	return item, true, nil
}

func (s *schedulerStore) UpsertSchedulerBinding(ctx context.Context, binding domain.SchedulerBinding) error {
	binding.SchedulerID = strings.TrimSpace(binding.SchedulerID)
	binding.TriggerID = strings.TrimSpace(binding.TriggerID)
	binding.SandboxID = strings.TrimSpace(binding.SandboxID)
	binding.SandboxConfigHash = strings.TrimSpace(binding.SandboxConfigHash)
	if binding.SchedulerID == "" || binding.SandboxID == "" {
		return fmt.Errorf("scheduler binding requires scheduler id and sandbox id")
	}
	now := time.Now().UTC()
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = now
	}
	binding.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `INSERT INTO scheduler_sandbox_binding(scheduler_id, trigger_id, sandbox_id, sandbox_config_hash, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(scheduler_id, trigger_id) DO UPDATE SET sandbox_id = excluded.sandbox_id, sandbox_config_hash = excluded.sandbox_config_hash, updated_at = excluded.updated_at`, binding.SchedulerID, binding.TriggerID, binding.SandboxID, binding.SandboxConfigHash, binding.CreatedAt.Unix(), binding.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert scheduler binding: %w", err)
	}
	return nil
}

func (s *schedulerStore) CompareAndSwapSchedulerBinding(ctx context.Context, expected *domain.SchedulerBinding, replacement domain.SchedulerBinding) (bool, error) {
	replacement.SchedulerID = strings.TrimSpace(replacement.SchedulerID)
	replacement.TriggerID = strings.TrimSpace(replacement.TriggerID)
	replacement.SandboxID = strings.TrimSpace(replacement.SandboxID)
	replacement.SandboxConfigHash = strings.TrimSpace(replacement.SandboxConfigHash)
	if replacement.SchedulerID == "" || replacement.SandboxID == "" {
		return false, fmt.Errorf("scheduler binding requires scheduler id and sandbox id")
	}
	now := time.Now().UTC()
	if expected == nil {
		result, err := s.db.ExecContext(ctx, `INSERT INTO scheduler_sandbox_binding(scheduler_id, trigger_id, sandbox_id, sandbox_config_hash, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?) ON CONFLICT(scheduler_id, trigger_id) DO NOTHING`, replacement.SchedulerID, replacement.TriggerID, replacement.SandboxID, replacement.SandboxConfigHash, now.Unix(), now.Unix())
		if err != nil {
			return false, fmt.Errorf("claim scheduler binding: %w", err)
		}
		rows, _ := result.RowsAffected()
		return rows == 1, nil
	}
	if strings.TrimSpace(expected.SchedulerID) != replacement.SchedulerID || strings.TrimSpace(expected.TriggerID) != replacement.TriggerID {
		return false, fmt.Errorf("scheduler binding replacement key does not match expected binding")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE scheduler_sandbox_binding
		SET sandbox_id = ?, sandbox_config_hash = ?, updated_at = ?
		WHERE scheduler_id = ? AND trigger_id = ? AND sandbox_id = ? AND sandbox_config_hash = ?`,
		replacement.SandboxID,
		replacement.SandboxConfigHash,
		now.Unix(),
		replacement.SchedulerID,
		replacement.TriggerID,
		strings.TrimSpace(expected.SandboxID),
		strings.TrimSpace(expected.SandboxConfigHash),
	)
	if err != nil {
		return false, fmt.Errorf("replace scheduler binding: %w", err)
	}
	rows, _ := result.RowsAffected()
	return rows == 1, nil
}
