package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin run event transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	results, created, err := appendProjectRunEventsTx(ctx, tx, events)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit run event: %w", err)
	}
	return results, created, nil
}

func appendProjectRunEventsTx(ctx context.Context, tx *sql.Tx, events []domain.ProjectRunEventRecord) ([]domain.ProjectRunEventRecord, []bool, error) {
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
		err := tx.QueryRowContext(ctx, `INSERT INTO project_run_event (id, run_id, seq, kind, text, agent, name, payload_json, success, exit_code, stop_reason, created_at, log_seq)
			SELECT ?, ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ? FROM project_run_event WHERE run_id = ?
			RETURNING seq`, event.ID, event.RunID, string(event.Kind), event.Text, event.Agent, event.Name, event.PayloadJSON, BoolToInt(event.Success), event.ExitCode, event.StopReason, event.CreatedAt.UTC().UnixMilli(), event.LogSeq, event.RunID).Scan(&event.Sequence)
		if err != nil {
			existing, found, findErr := getRunEventByID(ctx, tx, event.ID)
			if findErr == nil && found && existing.RunID == event.RunID {
				results = append(results, existing)
				created = append(created, false)
				continue
			}
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, seq, kind, text, agent, name, payload_json, success, exit_code, stop_reason, created_at, log_seq FROM project_run_event WHERE run_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`, strings.TrimSpace(runID), afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("list run events: %w", err)
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
		return nil, fmt.Errorf("iterate run events: %w", err)
	}
	return events, nil
}

func (s *projectStore) ListProjectRunEventsPage(ctx context.Context, runID string, offset, limit int) ([]domain.ProjectRunEventRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, seq, kind, text, agent, name, payload_json, success, exit_code, stop_reason, created_at, log_seq FROM project_run_event WHERE run_id = ? ORDER BY seq ASC LIMIT ? OFFSET ?`, strings.TrimSpace(runID), limit, max(offset, 0))
	if err != nil {
		return nil, fmt.Errorf("list run event page: %w", err)
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
		return nil, fmt.Errorf("iterate run event page: %w", err)
	}
	return events, nil
}

func (s *projectStore) HasProjectRunEvents(ctx context.Context, runID string) (bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_run_event WHERE run_id = ?)`, strings.TrimSpace(runID)).Scan(&exists); err != nil {
		return false, fmt.Errorf("check run events: %w", err)
	}
	return exists, nil
}

func (s *projectStore) CountProjectRunEvents(ctx context.Context, runID string) (int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_run_event WHERE run_id = ?`, strings.TrimSpace(runID)).Scan(&total); err != nil {
		return 0, fmt.Errorf("count run events: %w", err)
	}
	return total, nil
}

func (s *projectStore) CountProjectRunEventsForSandbox(ctx context.Context, sandboxID string) (int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_run_event e JOIN project_run r ON r.run_id = e.run_id WHERE r.sandbox_id = ?`, strings.TrimSpace(sandboxID)).Scan(&total); err != nil {
		return 0, fmt.Errorf("count sandbox run events: %w", err)
	}
	return total, nil
}

func (s *projectStore) ListProjectRunEventRunIDsForSandbox(ctx context.Context, sandboxID string) ([]string, error) {
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
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.run_id, e.seq, e.kind, e.text, e.agent, e.name, e.payload_json, e.success, e.exit_code, e.stop_reason, e.created_at, e.log_seq
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
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.run_id, e.seq, e.kind, e.text, e.agent, e.name, e.payload_json, e.success, e.exit_code, e.stop_reason, e.created_at, e.log_seq
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
	if err := scan(&event.ID, &event.RunID, &event.Sequence, &event.Kind, &event.Text, &event.Agent, &event.Name, &event.PayloadJSON, &success, &event.ExitCode, &event.StopReason, &createdAt, &event.LogSeq); err != nil {
		return event, fmt.Errorf("scan run event: %w", err)
	}
	event.Success = success != 0
	event.CreatedAt = time.UnixMilli(createdAt).UTC()
	return event, nil
}

func getRunEventByID(ctx context.Context, tx *sql.Tx, eventID string) (domain.ProjectRunEventRecord, bool, error) {
	event, err := scanRunEvent(tx.QueryRowContext(ctx, `SELECT id, run_id, seq, kind, text, agent, name, payload_json, success, exit_code, stop_reason, created_at, log_seq FROM project_run_event WHERE id = ?`, eventID).Scan)
	if err == nil {
		return event, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectRunEventRecord{}, false, nil
	}
	return domain.ProjectRunEventRecord{}, false, err
}

// runMessageKinds are the run events that make up a run's conversation.
const runMessageKinds = `'user_message', 'agent_message'`

// ListProjectRunMessagesPage pages the conversation of one run (runID) or of
// every run a sandbox hosted (sandboxID); exactly one is set.
func (s *projectStore) ListProjectRunMessagesPage(ctx context.Context, runID, sandboxID string, offset, limit int) ([]domain.ProjectRunEventRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	where, arg := runMessageScope(runID, sandboxID)
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.run_id, e.seq, e.kind, e.text, e.agent, e.name, e.payload_json, e.success, e.exit_code, e.stop_reason, e.created_at, e.log_seq
		FROM project_run_event e JOIN project_run r ON r.run_id = e.run_id
		WHERE `+where+` AND e.kind IN (`+runMessageKinds+`)
		ORDER BY e.created_at ASC, e.run_id ASC, e.seq ASC LIMIT ? OFFSET ?`, arg, limit, max(offset, 0))
	if err != nil {
		return nil, fmt.Errorf("list run messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var messages []domain.ProjectRunEventRecord
	for rows.Next() {
		message, scanErr := scanRunEvent(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list run messages: %w", err)
	}
	return messages, nil
}

// CountProjectRunMessages counts what ListProjectRunMessagesPage pages.
func (s *projectStore) CountProjectRunMessages(ctx context.Context, runID, sandboxID string) (int, error) {
	where, arg := runMessageScope(runID, sandboxID)
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_run_event e JOIN project_run r ON r.run_id = e.run_id
		WHERE `+where+` AND e.kind IN (`+runMessageKinds+`)`, arg).Scan(&total); err != nil {
		return 0, fmt.Errorf("count run messages: %w", err)
	}
	return total, nil
}

func runMessageScope(runID, sandboxID string) (string, string) {
	if runID = strings.TrimSpace(runID); runID != "" {
		return `e.run_id = ?`, runID
	}
	return `r.sandbox_id = ?`, strings.TrimSpace(sandboxID)
}
