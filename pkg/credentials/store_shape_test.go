package credentials_test

import (
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

func TestScanHandleRoundTripsPersistedRow(t *testing.T) {
	handle := baseHandle()
	handle.RevokedAt = testNow.Add(2 * time.Minute)
	ownersJSON, err := handle.OwnersJSON()
	if err != nil {
		t.Fatalf("OwnersJSON() error = %v", err)
	}
	scan := func(dest ...any) error {
		*dest[0].(*string) = handle.ID
		*dest[1].(*string) = string(handle.Kind)
		*dest[2].(*string) = handle.TokenHash
		*dest[3].(*string) = handle.TokenFingerprint
		*dest[4].(*string) = handle.EnvName
		*dest[5].(*string) = handle.SandboxID
		*dest[6].(*string) = handle.RunID
		*dest[7].(*string) = handle.Scope.Endpoint
		*dest[8].(*string) = ownersJSON
		*dest[9].(*int64) = credentials.UnixMillis(handle.IssuedAt)
		*dest[10].(*int64) = credentials.UnixMillis(handle.ExpiresAt)
		*dest[11].(*int64) = credentials.UnixMillis(handle.RevokedAt)
		return nil
	}
	got, err := credentials.ScanHandle(scan)
	if err != nil {
		t.Fatalf("ScanHandle() error = %v", err)
	}
	if !got.IssuedAt.Equal(handle.IssuedAt) || !got.ExpiresAt.Equal(handle.ExpiresAt) || !got.RevokedAt.Equal(handle.RevokedAt) {
		t.Fatalf("ScanHandle() times = %s/%s/%s, want %s/%s/%s", got.IssuedAt, got.ExpiresAt, got.RevokedAt, handle.IssuedAt, handle.ExpiresAt, handle.RevokedAt)
	}
	if got.Kind != handle.Kind || got.Scope.Endpoint != handle.Scope.Endpoint || len(got.Scope.Owners) != 2 {
		t.Fatalf("ScanHandle() = %#v, want the persisted handle", got)
	}
}

func TestUnixMillisUsesZeroForUnsetTimes(t *testing.T) {
	if got := credentials.UnixMillis(time.Time{}); got != 0 {
		t.Fatalf("UnixMillis(zero) = %d, want 0", got)
	}
	if got := credentials.UnixMillis(testNow); got != testNow.UnixMilli() {
		t.Fatalf("UnixMillis(now) = %d, want %d", got, testNow.UnixMilli())
	}
}
