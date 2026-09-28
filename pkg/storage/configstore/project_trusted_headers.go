package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

type storedTrustedHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SetProjectApplyTrustedHeaders records the trusted headers of the request that
// last applied the Project, replacing any earlier ones. An empty list clears
// them.
func (s *projectStore) SetProjectApplyTrustedHeaders(ctx context.Context, projectID string, headers []domain.TrustedHeader) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Errorf("project id is required")
	}
	stored := make([]storedTrustedHeader, 0, len(headers))
	for _, header := range headers {
		stored = append(stored, storedTrustedHeader(header))
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode project %s apply trusted headers: %w", projectID, err)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE project SET apply_trusted_headers_json = ? WHERE id = ?`, string(encoded), projectID)
	if err != nil {
		return fmt.Errorf("update project %s apply trusted headers: %w", projectID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.ResourceError(domain.ErrNotFound, "project", projectID, fmt.Sprintf("project %s not found", projectID), nil)
	}
	return nil
}

// ProjectApplyTrustedHeaders returns the trusted headers recorded by the last
// apply of the Project, or nil when it carried none.
func (s *projectStore) ProjectApplyTrustedHeaders(ctx context.Context, projectID string) ([]domain.TrustedHeader, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT apply_trusted_headers_json FROM project WHERE id = ?`, projectID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ResourceError(domain.ErrNotFound, "project", projectID, fmt.Sprintf("project %s not found", projectID), err)
	}
	if err != nil {
		return nil, fmt.Errorf("load project %s apply trusted headers: %w", projectID, err)
	}
	var stored []storedTrustedHeader
	if err := json.Unmarshal([]byte(encoded), &stored); err != nil {
		return nil, fmt.Errorf("decode project %s apply trusted headers: %w", projectID, err)
	}
	if len(stored) == 0 {
		return nil, nil
	}
	headers := make([]domain.TrustedHeader, 0, len(stored))
	for _, header := range stored {
		headers = append(headers, domain.TrustedHeader(header))
	}
	return headers, nil
}
