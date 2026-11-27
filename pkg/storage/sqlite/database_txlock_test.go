package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// openLockTestDatabase opens a migrated database with a short busy timeout. The
// timeout is long enough for the test's competing writer to commit but short
// enough that a regression fails the test instead of hanging it.
func openLockTestDatabase(t *testing.T) *Database {
	t.Helper()
	db, err := OpenWithMaxOpenConns(filepath.Join(t.TempDir(), "data.db"), 2*time.Second, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := db.DB().Exec(`CREATE TABLE txlock_probe (value INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestWriteTransactionWaitsForConcurrentWriter pins the BEGIN mode: a write
// transaction whose first statement is a read must wait for a concurrent writer
// under the busy timeout. A deferred transaction fails here immediately with
// SQLITE_BUSY instead of waiting.
func TestWriteTransactionWaitsForConcurrentWriter(t *testing.T) {
	db := openLockTestDatabase(t)
	ctx := context.Background()

	holder, err := db.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx, `INSERT INTO txlock_probe(value) VALUES (1)`); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		tx, err := db.DB().BeginTx(ctx, nil)
		if err != nil {
			done <- err
			return
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM txlock_probe`).Scan(&count); err != nil {
			_ = tx.Rollback()
			done <- err
			return
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO txlock_probe(value) VALUES (2)`); err != nil {
			_ = tx.Rollback()
			done <- err
			return
		}
		done <- tx.Commit()
	}()

	// Give the competing transaction time to reach the lock. Without an
	// immediate BEGIN it reports SQLITE_BUSY here instead of waiting.
	select {
	case err := <-done:
		_ = holder.Rollback()
		t.Fatalf("write transaction did not wait for the concurrent writer: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("write transaction failed after the writer released the lock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write transaction did not finish after the writer released the lock")
	}
}

// TestReadOnlyTransactionDoesNotTakeWriterLock keeps read-only transactions
// deferred, so they never serialize against writers.
func TestReadOnlyTransactionDoesNotTakeWriterLock(t *testing.T) {
	db := openLockTestDatabase(t)
	ctx := context.Background()

	reader, err := db.DB().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Rollback() }()
	var count int
	if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM txlock_probe`).Scan(&count); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := db.DB().ExecContext(ctx, `INSERT INTO txlock_probe(value) VALUES (3)`)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("writer failed behind an open read-only transaction: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read-only transaction held the writer lock")
	}
}
