package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/credentials"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// credentialStore owns generic credential handles: the persisted record of a
// short-lived, narrowly scoped, revocable credential whose raw value is never
// stored.
type credentialStore struct {
	db *sql.DB
}

// SaveCredentialHandle persists a handle. Only the handle's hash is written, so
// the credential truth exists solely between minting and the sandbox secret
// mechanism.
func (s *credentialStore) SaveCredentialHandle(ctx context.Context, handle credentials.Handle) error {
	handle = handle.Normalized()
	if err := handle.Validate(); err != nil {
		return fmt.Errorf("save credential handle: %w", err)
	}
	ownersJSON, err := handle.OwnersJSON()
	if err != nil {
		return fmt.Errorf("save credential handle: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO credential_handle(handle_id, kind, token_hash, token_fingerprint, env_name, sandbox_id, run_id, endpoint, owners_json, issued_at, expires_at, revoked_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(handle_id) DO UPDATE SET kind = excluded.kind, token_hash = excluded.token_hash, token_fingerprint = excluded.token_fingerprint, env_name = excluded.env_name, sandbox_id = excluded.sandbox_id, run_id = excluded.run_id, endpoint = excluded.endpoint, owners_json = excluded.owners_json, issued_at = excluded.issued_at, expires_at = excluded.expires_at, revoked_at = excluded.revoked_at`,
		handle.ID,
		string(handle.Kind),
		handle.TokenHash,
		handle.TokenFingerprint,
		handle.EnvName,
		handle.SandboxID,
		handle.RunID,
		handle.Scope.Endpoint,
		ownersJSON,
		credentials.UnixMillis(handle.IssuedAt),
		credentials.UnixMillis(handle.ExpiresAt),
		credentials.UnixMillis(handle.RevokedAt),
	); err != nil {
		return fmt.Errorf("save credential handle %s: %w", handle.ID, err)
	}
	return nil
}

// GetCredentialHandle resolves a raw bearer value to its handle. The lookup is
// by hash; the raw value is never compared or stored.
func (s *credentialStore) GetCredentialHandle(ctx context.Context, rawToken string) (credentials.Handle, error) {
	hash, fingerprint := credentials.HashToken(rawToken)
	handle, err := s.getCredentialHandleByColumn(ctx, "token_hash", hash)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return credentials.Handle{}, domain.ResourceError(domain.ErrNotFound, "credential handle", fingerprint, fmt.Sprintf("credential handle %s not found", fingerprint), err)
		}
		return credentials.Handle{}, err
	}
	return handle, nil
}

// GetCredentialHandleByID resolves a handle by its public identity, which is
// how an injection path selects a handle without ever handling the raw value.
func (s *credentialStore) GetCredentialHandleByID(ctx context.Context, handleID string) (credentials.Handle, error) {
	handleID = strings.TrimSpace(handleID)
	if handleID == "" {
		return credentials.Handle{}, domain.ResourceError(domain.ErrNotFound, "credential handle", "", "credential handle id is required", nil)
	}
	handle, err := s.getCredentialHandleByColumn(ctx, "handle_id", handleID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return credentials.Handle{}, domain.ResourceError(domain.ErrNotFound, "credential handle", handleID, fmt.Sprintf("credential handle %s not found", handleID), err)
		}
		return credentials.Handle{}, err
	}
	return handle, nil
}

func (s *credentialStore) getCredentialHandleByColumn(ctx context.Context, column, value string) (credentials.Handle, error) {
	// The column is chosen by this package, never by a caller, so interpolating
	// it cannot inject SQL; the value is a bound parameter.
	row := s.db.QueryRowContext(ctx, `SELECT `+credentials.HandleColumns+` FROM credential_handle WHERE `+column+` = ?`, value)
	handle, err := credentials.ScanHandle(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return credentials.Handle{}, domain.ErrNotFound
		}
		return credentials.Handle{}, fmt.Errorf("query credential handle: %w", err)
	}
	return handle, nil
}

// RevokeCredentialHandle revokes the handle selected by a raw bearer value.
func (s *credentialStore) RevokeCredentialHandle(ctx context.Context, rawToken string) error {
	hash, fingerprint := credentials.HashToken(rawToken)
	if err := s.revokeCredentialHandleByColumn(ctx, "token_hash", hash); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ResourceError(domain.ErrNotFound, "credential handle", fingerprint, fmt.Sprintf("credential handle %s not found", fingerprint), err)
		}
		return err
	}
	return nil
}

// RevokeCredentialHandleByID revokes one handle by its public identity.
func (s *credentialStore) RevokeCredentialHandleByID(ctx context.Context, handleID string) error {
	handleID = strings.TrimSpace(handleID)
	if handleID == "" {
		return domain.ResourceError(domain.ErrNotFound, "credential handle", "", "credential handle id is required", nil)
	}
	if err := s.revokeCredentialHandleByColumn(ctx, "handle_id", handleID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ResourceError(domain.ErrNotFound, "credential handle", handleID, fmt.Sprintf("credential handle %s not found", handleID), err)
		}
		return err
	}
	return nil
}

func (s *credentialStore) revokeCredentialHandleByColumn(ctx context.Context, column, value string) error {
	// Revocation is idempotent: an already-revoked handle keeps its original
	// revocation instant so "when did this stop being valid" stays answerable.
	result, err := s.db.ExecContext(ctx, `UPDATE credential_handle SET revoked_at = ? WHERE `+column+` = ? AND revoked_at = 0`, credentials.UnixMillis(time.Now().UTC()), value)
	if err != nil {
		return fmt.Errorf("revoke credential handle: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke credential handle: %w", err)
	}
	if affected == 0 {
		// Either the row is gone or it was already revoked; distinguish so a
		// caller learns that the credential is already dead rather than missing.
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM credential_handle WHERE `+column+` = ?)`, value).Scan(&exists); err != nil {
			return fmt.Errorf("revoke credential handle: %w", err)
		}
		if !exists {
			return domain.ErrNotFound
		}
	}
	return nil
}

// RevokeCredentialHandlesForSandbox releases every handle one sandbox holds. It
// is the sweep that makes "the sandbox is gone, so its credentials are gone"
// true even if the sandbox never called revoke itself.
func (s *credentialStore) RevokeCredentialHandlesForSandbox(ctx context.Context, sandboxID string) error {
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE credential_handle SET revoked_at = ? WHERE sandbox_id = ? AND revoked_at = 0`, credentials.UnixMillis(time.Now().UTC()), sandboxID); err != nil {
		return fmt.Errorf("revoke credential handles for sandbox: %w", err)
	}
	return nil
}

// ListCredentialHandlesForSandbox returns every handle a sandbox holds,
// including revoked and expired ones, so an audit or capability report can show
// the full grant history rather than only what is still live.
func (s *credentialStore) ListCredentialHandlesForSandbox(ctx context.Context, sandboxID string) ([]credentials.Handle, error) {
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+credentials.HandleColumns+` FROM credential_handle WHERE sandbox_id = ? ORDER BY issued_at ASC, handle_id ASC`, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("query credential handles for sandbox: %w", err)
	}
	defer func() { _ = rows.Close() }()
	handles := make([]credentials.Handle, 0)
	for rows.Next() {
		handle, err := credentials.ScanHandle(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan credential handle: %w", err)
		}
		handles = append(handles, handle)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate credential handles: %w", err)
	}
	return handles, nil
}
