package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenReadOnlyReadsButDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	owner, err := Open(path, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("open writable database: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })

	if _, err := owner.DB().ExecContext(context.Background(), `CREATE TABLE probe(value TEXT)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	if _, err := owner.DB().ExecContext(context.Background(), `INSERT INTO probe(value) VALUES('ok')`); err != nil {
		t.Fatalf("insert probe row: %v", err)
	}

	readOnly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("open read-only database: %v", err)
	}
	t.Cleanup(func() { _ = readOnly.Close() })

	var value string
	if err := readOnly.DB().QueryRowContext(context.Background(), `SELECT value FROM probe`).Scan(&value); err != nil {
		t.Fatalf("read probe row: %v", err)
	}
	if value != "ok" {
		t.Fatalf("probe value = %q, want %q", value, "ok")
	}

	_, err = readOnly.DB().ExecContext(context.Background(), `INSERT INTO probe(value) VALUES('forbidden')`)
	if err == nil {
		t.Fatal("write through read-only connection succeeded, want an error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "readonly") && !strings.Contains(strings.ToLower(err.Error()), "read-only") {
		t.Fatalf("write error = %v, want a read-only database error", err)
	}
}

func TestOpenReadOnlyRejectsMissingDatabase(t *testing.T) {
	_, err := OpenReadOnly(filepath.Join(t.TempDir(), "missing", "data.db"))
	if err == nil {
		t.Fatal("OpenReadOnly succeeded for a missing database")
	}
}

func TestOpenReadOnlyRejectsMemoryDatabase(t *testing.T) {
	_, err := OpenReadOnly(":memory:")
	if err == nil {
		t.Fatal("OpenReadOnly accepted a memory database")
	}
}
