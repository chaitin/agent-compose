package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/storage/dialect"
)

func TestPostgresMigrationsIntegration(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	schema, admin := newTestSchema(t, ctx, dsn)
	db, err := openDB(withSearchPath(dsn, schema))
	if err != nil {
		t.Fatalf("open schema database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}

	statuses, err := Inspect(ctx, db)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Version != baselineMigrationVersion || !statuses[0].Applied {
		t.Fatalf("unexpected migration status: %+v", statuses)
	}

	for _, table := range []string{"project", "project_run", "event", "scheduler_run", "sandboxes"} {
		var exists bool
		if err := admin.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2
		)`, schema, table).Scan(&exists); err != nil {
			t.Fatalf("inspect table %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %s was not created", table)
		}
	}

	lockTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin run history lock transaction: %v", err)
	}
	if _, err := lockTx.ExecContext(ctx, dialect.Postgres().LockRunHistoryTablesStatement()); err != nil {
		_ = lockTx.Rollback()
		t.Fatalf("run history lock statement: %v", err)
	}
	if err := lockTx.Rollback(); err != nil {
		t.Fatalf("rollback run history lock: %v", err)
	}

	var sequence int64
	if err := db.QueryRowContext(ctx, `INSERT INTO event(id, topic, source, correlation_id, payload_hash, payload_json, dispatch_status, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?) RETURNING sequence`,
		"e1", "test", "test", "c1", "h", `{}`, "pending", time.Now().UnixMilli()).Scan(&sequence); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if sequence != 1 {
		t.Fatalf("event sequence = %d, want 1", sequence)
	}
}

func TestPostgresConcurrentMigrationIntegration(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	schema, _ := newTestSchema(t, ctx, dsn)

	dsnWithSchema := withSearchPath(dsn, schema)
	first, err := openDB(dsnWithSchema)
	if err != nil {
		t.Fatalf("open first database: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := openDB(dsnWithSchema)
	if err != nil {
		t.Fatalf("open second database: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, db := range []*sql.DB{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- Migrate(ctx, db)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent migrate: %v", err)
		}
	}
	statuses, err := Inspect(ctx, first)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(statuses) != 1 || !statuses[0].Applied {
		t.Fatalf("unexpected migration status: %+v", statuses)
	}
}

func TestPostgresMigrationRejectsUnversionedSchemaIntegration(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema, admin := newTestSchema(t, ctx, dsn)
	if _, err := admin.ExecContext(ctx, `CREATE TABLE `+schema+`.legacy(id INT)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	db, err := openDB(withSearchPath(dsn, schema))
	if err != nil {
		t.Fatalf("open schema database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err == nil {
		t.Fatal("Migrate() error = nil, want unversioned schema error")
	}
	statuses, err := Inspect(ctx, db)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	for _, status := range statuses {
		if status.Applied {
			t.Fatalf("unexpected applied migration: %+v", status)
		}
	}
}

func TestPostgresDatabaseOpenIntegration(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	schema, _ := newTestSchema(t, ctx, dsn)

	db, err := Open(ctx, Config{
		URL:             withSearchPath(dsn, schema),
		MaxOpenConns:    6,
		MaxIdleConns:    3,
		ConnMaxLifetime: time.Minute,
		ConnectTimeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if got := db.DB().Stats().MaxOpenConnections; got != 6 {
		t.Fatalf("MaxOpenConnections = %d, want 6", got)
	}
	if err := db.DB().PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestPostgresInstanceLockIntegration(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema, _ := newTestSchema(t, ctx, dsn)
	db, err := openDB(withSearchPath(dsn, schema))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	lock, err := AcquireInstanceLock(ctx, db)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	if _, err := AcquireInstanceLock(ctx, db); !errors.Is(err, ErrInstanceLocked) {
		t.Fatalf("second lock error = %v, want ErrInstanceLocked", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}
	second, err := AcquireInstanceLock(ctx, db)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("release second lock: %v", err)
	}
}

func testPostgresDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(testPostgresDSNEnv))
	if dsn == "" {
		t.Skipf("set %s to run PostgreSQL integration tests", testPostgresDSNEnv)
	}
	return dsn
}

func newTestSchema(t *testing.T, ctx context.Context, dsn string) (string, *sql.DB) {
	t.Helper()
	admin, err := openDB(dsn)
	if err != nil {
		t.Fatalf("open admin database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("ac_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
	})
	return schema, admin
}

func TestPostgresInstanceLockMonitorReacquiresIntegration(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema, _ := newTestSchema(t, ctx, dsn)
	db, err := openDB(withSearchPath(dsn, schema))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	lock, err := AcquireInstanceLock(ctx, db)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	monitorDone := make(chan error, 1)
	go func() { monitorDone <- lock.Monitor(monitorCtx, 20*time.Millisecond, 2*time.Second) }()
	t.Cleanup(func() {
		stopMonitor()
		select {
		case err := <-monitorDone:
			if err != nil {
				t.Errorf("monitor stopped with error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("monitor did not stop")
		}
		if err := lock.Close(); err != nil {
			t.Errorf("close lock: %v", err)
		}
	})

	// Simulate the PostgreSQL session dying: the advisory lock is released and
	// Monitor must take a new one.
	lock.mu.Lock()
	old := lock.conn
	lock.conn = nil
	lock.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		lock.mu.Lock()
		reacquired := lock.conn != nil
		lock.mu.Unlock()
		if reacquired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("instance lock was not reacquired")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := AcquireInstanceLock(ctx, db); !errors.Is(err, ErrInstanceLocked) {
		t.Fatalf("lock after reacquire error = %v, want ErrInstanceLocked", err)
	}
}

// TestPostgresInstanceLockReacquireHonorsRetryWindowIntegration pins the retry
// window around the blocking operations. A saturated pool must not keep the
// monitor stuck beyond the configured recovery window.
func TestPostgresInstanceLockReacquireHonorsRetryWindowIntegration(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, err := openDB(dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(0)
	held, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("hold pool connection: %v", err)
	}
	t.Cleanup(func() { _ = held.Close() })

	lock := &InstanceLock{db: pool, key: advisoryKey("test-reacquire-window")}
	started := time.Now()
	err = lock.reacquire(ctx, 250*time.Millisecond)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("reacquire returned nil while the pool was saturated")
	}
	if !strings.Contains(err.Error(), "could not be reacquired within") {
		t.Fatalf("reacquire error = %v, want retry-window error", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("reacquire took %s, want it bounded by the retry window", elapsed)
	}
}
