package postgres

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestConnectErrorDoesNotLeakPassword(t *testing.T) {
	for _, dsn := range []string{
		"postgres://user:supersecret@127.0.0.1:1/agent_compose?sslmode=disable",
		"postgres://user:supersecret@/agent_compose",
		"not-a-url:supersecret",
	} {
		_, err := Connect(context.Background(), Config{URL: dsn, ConnectTimeout: 500 * time.Millisecond})
		if err == nil {
			t.Fatalf("Connect(%q) returned nil error", dsn)
		}
		if strings.Contains(err.Error(), "supersecret") {
			t.Fatalf("Connect(%q) error leaked the password: %v", dsn, err)
		}
	}
}
