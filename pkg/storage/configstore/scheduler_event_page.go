package configstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/idset"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

func (s *schedulerStore) ListSchedulerEventsPage(ctx context.Context, filter schedulers.SchedulerEventPageFilter) ([]domain.SchedulerEvent, error) {
	schedulerIDs := idset.Normalize(filter.SchedulerIDs)
	if len(schedulerIDs) == 0 {
		return []domain.SchedulerEvent{}, nil
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	placeholders := make([]string, len(schedulerIDs))
	args := make([]any, 0, len(schedulerIDs)+10)
	for index, schedulerID := range schedulerIDs {
		placeholders[index] = "?"
		args = append(args, schedulerID)
	}
	// Kept as SQL: joins scheduler_event with scheduler_run as one consistent
	// read; ent cannot express this join without splitting the read.
	query := `SELECT e.scheduler_id, e.event_id, e.scheduler_run_id, r.trigger_id, e.type, e.level, e.message, e.payload_json, e.linked_sandbox_id, e.linked_cell_id, e.linked_agent_thread_id, e.created_at
		FROM scheduler_event e JOIN scheduler_run r ON r.scheduler_id = e.scheduler_id AND r.run_id = e.scheduler_run_id
		WHERE e.scheduler_id IN (` + strings.Join(placeholders, ",") + `)`
	if filter.RequireTrigger {
		query += ` AND r.trigger_id <> ''`
	}
	if triggerID := strings.TrimSpace(filter.TriggerID); triggerID != "" {
		query += ` AND r.trigger_id = ?`
		args = append(args, triggerID)
	}
	if runID := strings.TrimSpace(filter.RunID); runID != "" {
		query += ` AND r.run_id = ?`
		args = append(args, runID)
	}
	if sandboxID := strings.TrimSpace(filter.LinkedSandboxID); sandboxID != "" {
		query += ` AND e.linked_sandbox_id = ?`
		args = append(args, sandboxID)
	}
	if !filter.BeforeCreatedAt.IsZero() {
		query += ` AND (e.created_at < ? OR (e.created_at = ? AND (e.scheduler_id < ? OR (e.scheduler_id = ? AND e.event_id < ?))))`
		beforeMillis := filter.BeforeCreatedAt.UTC().UnixMilli()
		args = append(args, beforeMillis, beforeMillis, strings.TrimSpace(filter.BeforeSchedulerID), strings.TrimSpace(filter.BeforeSchedulerID), strings.TrimSpace(filter.BeforeEventID))
	}
	if !filter.AfterCreatedAt.IsZero() {
		query += ` AND (e.created_at > ? OR (e.created_at = ? AND (e.scheduler_id > ? OR (e.scheduler_id = ? AND e.event_id > ?))))`
		afterMillis := filter.AfterCreatedAt.UTC().UnixMilli()
		args = append(args, afterMillis, afterMillis, strings.TrimSpace(filter.AfterSchedulerID), strings.TrimSpace(filter.AfterSchedulerID), strings.TrimSpace(filter.AfterEventID))
	}
	if !filter.FromCreatedAt.IsZero() {
		query += ` AND (e.created_at > ? OR (e.created_at = ? AND (e.scheduler_id > ? OR (e.scheduler_id = ? AND e.event_id >= ?))))`
		fromMillis := filter.FromCreatedAt.UTC().UnixMilli()
		args = append(args, fromMillis, fromMillis, strings.TrimSpace(filter.FromSchedulerID), strings.TrimSpace(filter.FromSchedulerID), strings.TrimSpace(filter.FromEventID))
	}
	if !filter.ThroughCreatedAt.IsZero() {
		query += ` AND (e.created_at < ? OR (e.created_at = ? AND (e.scheduler_id < ? OR (e.scheduler_id = ? AND e.event_id <= ?))))`
		throughMillis := filter.ThroughCreatedAt.UTC().UnixMilli()
		args = append(args, throughMillis, throughMillis, strings.TrimSpace(filter.ThroughSchedulerID), strings.TrimSpace(filter.ThroughSchedulerID), strings.TrimSpace(filter.ThroughEventID))
	}
	if filter.Ascending {
		query += ` ORDER BY e.created_at ASC, e.scheduler_id ASC, e.event_id ASC`
	} else {
		query += ` ORDER BY e.created_at DESC, e.scheduler_id DESC, e.event_id DESC`
	}
	query += ` LIMIT ?`
	args = append(args, filter.Limit)
	if filter.Offset > 0 {
		query += ` OFFSET ?`
		args = append(args, filter.Offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query scheduler event page: %w", err)
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
		return nil, fmt.Errorf("iterate scheduler event page: %w", err)
	}
	return items, nil
}

func (s *schedulerStore) CountSchedulerEventsPage(ctx context.Context, filter schedulers.SchedulerEventPageFilter) (int, error) {
	schedulerIDs := idset.Normalize(filter.SchedulerIDs)
	if len(schedulerIDs) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(schedulerIDs)+2)
	for _, schedulerID := range schedulerIDs {
		args = append(args, schedulerID)
	}
	// Kept as SQL: COUNT over a join between scheduler_event and scheduler_run;
	// ent cannot express this join without splitting the read.
	query := `SELECT COUNT(*) FROM scheduler_event e JOIN scheduler_run r ON r.scheduler_id = e.scheduler_id AND r.run_id = e.scheduler_run_id WHERE e.scheduler_id IN (` + placeholders(len(schedulerIDs)) + `)`
	if filter.RequireTrigger {
		query += ` AND r.trigger_id <> ''`
	}
	if triggerID := strings.TrimSpace(filter.TriggerID); triggerID != "" {
		query += ` AND r.trigger_id = ?`
		args = append(args, triggerID)
	}
	if runID := strings.TrimSpace(filter.RunID); runID != "" {
		query += ` AND r.run_id = ?`
		args = append(args, runID)
	}
	if sandboxID := strings.TrimSpace(filter.LinkedSandboxID); sandboxID != "" {
		query += ` AND e.linked_sandbox_id = ?`
		args = append(args, sandboxID)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count scheduler events: %w", err)
	}
	return total, nil
}

// maxSandboxSchedulerSteps bounds how many step events one sandbox's history
// reads. A sandbox a sticky scheduler keeps can host a step per trigger run.
const maxSandboxSchedulerSteps = 10000

// ListSandboxSchedulerSteps returns the start and end events of the shell/exec
// steps scheduler runs ran in a sandbox, oldest first. They locate each step's
// log, which is the step's only history.
func (s *schedulerStore) ListSandboxSchedulerSteps(ctx context.Context, sandboxID string) ([]domain.SchedulerEvent, error) {
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return []domain.SchedulerEvent{}, nil
	}
	// Kept as SQL: the trigger comes from scheduler_run, as on every other
	// scheduler event read. idx_scheduler_event_sandbox_run serves the filter.
	rows, err := s.db.QueryContext(ctx, `SELECT e.scheduler_id, e.event_id, e.scheduler_run_id, COALESCE(r.trigger_id, ''), e.type, e.level, e.message, e.payload_json, e.linked_sandbox_id, e.linked_cell_id, e.linked_agent_thread_id, e.created_at
		FROM scheduler_event e LEFT JOIN scheduler_run r ON r.scheduler_id = e.scheduler_id AND r.run_id = e.scheduler_run_id
		WHERE e.linked_sandbox_id = ? AND e.linked_cell_id <> '' AND e.scheduler_run_id <> ''
			AND e.type IN ('scheduler.command.started', 'scheduler.command.completed', 'scheduler.command.failed')
		ORDER BY e.created_at ASC, e.scheduler_id ASC, e.event_id ASC
		LIMIT ?`, sandboxID, maxSandboxSchedulerSteps)
	if err != nil {
		return nil, fmt.Errorf("query sandbox scheduler steps: %w", err)
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
		return nil, fmt.Errorf("iterate sandbox scheduler steps: %w", err)
	}
	return items, nil
}
