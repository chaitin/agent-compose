package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"

	"github.com/chaitin/agent-compose/pkg/storage/migrations"
)

const baselineMigrationVersion int64 = 1

const legacyMigratorHint = "this data root requires migration; see the agent-compose documentation for the required procedure"

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

type migration = migrations.Asset

// EmbeddedMigration is a read-only view of an immutable migration asset.
// Copy-migration tools use it to adopt an unversioned historical schema
// without duplicating the released SQL files.
type EmbeddedMigration = migrations.Asset

type appliedMigration = migrations.Applied

type migrationConn interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Migrate upgrades an already-open application database. Production callers
// should use Open so connection ownership and migration ordering stay unified.
func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("SQLite database is required")
	}
	return applyMigrations(ctx, db, embeddedMigrations)
}

// MigrateThrough applies the embedded migration prefix ending at maxVersion.
// It is intended for tools that must perform a data conversion between two
// released schema versions.
func MigrateThrough(ctx context.Context, db *sql.DB, maxVersion int64) error {
	if db == nil {
		return fmt.Errorf("SQLite database is required")
	}
	migrations, err := loadMigrations(embeddedMigrations)
	if err != nil {
		return err
	}
	end := sort.Search(len(migrations), func(index int) bool { return migrations[index].Version > maxVersion })
	if end == 0 {
		return fmt.Errorf("no embedded SQLite migrations through version %d", maxVersion)
	}
	return applyMigrationSet(ctx, db, migrations[:end])
}

// EmbeddedMigrationAt returns an immutable embedded migration by version.
func EmbeddedMigrationAt(version int64) (EmbeddedMigration, bool, error) {
	migrations, err := loadMigrations(embeddedMigrations)
	if err != nil {
		return EmbeddedMigration{}, false, err
	}
	for _, item := range migrations {
		if item.Version == version {
			return EmbeddedMigration(item), true, nil
		}
	}
	return EmbeddedMigration{}, false, nil
}

func applyMigrations(ctx context.Context, db *sql.DB, migrationFS fs.FS) error {
	migrations, err := loadMigrations(migrationFS)
	if err != nil {
		return err
	}
	if migrations[0].Version != baselineMigrationVersion {
		return fmt.Errorf("first SQLite migration must be version %06d", baselineMigrationVersion)
	}
	return applyMigrationSet(ctx, db, migrations)
}

func applyMigrationSet(ctx context.Context, db *sql.DB, migrations []migration) error {
	// Pin the whole upgrade to one physical connection. database/sql does not
	// expose BEGIN IMMEDIATE via TxOptions, so transaction control must stay on
	// this connection explicitly.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire SQLite migration connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Take the write reservation before reading migration history. Concurrent
	// daemon startups therefore cannot both decide that the same version is
	// pending.
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin SQLite migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// A canceled caller must not prevent cleanup of the open transaction.
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`)
		}
	}()

	hasHistory, err := migrationHistoryExists(ctx, conn)
	if err != nil {
		return err
	}
	var applied []appliedMigration
	if hasHistory {
		applied, err = loadAppliedMigrations(ctx, conn)
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			return fmt.Errorf("SQLite migration history is empty in a non-empty database; %s", legacyMigratorHint)
		}
	} else {
		nonEmpty, err := hasApplicationSchema(ctx, conn)
		if err != nil {
			return err
		}
		if nonEmpty {
			return fmt.Errorf("unversioned SQLite database is not supported by the daemon; %s", legacyMigratorHint)
		}
	}
	if err := validateAppliedMigrations(migrations, applied); err != nil {
		return fmt.Errorf("%w; %s", err, legacyMigratorHint)
	}

	if !hasHistory {
		if _, err := conn.ExecContext(ctx, `CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			checksum TEXT NOT NULL,
			applied_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
		)`); err != nil {
			return fmt.Errorf("create SQLite migration history: %w", err)
		}
	}

	pending := migrations[len(applied):]
	for _, item := range pending {
		if err := applyMigrationDataStep(ctx, conn, item); err != nil {
			return fmt.Errorf("prepare SQLite migration %s: %w", item.Name, err)
		}
		if _, err := conn.ExecContext(ctx, item.Statement); err != nil {
			return fmt.Errorf("apply SQLite migration %s: %w", item.Name, err)
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, checksum) VALUES(?, ?, ?)`,
			item.Version, item.Name, item.Checksum); err != nil {
			return fmt.Errorf("record SQLite migration %s: %w", item.Name, err)
		}
	}

	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit SQLite migrations: %w", err)
	}
	committed = true
	for _, item := range pending {
		slog.Info("SQLite migration applied", "version", item.Version, "name", item.Name)
	}
	return nil
}

func migrationHistoryExists(ctx context.Context, conn migrationConn) (bool, error) {
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect SQLite migration history: %w", err)
	}
	return count == 1, nil
}

func hasApplicationSchema(ctx context.Context, conn migrationConn) (bool, error) {
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type IN ('table', 'index', 'trigger', 'view')
		AND name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect SQLite schema before migration: %w", err)
	}
	return count != 0, nil
}

func loadMigrations(migrationFS fs.FS) ([]migration, error) {
	return migrations.Load(migrationFS, "SQLite")
}

func loadAppliedMigrations(ctx context.Context, conn migrationConn) ([]appliedMigration, error) {
	rows, err := conn.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version ASC`) //nolint:rowserrcheck // migrations.ScanApplied checks rows.Err before returning.
	if err != nil {
		return nil, fmt.Errorf("load applied SQLite migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return migrations.ScanApplied(rows, "SQLite")
}

func validateAppliedMigrations(available []migration, applied []appliedMigration) error {
	return migrations.ValidateApplied("SQLite", available, applied)
}

// LatestMigrationVersion returns the highest embedded SQLite migration
// version. Offline copy tools use it to reject a source database that has not
// been upgraded by a matching daemon yet.
func LatestMigrationVersion() (int64, error) {
	migrations, err := loadMigrations(embeddedMigrations)
	if err != nil {
		return 0, err
	}
	return migrations[len(migrations)-1].Version, nil
}

// AppliedMigrationVersion returns the highest recorded migration version, or
// zero when the database has no migration history.
func AppliedMigrationVersion(ctx context.Context, db *sql.DB) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("SQLite database is required")
	}
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read SQLite migration version: %w", err)
	}
	return version, nil
}
