package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/projectrunevent"
	domain "github.com/chaitin/agent-compose/pkg/model"

	"github.com/chaitin/agent-compose/pkg/storage/dialect"
)

func (s *projectStore) AppendProjectRunEvent(ctx context.Context, event domain.ProjectRunEventRecord) (domain.ProjectRunEventRecord, bool, error) {
	items, created, err := s.AppendProjectRunEvents(ctx, []domain.ProjectRunEventRecord{event})
	if err != nil {
		return domain.ProjectRunEventRecord{}, false, err
	}
	return items[0], created[0], nil
}

func (s *projectStore) AppendProjectRunEvents(ctx context.Context, events []domain.ProjectRunEventRecord) ([]domain.ProjectRunEventRecord, []bool, error) {
	if len(events) == 0 {
		return nil, nil, nil
	}
	// Kept as SQL: the append allocates per-run sequence numbers with a
	// locking read and INSERT ... SELECT, so it stays on one raw transaction.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin run event transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	results, created, err := appendProjectRunEventsTx(ctx, tx, s.dialect, events)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit run event: %w", err)
	}
	s.notifyRunEventsByRunID(ctx, results, created)
	return results, created, nil
}

// lockProjectRunRows serializes sequence allocation for one run. SQLite's
// write transaction already excludes other writers; PostgreSQL needs the
// parent row locked because READ COMMITTED gives concurrent appends the same
// MAX(seq) value.
func lockProjectRunRows(ctx context.Context, tx *sql.Tx, d dialect.Dialect, events []domain.ProjectRunEventRecord) error {
	if d == nil || d.Name() != dialect.NamePostgres {
		return nil
	}
	runIDs := make([]string, 0, len(events))
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		runID := strings.TrimSpace(event.RunID)
		if runID == "" {
			continue
		}
		if _, ok := seen[runID]; ok {
			continue
		}
		seen[runID] = struct{}{}
		runIDs = append(runIDs, runID)
	}
	sort.Strings(runIDs)
	for _, runID := range runIDs {
		var existing string
		// Kept as SQL: PostgreSQL needs SELECT ... FOR UPDATE on the parent run
		// row; ent cannot express a locking read.
		err := tx.QueryRowContext(ctx, `SELECT run_id FROM project_run WHERE run_id = ?`+d.ForUpdateClause(), runID).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("lock project run %s for event append: %w", runID, err)
		}
	}
	return nil
}

func appendProjectRunEventsTx(ctx context.Context, tx *sql.Tx, d dialect.Dialect, events []domain.ProjectRunEventRecord) ([]domain.ProjectRunEventRecord, []bool, error) {
	if err := lockProjectRunRows(ctx, tx, d, events); err != nil {
		return nil, nil, err
	}
	results := make([]domain.ProjectRunEventRecord, 0, len(events))
	created := make([]bool, 0, len(events))
	for _, event := range events {
		event.ID = strings.TrimSpace(event.ID)
		event.RunID = strings.TrimSpace(event.RunID)
		event.Kind = domain.ProjectRunEventKind(strings.TrimSpace(string(event.Kind)))
		if event.ID == "" || event.RunID == "" || event.Kind == "" {
			return nil, nil, fmt.Errorf("event id, run id, and kind are required")
		}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = time.Now().UTC()
		}
		// ON CONFLICT keeps the transaction usable on PostgreSQL: a duplicate
		// primary key aborts the whole transaction, so a follow-up SELECT in
		// the old error path could never observe the existing row.
		// Kept as SQL: seq is allocated with COALESCE(MAX(seq), 0) + 1 in an
		// INSERT ... SELECT and returned by RETURNING; ent cannot express that
		// aggregate-derived upsert.
		err := tx.QueryRowContext(ctx, `INSERT INTO project_run_event (id, run_id, seq, kind, text, agent, name, payload_json, success, exit_code, stop_reason, created_at)
			SELECT ?, ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?, ?, ?, ?, ?, ?, ? FROM project_run_event WHERE run_id = ?
			ON CONFLICT (id) DO NOTHING
			RETURNING seq`, event.ID, event.RunID, string(event.Kind), event.Text, event.Agent, event.Name, event.PayloadJSON, BoolToInt(event.Success), event.ExitCode, event.StopReason, event.CreatedAt.UTC().UnixMilli(), event.RunID).Scan(&event.Sequence)
		if errors.Is(err, sql.ErrNoRows) {
			existing, found, findErr := getRunEventByID(ctx, tx, event.ID)
			if findErr != nil {
				return nil, nil, fmt.Errorf("read conflicting run event %s: %w", event.ID, findErr)
			}
			if !found {
				return nil, nil, fmt.Errorf("insert run event %s: conflict without an existing row", event.ID)
			}
			if existing.RunID != event.RunID {
				return nil, nil, fmt.Errorf("insert run event: event %s already belongs to run %s", event.ID, existing.RunID)
			}
			results = append(results, existing)
			created = append(created, false)
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("insert run event: %w", err)
		}
		results = append(results, event)
		created = append(created, true)
	}
	return results, created, nil
}

func (s *projectStore) ListProjectRunEvents(ctx context.Context, runID string, afterSequence uint64, limit int) ([]domain.ProjectRunEventRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	// A sequence cursor above int64 cannot be compared against the stored
	// column; reject it instead of wrapping the conversion to a negative value.
	if afterSequence > math.MaxInt64 {
		return nil, fmt.Errorf("list run events: sequence cursor %d is out of range", afterSequence)
	}
	entities, err := s.client.ProjectRunEvent.Query().
		Where(
			projectrunevent.RunIDEQ(strings.TrimSpace(runID)),
			projectrunevent.SequenceGT(int64(afterSequence)),
		).
		Order(projectrunevent.BySequence()).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list run events: %w", err)
	}
	var events []domain.ProjectRunEventRecord
	for _, entity := range entities {
		events = append(events, projectRunEventFromEntity(entity))
	}
	return events, nil
}

func (s *projectStore) ListProjectRunEventsPage(ctx context.Context, runID string, offset, limit int) ([]domain.ProjectRunEventRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	entities, err := s.client.ProjectRunEvent.Query().
		Where(projectrunevent.RunIDEQ(strings.TrimSpace(runID))).
		Order(projectrunevent.BySequence()).
		Limit(limit).
		Offset(max(offset, 0)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list run event page: %w", err)
	}
	var events []domain.ProjectRunEventRecord
	for _, entity := range entities {
		events = append(events, projectRunEventFromEntity(entity))
	}
	return events, nil
}

func (s *projectStore) HasProjectRunEvents(ctx context.Context, runID string) (bool, error) {
	exists, err := s.client.ProjectRunEvent.Query().
		Where(projectrunevent.RunIDEQ(strings.TrimSpace(runID))).
		Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("check run events: %w", err)
	}
	return exists, nil
}

func (s *projectStore) CountProjectRunEvents(ctx context.Context, runID string) (int, error) {
	total, err := s.client.ProjectRunEvent.Query().
		Where(projectrunevent.RunIDEQ(strings.TrimSpace(runID))).
		Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count run events: %w", err)
	}
	return total, nil
}

func (s *projectStore) CountProjectRunEventsForSandbox(ctx context.Context, sandboxID string) (int, error) {
	var total int
	// Kept as SQL: COUNT join between project_run_event and project_run.
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_run_event e JOIN project_run r ON r.run_id = e.run_id WHERE r.sandbox_id = ?`, strings.TrimSpace(sandboxID)).Scan(&total); err != nil {
		return 0, fmt.Errorf("count sandbox run events: %w", err)
	}
	return total, nil
}

func (s *projectStore) ListProjectRunEventRunIDsForSandbox(ctx context.Context, sandboxID string) ([]string, error) {
	// Kept as SQL: DISTINCT join between project_run_event and project_run.
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT e.run_id FROM project_run_event e JOIN project_run r ON r.run_id = e.run_id WHERE r.sandbox_id = ? ORDER BY e.run_id`, strings.TrimSpace(sandboxID))
	if err != nil {
		return nil, fmt.Errorf("list sandbox run event run ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var runIDs []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return nil, fmt.Errorf("scan sandbox run event run id: %w", err)
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sandbox run event run ids: %w", err)
	}
	return runIDs, nil
}

func (s *projectStore) ListProjectRunEventsForSandbox(ctx context.Context, sandboxID string, cursor domain.ProjectRunEventSandboxCursor) ([]domain.ProjectRunEventRecord, error) {
	afterCreatedAt, afterRunID, afterSequence, limit := cursor.AfterCreatedAt, cursor.AfterRunID, cursor.AfterSequence, cursor.Limit
	if limit <= 0 {
		limit = 100
	}
	// Kept as SQL: join to project_run with a compound keyset predicate.
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.run_id, e.seq, e.kind, e.text, e.agent, e.name, e.payload_json, e.success, e.exit_code, e.stop_reason, e.created_at
		FROM project_run_event e JOIN project_run r ON r.run_id = e.run_id
		WHERE r.sandbox_id = ? AND (e.created_at > ? OR (e.created_at = ? AND (e.run_id > ? OR (e.run_id = ? AND e.seq > ?))))
		ORDER BY e.created_at ASC, e.run_id ASC, e.seq ASC LIMIT ?`, strings.TrimSpace(sandboxID), afterCreatedAt.UTC().UnixMilli(), afterCreatedAt.UTC().UnixMilli(), afterRunID, afterRunID, afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("list sandbox run events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var events []domain.ProjectRunEventRecord
	for rows.Next() {
		event, scanErr := scanRunEvent(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sandbox run events: %w", err)
	}
	return events, nil
}

func (s *projectStore) ListProjectRunEventsForSandboxPage(ctx context.Context, sandboxID string, offset, limit int) ([]domain.ProjectRunEventRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	// Kept as SQL: join to project_run scoped to one sandbox.
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.run_id, e.seq, e.kind, e.text, e.agent, e.name, e.payload_json, e.success, e.exit_code, e.stop_reason, e.created_at
		FROM project_run_event e JOIN project_run r ON r.run_id = e.run_id
		WHERE r.sandbox_id = ? ORDER BY e.created_at ASC, e.run_id ASC, e.seq ASC LIMIT ? OFFSET ?`, strings.TrimSpace(sandboxID), limit, max(offset, 0))
	if err != nil {
		return nil, fmt.Errorf("list sandbox run event page: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var events []domain.ProjectRunEventRecord
	for rows.Next() {
		event, scanErr := scanRunEvent(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sandbox run event page: %w", err)
	}
	return events, nil
}

type runEventScanner func(...any) error

func scanRunEvent(scan runEventScanner) (domain.ProjectRunEventRecord, error) {
	var event domain.ProjectRunEventRecord
	var success int
	var createdAt int64
	if err := scan(&event.ID, &event.RunID, &event.Sequence, &event.Kind, &event.Text, &event.Agent, &event.Name, &event.PayloadJSON, &success, &event.ExitCode, &event.StopReason, &createdAt); err != nil {
		return event, fmt.Errorf("scan run event: %w", err)
	}
	event.Success = success != 0
	event.CreatedAt = time.UnixMilli(createdAt).UTC()
	return event, nil
}

// projectRunEventFromEntity maps one run-event row exactly like scanRunEvent.
func projectRunEventFromEntity(entity *dao.ProjectRunEvent) domain.ProjectRunEventRecord {
	return domain.ProjectRunEventRecord{
		ID:          entity.ID,
		RunID:       entity.RunID,
		Sequence:    uint64(entity.Sequence),
		Kind:        domain.ProjectRunEventKind(entity.Kind),
		Text:        entity.Text,
		Agent:       entity.Agent,
		Name:        entity.Name,
		PayloadJSON: entity.PayloadJSON,
		Success:     entity.Success != 0,
		ExitCode:    entity.ExitCode,
		StopReason:  entity.StopReason,
		CreatedAt:   time.UnixMilli(entity.CreatedAt).UTC(),
	}
}

func getRunEventByID(ctx context.Context, tx *sql.Tx, eventID string) (domain.ProjectRunEventRecord, bool, error) {
	// Kept as SQL: conflict-resolution read that runs on the raw transaction
	// carrying the INSERT ... SELECT above.
	event, err := scanRunEvent(tx.QueryRowContext(ctx, `SELECT id, run_id, seq, kind, text, agent, name, payload_json, success, exit_code, stop_reason, created_at FROM project_run_event WHERE id = ?`, eventID).Scan)
	if err == nil {
		return event, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectRunEventRecord{}, false, nil
	}
	return domain.ProjectRunEventRecord{}, false, err
}
