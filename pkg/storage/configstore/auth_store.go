package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	controlauth "agent-compose/pkg/auth"
	"agent-compose/pkg/identity"
)

type authStore struct{ db *sql.DB }

func (s *authStore) ensureAuthSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS auth_token (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL COLLATE NOCASE UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			token_fingerprint TEXT NOT NULL,
			origin TEXT NOT NULL DEFAULT 'issued',
			created_by_token_id TEXT NOT NULL DEFAULT '',
			client_request_id TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			revoked_by_token_id TEXT NOT NULL DEFAULT '',
			revoked_at INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE INDEX IF NOT EXISTS idx_auth_token_role_active ON auth_token(role, revoked_at);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_token_create_request
			ON auth_token(created_by_token_id, client_request_id) WHERE client_request_id != '';`,
		`CREATE TABLE IF NOT EXISTS operation_audit (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL UNIQUE,
			request_id TEXT NOT NULL,
			token_id TEXT NOT NULL DEFAULT '',
			token_name TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT '',
			origin TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL,
			resource_type TEXT NOT NULL DEFAULT '',
			resource_id TEXT NOT NULL DEFAULT '',
			project_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			params_json TEXT NOT NULL DEFAULT '{}',
			changes_json TEXT NOT NULL DEFAULT '{}',
			error_code TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			started_at INTEGER NOT NULL,
			completed_at INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE INDEX IF NOT EXISTS idx_operation_audit_token_sequence ON operation_audit(token_id, sequence DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_operation_audit_action_sequence ON operation_audit(action, sequence DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_operation_audit_project_sequence ON operation_audit(project_id, sequence DESC);`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create control-plane auth schema: %w", err)
		}
	}
	return nil
}

func (s *authStore) ReconcileEnvironmentToken(ctx context.Context, plaintext string, now time.Time) (controlauth.Token, bool, error) {
	id := identity.NewID(identity.ResourceAuthToken, controlauth.OriginEnvironment, controlauth.DefaultAdminName)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlauth.Token{}, false, fmt.Errorf("begin environment token reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := findAuthToken(ctx, tx, id)
	if err != nil {
		return controlauth.Token{}, false, err
	}
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		if !found || !existing.Active() {
			return existing, false, tx.Commit()
		}
		existing.RevokedAt = now
		existing.RevokedByTokenID = "system"
		if _, err := tx.ExecContext(ctx, `UPDATE auth_token SET revoked_by_token_id = ?, revoked_at = ? WHERE id = ?`, "system", now.UnixMilli(), id); err != nil {
			return controlauth.Token{}, false, fmt.Errorf("revoke environment token: %w", err)
		}
		if err := insertAuditTx(ctx, tx, controlauth.Audit{
			ID: identity.NewRandomID(identity.ResourceAudit), RequestID: "startup", TokenName: "system",
			Origin: controlauth.OriginSystem, Action: "auth.token.reconcile", Resource: controlauth.Resource{Type: "auth-token", ID: id},
			Status: controlauth.AuditStatusSucceeded, ParamsJSON: `{"source":"environment"}`,
			ChangesJSON: `{"revoked":true}`, StartedAt: now, CompletedAt: now,
		}); err != nil {
			return controlauth.Token{}, false, err
		}
		return existing, true, tx.Commit()
	}

	hash := controlauth.TokenHash(plaintext)
	var conflictingID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM auth_token WHERE token_hash = ? AND id != ?`, hash, id).Scan(&conflictingID); err == nil {
		return controlauth.Token{}, false, fmt.Errorf("%w: environment token matches token %s", controlauth.ErrTokenConflict, conflictingID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return controlauth.Token{}, false, fmt.Errorf("check environment token hash: %w", err)
	}

	fingerprint := controlauth.TokenFingerprint(plaintext)
	changed := !found || existing.Role != controlauth.RoleAdmin || existing.Origin != controlauth.OriginEnvironment || existing.Fingerprint != fingerprint || !existing.Active()
	if !changed {
		return existing, false, tx.Commit()
	}
	if !found {
		existing = controlauth.Token{
			ID: id, Name: controlauth.DefaultAdminName, Description: "Managed by AGENT_COMPOSE_AUTH_TOKEN",
			Role: controlauth.RoleAdmin, Origin: controlauth.OriginEnvironment, Fingerprint: fingerprint,
			CreatedByTokenID: "system", CreatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO auth_token(
			id, name, description, role, token_hash, token_fingerprint, origin, created_by_token_id, client_request_id, created_at, revoked_by_token_id, revoked_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, '', ?, '', 0)`,
			existing.ID, existing.Name, existing.Description, existing.Role, hash, fingerprint, existing.Origin, existing.CreatedByTokenID, now.UnixMilli()); err != nil {
			return controlauth.Token{}, false, fmt.Errorf("insert environment token: %w", err)
		}
	} else {
		existing.Name = controlauth.DefaultAdminName
		existing.Description = "Managed by AGENT_COMPOSE_AUTH_TOKEN"
		existing.Role = controlauth.RoleAdmin
		existing.Origin = controlauth.OriginEnvironment
		existing.Fingerprint = fingerprint
		existing.RevokedAt = time.Time{}
		existing.RevokedByTokenID = ""
		if _, err := tx.ExecContext(ctx, `UPDATE auth_token SET
			name = ?, description = ?, role = ?, token_hash = ?, token_fingerprint = ?, origin = ?, revoked_by_token_id = '', revoked_at = 0
			WHERE id = ?`, existing.Name, existing.Description, existing.Role, hash, fingerprint, existing.Origin, id); err != nil {
			return controlauth.Token{}, false, fmt.Errorf("update environment token: %w", err)
		}
	}
	if err := insertAuditTx(ctx, tx, controlauth.Audit{
		ID: identity.NewRandomID(identity.ResourceAudit), RequestID: "startup", TokenName: "system",
		Origin: controlauth.OriginSystem, Action: "auth.token.reconcile", Resource: controlauth.Resource{Type: "auth-token", ID: id},
		Status: controlauth.AuditStatusSucceeded, ParamsJSON: `{"source":"environment"}`,
		ChangesJSON: `{"active":true,"role":"admin"}`, StartedAt: now, CompletedAt: now,
	}); err != nil {
		return controlauth.Token{}, false, err
	}
	return existing, true, tx.Commit()
}

func (s *authStore) AuthInitialized(ctx context.Context) (bool, error) {
	var initialized bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM auth_token)`).Scan(&initialized); err != nil {
		return false, fmt.Errorf("query auth initialization: %w", err)
	}
	return initialized, nil
}

func (s *authStore) AuthenticateTokenHash(ctx context.Context, hash string) (controlauth.Token, error) {
	row := s.db.QueryRowContext(ctx, authTokenSelect+` WHERE token_hash = ? AND revoked_at = 0`, strings.TrimSpace(hash))
	token, err := scanAuthToken(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return controlauth.Token{}, controlauth.ErrInvalidToken
	}
	if err != nil {
		return controlauth.Token{}, fmt.Errorf("authenticate daemon token: %w", err)
	}
	return token, nil
}

func (s *authStore) ListTokens(ctx context.Context, options controlauth.TokenListOptions) (controlauth.TokenListResult, error) {
	limit := options.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	where := []string{"1 = 1"}
	args := make([]any, 0, 3)
	if !options.IncludeRevoked {
		where = append(where, "revoked_at = 0")
	}
	if options.BeforeID != "" {
		where = append(where, "id < ?")
		args = append(args, options.BeforeID)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, authTokenSelect+` WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return controlauth.TokenListResult{}, fmt.Errorf("list daemon tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]controlauth.Token, 0, limit+1)
	for rows.Next() {
		item, scanErr := scanAuthToken(rows.Scan)
		if scanErr != nil {
			return controlauth.TokenListResult{}, fmt.Errorf("scan daemon token: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return controlauth.TokenListResult{}, fmt.Errorf("iterate daemon tokens: %w", err)
	}
	result := controlauth.TokenListResult{Tokens: items}
	if len(items) > limit {
		result.NextCursor = items[limit-1].ID
		result.Tokens = items[:limit]
	}
	return result, nil
}

func (s *authStore) CreateToken(ctx context.Context, actor controlauth.Identity, input controlauth.CreateTokenInput, requestID, paramsJSON string, now time.Time) (controlauth.Token, bool, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlauth.Token{}, false, false, fmt.Errorf("begin token create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	hash := controlauth.TokenHash(input.Plaintext)
	row := tx.QueryRowContext(ctx, authTokenSelect+` WHERE created_by_token_id = ? AND client_request_id = ?`, actor.TokenID, input.ClientRequestID)
	existing, findErr := scanAuthToken(row.Scan)
	if findErr == nil {
		var existingHash string
		if err := tx.QueryRowContext(ctx, `SELECT token_hash FROM auth_token WHERE id = ?`, existing.ID).Scan(&existingHash); err != nil {
			return controlauth.Token{}, false, false, fmt.Errorf("read idempotent token hash: %w", err)
		}
		if existing.Name != input.Name || existing.Description != input.Description || existing.Role != input.Role || existingHash != hash {
			return controlauth.Token{}, false, false, controlauth.ErrIdempotencyConflict
		}
		return existing, false, true, tx.Commit()
	}
	if !errors.Is(findErr, sql.ErrNoRows) {
		return controlauth.Token{}, false, false, fmt.Errorf("find idempotent token create: %w", findErr)
	}

	item := controlauth.Token{
		ID: identity.NewRandomID(identity.ResourceAuthToken), Name: input.Name, Description: input.Description,
		Role: input.Role, Origin: controlauth.OriginIssued, Fingerprint: controlauth.TokenFingerprint(input.Plaintext),
		CreatedByTokenID: actor.TokenID, ClientRequestID: input.ClientRequestID, CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO auth_token(
		id, name, description, role, token_hash, token_fingerprint, origin, created_by_token_id, client_request_id, created_at, revoked_by_token_id, revoked_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', 0)`, item.ID, item.Name, item.Description, item.Role, hash, item.Fingerprint, item.Origin, item.CreatedByTokenID, item.ClientRequestID, now.UnixMilli()); err != nil {
		return controlauth.Token{}, false, false, fmt.Errorf("%w: insert daemon token: %v", controlauth.ErrTokenConflict, err)
	}
	changes, _ := json.Marshal(map[string]any{"created": true, "token_id": item.ID, "name": item.Name, "role": item.Role})
	if err := insertAuditTx(ctx, tx, controlauth.Audit{
		ID: identity.NewRandomID(identity.ResourceAudit), RequestID: requestID,
		TokenID: actor.TokenID, TokenName: actor.TokenName, Role: actor.Role, Origin: actor.Origin,
		Action: "auth.token.create", Resource: controlauth.Resource{Type: "auth-token", ID: item.ID},
		Status: controlauth.AuditStatusSucceeded, ParamsJSON: paramsJSON, ChangesJSON: string(changes), StartedAt: now, CompletedAt: now,
	}); err != nil {
		return controlauth.Token{}, false, false, err
	}
	if err := tx.Commit(); err != nil {
		return controlauth.Token{}, false, false, fmt.Errorf("commit token create: %w", err)
	}
	return item, true, false, nil
}

func (s *authStore) RevokeToken(ctx context.Context, actor controlauth.Identity, ref, requestID, paramsJSON string, now time.Time) (controlauth.Token, bool, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlauth.Token{}, false, false, fmt.Errorf("begin token revoke: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	item, found, err := findAuthToken(ctx, tx, ref)
	if err != nil {
		return controlauth.Token{}, false, false, err
	}
	if !found {
		return controlauth.Token{}, false, false, controlauth.ErrTokenNotFound
	}
	if item.ID == actor.TokenID {
		return controlauth.Token{}, false, false, controlauth.ErrTokenSelfRevoke
	}
	if item.Origin == controlauth.OriginEnvironment {
		return controlauth.Token{}, false, false, controlauth.ErrEnvironmentToken
	}
	if !item.Active() {
		return item, false, true, tx.Commit()
	}
	if item.Role == controlauth.RoleAdmin && !actor.IsLocal() {
		var activeAdmins int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM auth_token WHERE role = ? AND revoked_at = 0`, controlauth.RoleAdmin).Scan(&activeAdmins); err != nil {
			return controlauth.Token{}, false, false, fmt.Errorf("count active admin tokens: %w", err)
		}
		if activeAdmins <= 1 {
			return controlauth.Token{}, false, false, controlauth.ErrLastAdminToken
		}
	}
	item.RevokedAt = now
	item.RevokedByTokenID = actor.TokenID
	if _, err := tx.ExecContext(ctx, `UPDATE auth_token SET revoked_by_token_id = ?, revoked_at = ? WHERE id = ? AND revoked_at = 0`, actor.TokenID, now.UnixMilli(), item.ID); err != nil {
		return controlauth.Token{}, false, false, fmt.Errorf("revoke daemon token: %w", err)
	}
	changes, _ := json.Marshal(map[string]any{"revoked": true, "token_id": item.ID, "name": item.Name})
	if err := insertAuditTx(ctx, tx, controlauth.Audit{
		ID: identity.NewRandomID(identity.ResourceAudit), RequestID: requestID,
		TokenID: actor.TokenID, TokenName: actor.TokenName, Role: actor.Role, Origin: actor.Origin,
		Action: "auth.token.revoke", Resource: controlauth.Resource{Type: "auth-token", ID: item.ID},
		Status: controlauth.AuditStatusSucceeded, ParamsJSON: paramsJSON, ChangesJSON: string(changes), StartedAt: now, CompletedAt: now,
	}); err != nil {
		return controlauth.Token{}, false, false, err
	}
	if err := tx.Commit(); err != nil {
		return controlauth.Token{}, false, false, fmt.Errorf("commit token revoke: %w", err)
	}
	return item, true, false, nil
}

func (s *authStore) BeginAudit(ctx context.Context, audit controlauth.Audit) error {
	if _, err := s.db.ExecContext(ctx, auditInsert, auditValues(audit)...); err != nil {
		return fmt.Errorf("begin operation audit: %w", err)
	}
	return nil
}

func (s *authStore) FinishAudit(ctx context.Context, audit controlauth.Audit) error {
	result, err := s.db.ExecContext(ctx, `UPDATE operation_audit SET resource_type = ?, resource_id = ?, project_id = ?, status = ?, params_json = ?, changes_json = ?, error_code = ?, error_message = ?, completed_at = ? WHERE id = ?`,
		audit.Resource.Type, audit.Resource.ID, audit.Resource.ProjectID, audit.Status, audit.ParamsJSON, audit.ChangesJSON, audit.ErrorCode, audit.ErrorMessage, unixMillis(audit.CompletedAt), audit.ID)
	if err != nil {
		return fmt.Errorf("finish operation audit: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("finish operation audit %s: record not found", audit.ID)
	}
	return nil
}

func (s *authStore) ListAudits(ctx context.Context, filter controlauth.AuditFilter) (controlauth.AuditListResult, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	where := []string{"1 = 1"}
	args := make([]any, 0, 10)
	if filter.Token != "" {
		item, found, err := findAuthToken(ctx, s.db, filter.Token)
		if err != nil {
			return controlauth.AuditListResult{}, err
		}
		if !found {
			return controlauth.AuditListResult{}, controlauth.ErrTokenNotFound
		}
		where = append(where, "token_id = ?")
		args = append(args, item.ID)
	}
	appendTextFilter := func(column, value string) {
		if strings.TrimSpace(value) != "" {
			where = append(where, column+" = ?")
			args = append(args, strings.TrimSpace(value))
		}
	}
	appendTextFilter("action", filter.Action)
	appendTextFilter("resource_type", filter.ResourceType)
	appendTextFilter("resource_id", filter.ResourceID)
	appendTextFilter("project_id", filter.ProjectID)
	appendTextFilter("status", string(filter.Status))
	if !filter.StartedAfter.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, filter.StartedAfter.UnixMilli())
	}
	if !filter.StartedBefore.IsZero() {
		where = append(where, "started_at <= ?")
		args = append(args, filter.StartedBefore.UnixMilli())
	}
	if filter.BeforeSequence > 0 {
		where = append(where, "sequence < ?")
		args = append(args, filter.BeforeSequence)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, auditSelect+` WHERE `+strings.Join(where, " AND ")+` ORDER BY sequence DESC LIMIT ?`, args...)
	if err != nil {
		return controlauth.AuditListResult{}, fmt.Errorf("list operation audits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]controlauth.Audit, 0, limit+1)
	for rows.Next() {
		item, scanErr := scanAudit(rows.Scan)
		if scanErr != nil {
			return controlauth.AuditListResult{}, fmt.Errorf("scan operation audit: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return controlauth.AuditListResult{}, fmt.Errorf("iterate operation audits: %w", err)
	}
	result := controlauth.AuditListResult{Audits: items}
	if len(items) > limit {
		result.NextCursor = strconv.FormatUint(items[limit-1].Sequence, 10)
		result.Audits = items[:limit]
	}
	return result, nil
}

const authTokenSelect = `SELECT id, name, description, role, token_fingerprint, origin, created_by_token_id, client_request_id, created_at, revoked_by_token_id, revoked_at FROM auth_token`

func findAuthToken(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ref string) (controlauth.Token, bool, error) {
	ref = strings.TrimSpace(ref)
	row := query.QueryRowContext(ctx, authTokenSelect+` WHERE id = ? OR name = ? COLLATE NOCASE ORDER BY CASE WHEN id = ? THEN 0 ELSE 1 END LIMIT 1`, ref, ref, ref)
	item, err := scanAuthToken(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return controlauth.Token{}, false, nil
	}
	if err != nil {
		return controlauth.Token{}, false, fmt.Errorf("find daemon token %s: %w", ref, err)
	}
	return item, true, nil
}

func scanAuthToken(scan func(...any) error) (controlauth.Token, error) {
	var item controlauth.Token
	var role string
	var createdAt, revokedAt int64
	err := scan(&item.ID, &item.Name, &item.Description, &role, &item.Fingerprint, &item.Origin, &item.CreatedByTokenID, &item.ClientRequestID, &createdAt, &item.RevokedByTokenID, &revokedAt)
	if err != nil {
		return controlauth.Token{}, err
	}
	item.Role = controlauth.Role(role)
	item.CreatedAt = time.UnixMilli(createdAt).UTC()
	if revokedAt > 0 {
		item.RevokedAt = time.UnixMilli(revokedAt).UTC()
	}
	return item, nil
}

const auditInsert = `INSERT INTO operation_audit(
	id, request_id, token_id, token_name, role, origin, action, resource_type, resource_id, project_id,
	status, params_json, changes_json, error_code, error_message, started_at, completed_at
) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func insertAuditTx(ctx context.Context, tx *sql.Tx, audit controlauth.Audit) error {
	if _, err := tx.ExecContext(ctx, auditInsert, auditValues(audit)...); err != nil {
		return fmt.Errorf("insert operation audit: %w", err)
	}
	return nil
}

func auditValues(audit controlauth.Audit) []any {
	return []any{
		audit.ID, audit.RequestID, audit.TokenID, audit.TokenName, audit.Role, audit.Origin, audit.Action,
		audit.Resource.Type, audit.Resource.ID, audit.Resource.ProjectID, audit.Status,
		defaultJSON(audit.ParamsJSON), defaultJSON(audit.ChangesJSON), audit.ErrorCode, audit.ErrorMessage,
		unixMillis(audit.StartedAt), unixMillis(audit.CompletedAt),
	}
}

const auditSelect = `SELECT sequence, id, request_id, token_id, token_name, role, origin, action, resource_type, resource_id, project_id, status, params_json, changes_json, error_code, error_message, started_at, completed_at FROM operation_audit`

func scanAudit(scan func(...any) error) (controlauth.Audit, error) {
	var item controlauth.Audit
	var role, status string
	var startedAt, completedAt int64
	err := scan(&item.Sequence, &item.ID, &item.RequestID, &item.TokenID, &item.TokenName, &role, &item.Origin, &item.Action,
		&item.Resource.Type, &item.Resource.ID, &item.Resource.ProjectID, &status, &item.ParamsJSON, &item.ChangesJSON,
		&item.ErrorCode, &item.ErrorMessage, &startedAt, &completedAt)
	if err != nil {
		return controlauth.Audit{}, err
	}
	item.Role = controlauth.Role(role)
	item.Status = controlauth.AuditStatus(status)
	item.StartedAt = time.UnixMilli(startedAt).UTC()
	if completedAt > 0 {
		item.CompletedAt = time.UnixMilli(completedAt).UTC()
	}
	return item, nil
}

func defaultJSON(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "{}"
	}
	return value
}

func unixMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}
