package credentials_test

import (
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

func TestUnixMillisUsesZeroForUnsetTimes(t *testing.T) {
	if got := credentials.UnixMillis(time.Time{}); got != 0 {
		t.Fatalf("UnixMillis(zero) = %d, want 0", got)
	}
	if got := credentials.UnixMillis(testNow); got != testNow.UnixMilli() {
		t.Fatalf("UnixMillis(now) = %d, want %d", got, testNow.UnixMilli())
	}
}
