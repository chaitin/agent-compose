package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/credentialhandle"
	"github.com/chaitin/agent-compose/pkg/credentials"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// credentialStore owns generic credential handles: the persisted record of a
// short-lived, narrowly scoped, revocable credential whose raw value is never
// stored.
type credentialStore struct {
	db     *sql.DB
	client *dao.Client
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
	// Kept as SQL: the upsert must preserve an existing revocation (the CASE),
	// which ent's upsert setters cannot express, and splitting it into a read
	// and a write would race a concurrent revoke.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO credential_handle(handle_id, kind, token_hash, token_fingerprint, env_name, sandbox_id, run_id, endpoint, owners_json, issued_at, expires_at, revoked_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(handle_id) DO UPDATE SET kind = excluded.kind, token_hash = excluded.token_hash, token_fingerprint = excluded.token_fingerprint, env_name = excluded.env_name, sandbox_id = excluded.sandbox_id, run_id = excluded.run_id, endpoint = excluded.endpoint, owners_json = excluded.owners_json, issued_at = excluded.issued_at, expires_at = excluded.expires_at, revoked_at = CASE WHEN credential_handle.revoked_at > 0 THEN credential_handle.revoked_at ELSE excluded.revoked_at END`,
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
	query := s.client.CredentialHandle.Query()
	switch column {
	case "token_hash":
		query = query.Where(credentialhandle.TokenHashEQ(value))
	case "handle_id":
		query = query.Where(credentialhandle.IDEQ(value))
	default:
		return credentials.Handle{}, fmt.Errorf("query credential handle: unknown lookup column %q", column)
	}
	entity, err := query.Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return credentials.Handle{}, domain.ErrNotFound
		}
		return credentials.Handle{}, fmt.Errorf("query credential handle: %w", err)
	}
	handle, err := credentialHandleFromEntity(entity)
	if err != nil {
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
	update := s.client.CredentialHandle.Update().
		SetRevokedAt(credentials.UnixMillis(time.Now().UTC())).
		Where(credentialhandle.RevokedAtEQ(0))
	switch column {
	case "token_hash":
		update = update.Where(credentialhandle.TokenHashEQ(value))
	case "handle_id":
		update = update.Where(credentialhandle.IDEQ(value))
	default:
		return fmt.Errorf("revoke credential handle: unknown lookup column %q", column)
	}
	affected, err := update.Save(ctx)
	if err != nil {
		return fmt.Errorf("revoke credential handle: %w", err)
	}
	if affected == 0 {
		// Either the row is gone or it was already revoked; distinguish so a
		// caller learns that the credential is already dead rather than missing.
		existsQuery := s.client.CredentialHandle.Query()
		switch column {
		case "token_hash":
			existsQuery = existsQuery.Where(credentialhandle.TokenHashEQ(value))
		case "handle_id":
			existsQuery = existsQuery.Where(credentialhandle.IDEQ(value))
		default:
			return fmt.Errorf("revoke credential handle: unknown lookup column %q", column)
		}
		exists, err := existsQuery.Exist(ctx)
		if err != nil {
			return fmt.Errorf("revoke credential handle: %w", err)
		}
		if !exists {
			return domain.ErrNotFound
		}
	}
	return nil
}

// CredentialHandleRetention is how long a dead handle row is kept before the
// sandbox sweep physically prunes it. Handles are short-lived by design
// (credentials.DefaultHandleTTL, at most credentials.MaxHandleTTL) and one run
// can mint many, so without a bound the table grows for the life of the
// deployment. The grace window keeps a recently-dead handle answerable for
// audit while bounding that growth; both states already fail closed at
// Authorize, so deleting them changes nothing observable.
const CredentialHandleRetention = time.Hour

// RevokeCredentialHandlesForSandbox releases every handle one sandbox holds. It
// is the sweep that makes "the sandbox is gone, so its credentials are gone"
// true even if the sandbox never called revoke itself.
//
// It also opportunistically prunes handles that have been dead for longer than
// CredentialHandleRetention, which is what keeps the table bounded across
// sandboxes.
//
// Both ways of dying get the same grace window: a handle that merely ran out
// (the common case, since DefaultHandleTTL is 15 minutes) is kept until it has
// been expired for CredentialHandleRetention, exactly like one that was
// revoked. That is deliberate and differs from llm_facade_token's prune, which
// drops expired rows immediately; the handle model is what an audit consults for
// "which credential was usable when", so "expired but still within the window"
// must remain answerable. Both states fail closed at Authorize either way.
func (s *credentialStore) RevokeCredentialHandlesForSandbox(ctx context.Context, sandboxID string) error {
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return nil
	}
	now := time.Now().UTC()
	if _, err := s.client.CredentialHandle.Update().
		SetRevokedAt(credentials.UnixMillis(now)).
		Where(credentialhandle.SandboxIDEQ(sandboxID), credentialhandle.RevokedAtEQ(0)).
		Save(ctx); err != nil {
		return fmt.Errorf("revoke credential handles for sandbox: %w", err)
	}
	cutoff := credentials.UnixMillis(now.Add(-CredentialHandleRetention))
	if _, err := s.client.CredentialHandle.Delete().
		Where(credentialhandle.Or(
			credentialhandle.And(credentialhandle.RevokedAtNEQ(0), credentialhandle.RevokedAtLT(cutoff)),
			credentialhandle.And(credentialhandle.ExpiresAtNEQ(0), credentialhandle.ExpiresAtLT(cutoff)),
		)).
		Exec(ctx); err != nil {
		return fmt.Errorf("prune credential handles: %w", err)
	}
	return nil
}

// ListCredentialHandlesForSandbox returns every handle a sandbox still holds,
// including revoked and expired ones, so an audit or capability report can show
// the grant history rather than only what is still live. The history is bounded
// by CredentialHandleRetention: RevokeCredentialHandlesForSandbox removes a
// handle once it has been dead for longer than that grace window.
func (s *credentialStore) ListCredentialHandlesForSandbox(ctx context.Context, sandboxID string) ([]credentials.Handle, error) {
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return nil, nil
	}
	entities, err := s.client.CredentialHandle.Query().
		Where(credentialhandle.SandboxIDEQ(sandboxID)).
		Order(credentialhandle.ByIssuedAt(), credentialhandle.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query credential handles for sandbox: %w", err)
	}
	handles := make([]credentials.Handle, 0, len(entities))
	for _, entity := range entities {
		handle, err := credentialHandleFromEntity(entity)
		if err != nil {
			return nil, fmt.Errorf("scan credential handle: %w", err)
		}
		handles = append(handles, handle)
	}
	return handles, nil
}

// credentialHandleFromEntity maps credential_handle to the domain handle,
// keeping the released Unix-millisecond column units (0 means "not set").
func credentialHandleFromEntity(entity *dao.CredentialHandle) (credentials.Handle, error) {
	owners, err := credentials.ParseOwners(entity.OwnersJSON)
	if err != nil {
		return credentials.Handle{}, err
	}
	handle := credentials.Handle{
		ID:               entity.ID,
		Kind:             credentials.Kind(entity.Kind),
		TokenHash:        entity.TokenHash,
		TokenFingerprint: entity.TokenFingerprint,
		EnvName:          entity.EnvName,
		SandboxID:        entity.SandboxID,
		RunID:            entity.RunID,
		Scope:            credentials.Scope{Endpoint: entity.Endpoint, Owners: owners},
		IssuedAt:         storedUnixMillis(entity.IssuedAt),
		ExpiresAt:        storedUnixMillis(entity.ExpiresAt),
		RevokedAt:        storedUnixMillis(entity.RevokedAt),
	}
	return handle.Normalized(), nil
}

// storedUnixMillis converts a released Unix-millisecond column to time.
func storedUnixMillis(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}
