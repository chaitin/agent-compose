package credentials

import (
	"encoding/json"
	"fmt"
	"time"
)

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

// UnixMillis converts a timestamp to the Unix-millisecond representation the
// credential_handle table stores. The zero time maps to 0, which the schema
// uses for "not set".
func UnixMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}
