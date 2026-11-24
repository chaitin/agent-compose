package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/predicate"
	"github.com/chaitin/agent-compose/ent/dao/schedulerevent"
	"github.com/chaitin/agent-compose/ent/dao/schedulerrun"
	"github.com/chaitin/agent-compose/pkg/idset"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

func (s *schedulerStore) GetSchedulerRunForSchedulers(ctx context.Context, schedulerIDs []string, runID string) (domain.SchedulerRunSummary, error) {
	schedulerIDs = idset.Normalize(schedulerIDs)
	runID = strings.TrimSpace(runID)
	if len(schedulerIDs) == 0 {
		return domain.SchedulerRunSummary{}, schedulerRunPageNotFound(runID, nil)
	}
	query := s.client.SchedulerRun.Query().Where(
		schedulerrun.SchedulerIDIn(schedulerIDs...),
		schedulerrun.TriggerIDNEQ(""),
	)
	if match, ok := parseResourceIDMatch(runID); ok {
		if match.exact {
			query = query.Where(schedulerrun.Or(
				schedulerrun.IDEQ(match.values[0]),
				schedulerrun.IDEQ(match.values[1]),
			))
		} else {
			query = query.Where(schedulerrun.Or(
				schedulerrun.And(schedulerrun.IDGTE(match.values[0]), schedulerrun.IDLT(match.values[1])),
				schedulerrun.And(schedulerrun.IDGTE(match.values[2]), schedulerrun.IDLT(match.values[3])),
			))
		}
	} else {
		query = query.Where(schedulerrun.IDEQ(runID))
	}
	entities, err := query.
		Order(schedulerrun.BySchedulerID(), schedulerrun.ByID()).
		Limit(2).
		All(ctx)
	if err != nil {
		return domain.SchedulerRunSummary{}, err
	}
	if len(entities) == 0 {
		return domain.SchedulerRunSummary{}, schedulerRunPageNotFound(runID, sql.ErrNoRows)
	}
	if len(entities) > 1 {
		return domain.SchedulerRunSummary{}, domain.ResourceError(domain.ErrAmbiguous, "scheduler run", runID, fmt.Sprintf("scheduler run reference %s is ambiguous", runID), nil)
	}
	return schedulerRunFromEntity(entities[0]), nil
}

func schedulerRunPageNotFound(runID string, cause error) error {
	return domain.ResourceError(domain.ErrNotFound, "scheduler run", runID, fmt.Sprintf("scheduler run %s not found", runID), cause)
}

func (s *schedulerStore) ListSchedulerRunsPage(ctx context.Context, filter schedulers.SchedulerRunPageFilter) ([]domain.SchedulerRunSummary, error) {
	schedulerIDs := idset.Normalize(filter.SchedulerIDs)
	if len(schedulerIDs) == 0 {
		return []domain.SchedulerRunSummary{}, nil
	}
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	query := s.client.SchedulerRun.Query().
		Where(schedulerrun.SchedulerIDIn(schedulerIDs...))
	if filter.RequireTrigger {
		query = query.Where(schedulerrun.TriggerIDNEQ(""))
	}
	if triggerID := strings.TrimSpace(filter.TriggerID); triggerID != "" {
		query = query.Where(schedulerrun.TriggerIDEQ(triggerID))
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where(schedulerrun.StatusEQ(status))
	}
	if !filter.BeforeStartedAt.IsZero() {
		beforeMillis := filter.BeforeStartedAt.UTC().UnixMilli()
		query = query.Where(schedulerrun.Or(
			schedulerrun.StartedAtLT(beforeMillis),
			schedulerrun.And(
				schedulerrun.StartedAtEQ(beforeMillis),
				schedulerrun.Or(
					schedulerrun.SchedulerIDLT(strings.TrimSpace(filter.BeforeSchedulerID)),
					schedulerrun.And(
						schedulerrun.SchedulerIDEQ(strings.TrimSpace(filter.BeforeSchedulerID)),
						schedulerrun.IDLT(strings.TrimSpace(filter.BeforeRunID)),
					),
				),
			),
		))
	}
	entities, err := query.
		Order(dao.Desc(schedulerrun.FieldStartedAt, schedulerrun.FieldSchedulerID, schedulerrun.FieldID)).
		Limit(filter.Limit).
		Offset(max(filter.Offset, 0)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query scheduler run page: %w", err)
	}

	items := make([]domain.SchedulerRunSummary, 0, len(entities))
	for _, entity := range entities {
		items = append(items, schedulerRunFromEntity(entity))
	}
	return items, nil
}

func (s *schedulerStore) CountSchedulerRunsPage(ctx context.Context, filter schedulers.SchedulerRunPageFilter) (int, error) {
	schedulerIDs := idset.Normalize(filter.SchedulerIDs)
	if len(schedulerIDs) == 0 {
		return 0, nil
	}
	query := s.client.SchedulerRun.Query().
		Where(schedulerrun.SchedulerIDIn(schedulerIDs...))
	if filter.RequireTrigger {
		query = query.Where(schedulerrun.TriggerIDNEQ(""))
	}
	if triggerID := strings.TrimSpace(filter.TriggerID); triggerID != "" {
		query = query.Where(schedulerrun.TriggerIDEQ(triggerID))
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where(schedulerrun.StatusEQ(status))
	}
	total, err := query.Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count scheduler runs: %w", err)
	}
	return total, nil
}

func (s *schedulerStore) ListSchedulerRunSandboxIDs(ctx context.Context, keys []schedulers.SchedulerRunKey) (map[schedulers.SchedulerRunKey][]string, error) {
	result := make(map[schedulers.SchedulerRunKey][]string)
	predicates := make([]predicate.SchedulerEvent, 0, len(keys))
	seenKeys := make(map[schedulers.SchedulerRunKey]struct{}, len(keys))
	for _, key := range keys {
		key.SchedulerID = strings.TrimSpace(key.SchedulerID)
		key.RunID = strings.TrimSpace(key.RunID)
		if key.SchedulerID == "" || key.RunID == "" {
			continue
		}
		if _, ok := seenKeys[key]; ok {
			continue
		}
		seenKeys[key] = struct{}{}
		predicates = append(predicates, schedulerevent.And(
			schedulerevent.IDEQ(key.SchedulerID),
			schedulerevent.SchedulerRunIDEQ(key.RunID),
		))
	}
	if len(predicates) == 0 {
		return result, nil
	}
	entities, err := s.client.SchedulerEvent.Query().
		Where(
			schedulerevent.LinkedSandboxIDNEQ(""),
			schedulerevent.Or(predicates...),
		).
		Select(
			schedulerevent.FieldID,
			schedulerevent.FieldSchedulerRunID,
			schedulerevent.FieldLinkedSandboxID,
		).
		Order(
			schedulerevent.ByID(),
			schedulerevent.BySchedulerRunID(),
			schedulerevent.ByLinkedSandboxID(),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query scheduler run sandbox ids: %w", err)
	}
	seen := make(map[schedulers.SchedulerRunKey]map[string]struct{})
	for _, entity := range entities {
		key := schedulers.SchedulerRunKey{SchedulerID: entity.ID, RunID: entity.SchedulerRunID}
		sandboxID := entity.LinkedSandboxID
		if seen[key] == nil {
			seen[key] = make(map[string]struct{})
		}
		if _, ok := seen[key][sandboxID]; ok {
			continue
		}
		seen[key][sandboxID] = struct{}{}
		result[key] = append(result[key], sandboxID)
	}
	return result, nil
}

// Kept as SQL: CTE with a ROW_NUMBER() window function ranks scheduler runs per
// sandbox; ent cannot express window functions without changing the read.
func (s *schedulerStore) BatchGetLatestSchedulerRunsBySandboxIDs(ctx context.Context, schedulerIDs, sandboxIDs []string) (map[string]domain.SchedulerRunSummary, error) {
	schedulerIDs = idset.Normalize(schedulerIDs)
	sandboxIDs = idset.Normalize(sandboxIDs)
	result := make(map[string]domain.SchedulerRunSummary)
	if len(schedulerIDs) == 0 || len(sandboxIDs) == 0 {
		return result, nil
	}

	args := make([]any, 0, len(schedulerIDs)+len(sandboxIDs))
	for _, sandboxID := range sandboxIDs {
		args = append(args, sandboxID)
	}
	for _, schedulerID := range schedulerIDs {
		args = append(args, schedulerID)
	}
	query := `WITH associations AS (
		SELECT DISTINCT linked_sandbox_id AS sandbox_id, scheduler_id, scheduler_run_id AS run_id
		FROM scheduler_event
		WHERE linked_sandbox_id <> ''
			AND linked_sandbox_id IN (` + placeholders(len(sandboxIDs)) + `)
			AND scheduler_id IN (` + placeholders(len(schedulerIDs)) + `)
	), ranked AS (
		SELECT a.sandbox_id,
			r.scheduler_id, r.run_id, r.trigger_id, r.trigger_kind, r.trigger_source, r.status,
			r.started_at, r.completed_at, r.duration_ms, r.error, r.result_json, r.payload_json,
			r.source_script_sha256, r.artifacts_dir,
			ROW_NUMBER() OVER (
				PARTITION BY a.sandbox_id
				ORDER BY CASE WHEN r.completed_at > 0 THEN r.completed_at ELSE r.started_at END DESC,
					r.started_at DESC, r.scheduler_id DESC, r.run_id DESC
			) AS association_rank
		FROM associations a
		JOIN scheduler_run r ON r.scheduler_id = a.scheduler_id AND r.run_id = a.run_id
		WHERE r.trigger_id <> ''
	)
	SELECT sandbox_id, scheduler_id, run_id, trigger_id, trigger_kind, trigger_source, status,
		started_at, completed_at, duration_ms, error, result_json, payload_json,
		source_script_sha256, artifacts_dir
	FROM ranked
	WHERE association_rank = 1
	ORDER BY sandbox_id ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query latest scheduler runs by sandbox ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sandboxID string
		run, scanErr := schedulers.ScanSchedulerRun(func(dest ...any) error {
			return rows.Scan(append([]any{&sandboxID}, dest...)...)
		})
		if scanErr != nil {
			return nil, scanErr
		}
		result[sandboxID] = run
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate latest scheduler runs by sandbox ids: %w", err)
	}
	return result, nil
}
