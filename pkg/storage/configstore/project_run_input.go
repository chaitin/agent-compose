package configstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

type runInputQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadRunInput(ctx context.Context, query runInputQuery, runID string) (*domain.RunInputPreparation, error) {
	var encoded string
	err := query.QueryRowContext(ctx, `SELECT preparation_json FROM project_run_input WHERE run_id = ?`, runID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load run input: %w", err)
	}
	var input domain.RunInputPreparation
	if err := json.Unmarshal([]byte(encoded), &input); err != nil {
		return nil, fmt.Errorf("decode run input: %w", err)
	}
	return &input, nil
}

// SaveRunInput updates evidence with optimistic concurrency. Ordinary Run updates
// never overwrite this record. SQLite SQL follows the existing configstore boundary.
func (s *projectStore) SaveRunInput(ctx context.Context, previous, next domain.RunInputPreparation) error {
	oldJSON, err := json.Marshal(previous)
	if err != nil {
		return err
	}
	nextJSON, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if previous.RunID != next.RunID || !bytes.Equal(previous.ManifestSHA256, next.ManifestSHA256) {
		return fmt.Errorf("%w: input identity cannot change", domain.ErrInvalidArgument)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE project_run_input SET preparation_json = ? WHERE run_id = ? AND preparation_json = ?`, string(nextJSON), next.RunID, string(oldJSON))
	if err != nil {
		return fmt.Errorf("save run input: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("%w: input evidence changed concurrently", domain.ErrConflict)
	}
	return nil
}

func insertRunInput(ctx context.Context, tx *sql.Tx, input *domain.RunInputPreparation) error {
	if input == nil {
		return nil
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO project_run_input(run_id, preparation_json) VALUES (?, ?)`, input.RunID, string(encoded))
	return err
}

func validateRunInputReplay(existing, incoming ProjectRunRecord) error {
	a, b := existing.InputPreparation, incoming.InputPreparation
	if a == nil && b == nil {
		return nil
	}
	if a == nil || b == nil || existing.Prompt != incoming.Prompt || !bytes.Equal(a.ManifestSHA256, b.ManifestSHA256) {
		return fmt.Errorf("%w: run request identity has different text or file manifest", domain.ErrConflict)
	}
	return nil
}
