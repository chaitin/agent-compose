package sessionstore

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "agent-compose/pkg/config"
)

func TestRemoveSessionDeletesSessionDirectory(t *testing.T) {
	store := newRemoveSessionTestStore(t)
	sessionID := "session-remove"
	sessionDir := store.SessionDir(sessionID)
	if err := os.MkdirAll(filepath.Join(sessionDir, "state"), 0o755); err != nil {
		t.Fatalf("create session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "state", "events.json"), []byte("[]\n"), 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	unlock := store.lockSession(sessionID)
	unlock()
	if _, ok := store.sessionLocks.Load(sessionID); !ok {
		t.Fatalf("session lock was not initialized")
	}

	if err := store.RemoveSession(context.Background(), sessionID); err != nil {
		t.Fatalf("RemoveSession returned error: %v", err)
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("session dir stat err = %v, want not exist", err)
	}
	if _, ok := store.sessionLocks.Load(sessionID); ok {
		t.Fatalf("session lock was not removed")
	}
}

func TestRemoveSessionRejectsUnsafeIDs(t *testing.T) {
	store := newRemoveSessionTestStore(t)
	outside := filepath.Join(filepath.Dir(store.config.SessionRoot), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}

	for _, id := range []string{"", " ", ".", "..", "../outside", "nested/session"} {
		err := store.RemoveSession(context.Background(), id)
		if err == nil {
			t.Fatalf("RemoveSession(%q) returned nil error", id)
		}
		if !strings.Contains(err.Error(), "session id") {
			t.Fatalf("RemoveSession(%q) error = %v, want session id validation", id, err)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside dir stat err = %v, want preserved", err)
	}
}

func TestRemoveSessionMissingDirectoryReturnsError(t *testing.T) {
	store := newRemoveSessionTestStore(t)
	sessionID := "missing-session"
	err := store.RemoveSession(context.Background(), sessionID)
	if err == nil {
		t.Fatal("RemoveSession missing session returned nil error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("RemoveSession missing error = %v, want not exist", err)
	}
	if _, ok := store.sessionLocks.Load(sessionID); ok {
		t.Fatalf("missing session created a lock entry")
	}
}

func newRemoveSessionTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewWithConfig(&appconfig.Config{SessionRoot: filepath.Join(t.TempDir(), "sessions")})
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	return store
}
