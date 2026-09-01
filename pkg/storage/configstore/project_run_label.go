package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const (
	maxProjectRunLabelKeyLen   = 128
	maxProjectRunLabelValueLen = 256
)

// rowQueryer is satisfied by both *sql.DB and *sql.Tx, letting label reads run
// either standalone or inside an already-open transaction.
type rowQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// loadProjectRunLabels aggregates the full label set for one run from
// project_run_label. There is no redundant column on project_run, so this
// query is the only way to reconstruct a run's labels for detail-view reads
// (GetProjectRun and the transactional helpers that back RunAgent/GetRun).
// ListProjectRunsByOptions never calls this: labels live on RunDetail, not
// RunSummary, so the high-frequency list/stream path stays free of it.
func loadProjectRunLabels(ctx context.Context, q rowQueryer, runID string) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, value FROM project_run_label WHERE run_id = ?`, runID)
	if err != nil {
		return nil, fmt.Errorf("load project run labels %s: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()
	var labels map[string]string
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan project run label %s: %w", runID, err)
		}
		if labels == nil {
			labels = make(map[string]string, 4)
		}
		labels[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project run labels %s: %w", runID, err)
	}
	return labels, nil
}

// insertProjectRunLabelsTx validates and persists labels into
// project_run_label inside an already-open transaction. This is the single
// write path for run labels: every caller, whether it reached here through
// the CLI's --label flag or a direct RunAgent API call, is validated here and
// only here.
func insertProjectRunLabelsTx(ctx context.Context, tx *sql.Tx, runID string, labels map[string]string) error {
	for key, value := range labels {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("run label key must not be empty")
		}
		if len(key) > maxProjectRunLabelKeyLen {
			return fmt.Errorf("run label key %q exceeds %d characters", key, maxProjectRunLabelKeyLen)
		}
		if len(value) > maxProjectRunLabelValueLen {
			return fmt.Errorf("run label value for key %q exceeds %d characters", key, maxProjectRunLabelValueLen)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_run_label(run_id, key, value) VALUES(?, ?, ?)
			ON CONFLICT(run_id, key) DO UPDATE SET value = excluded.value`, runID, key, value); err != nil {
			return fmt.Errorf("insert project run label %q for run %s: %w", key, runID, err)
		}
	}
	return nil
}
