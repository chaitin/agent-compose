package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// projectOctoBusTokenStore owns the per-project OctoBus server bearer tokens.
// These tokens are deliberately kept out of the versioned project spec so that
// rotating one does not create a new project revision.
type projectOctoBusTokenStore struct {
	db *sql.DB
}

// GetProjectOctoBusServerToken returns the stored bearer token for a project
// server. An empty token means no Authorization header is sent to that server.
func (s *projectOctoBusTokenStore) GetProjectOctoBusServerToken(ctx context.Context, projectID, serverName string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	serverName = strings.TrimSpace(serverName)
	if projectID == "" || serverName == "" {
		return "", fmt.Errorf("project octobus server token: project id and server name are required")
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT token FROM project_octobus_server_token WHERE project_id = ? AND server_name = ?`,
		projectID, serverName)
	var token string
	switch err := row.Scan(&token); {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("query project octobus server token: %w", err)
	}
	return token, nil
}

// SaveProjectOctoBusServerToken upserts the bearer token for a project server.
// An empty token clears it.
func (s *projectOctoBusTokenStore) SaveProjectOctoBusServerToken(ctx context.Context, projectID, serverName, token string) error {
	projectID = strings.TrimSpace(projectID)
	serverName = strings.TrimSpace(serverName)
	if projectID == "" || serverName == "" {
		return fmt.Errorf("project octobus server token: project id and server name are required")
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO project_octobus_server_token(project_id, server_name, token, updated_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(project_id, server_name) DO UPDATE SET token = excluded.token, updated_at = excluded.updated_at`,
		projectID, serverName, strings.TrimSpace(token), time.Now().UTC().Unix()); err != nil {
		return fmt.Errorf("save project octobus server token: %w", err)
	}
	return nil
}
