package credentials

import (
	"encoding/json"
	"fmt"
	"time"
)

// HandleColumns is the column list ScanHandle reads, in order. It matches the
// credential_handle table introduced by migration 000018.
//
// Timestamps in that table are Unix milliseconds, converted at this boundary;
// the released second-based llm_facade_token columns are converted by the llms
// package instead.
const HandleColumns = "handle_id, kind, token_hash, token_fingerprint, env_name, sandbox_id, run_id, endpoint, owners_json, issued_at, expires_at, revoked_at"

// OwnersJSON encodes the handle's owners as canonical JSON for persistence.
// Normalization sorts and deduplicates, so equal grants produce equal bytes.
func (h Handle) OwnersJSON() (string, error) {
	owners := NormalizeOwners(h.Scope.Owners)
	if len(owners) == 0 {
		return "", fmt.Errorf("%w: %w", ErrInvalidHandle, ErrUnattributable)
	}
	encoded, err := json.Marshal(owners)
	if err != nil {
		return "", fmt.Errorf("encode credential handle owners: %w", err)
	}
	return string(encoded), nil
}

// ParseOwners decodes persisted owners. A malformed value is an error rather
// than an empty grant: turning it into "no owners" would silently change an
// authorization into a denial, and turning it into a valid owner would invent
// one.
func ParseOwners(raw string) ([]Owner, error) {
	if raw == "" {
		return nil, nil
	}
	var owners []Owner
	if err := json.Unmarshal([]byte(raw), &owners); err != nil {
		return nil, fmt.Errorf("decode credential handle owners: %w", err)
	}
	return NormalizeOwners(owners), nil
}

// ScanHandle reads one persisted handle. It is the credentials counterpart of
// llms.ScanFacadeToken and keeps every store's row shape next to the model it
// belongs to.
func ScanHandle(scan func(dest ...any) error) (Handle, error) {
	var (
		handle    Handle
		kind      string
		ownersRaw string
		issuedMS  int64
		expiresMS int64
		revokedMS int64
	)
	if err := scan(
		&handle.ID,
		&kind,
		&handle.TokenHash,
		&handle.TokenFingerprint,
		&handle.EnvName,
		&handle.SandboxID,
		&handle.RunID,
		&handle.Scope.Endpoint,
		&ownersRaw,
		&issuedMS,
		&expiresMS,
		&revokedMS,
	); err != nil {
		return Handle{}, err
	}
	owners, err := ParseOwners(ownersRaw)
	if err != nil {
		return Handle{}, err
	}
	handle.Kind = Kind(kind)
	handle.Scope.Owners = owners
	handle.IssuedAt = unixMillis(issuedMS)
	handle.ExpiresAt = unixMillis(expiresMS)
	handle.RevokedAt = unixMillis(revokedMS)
	return handle.Normalized(), nil
}

// UnixMillis converts a timestamp to the Unix-millisecond representation the
// credential_handle table stores. The zero time maps to 0, which the schema
// uses for "not set".
func UnixMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}

func unixMillis(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}
