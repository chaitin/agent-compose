package postgres

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/storage/migrations"
)

const (
	baselineMigrationVersion int64 = 1
	// sqliteBaselineVersion is the released SQLite migration the PostgreSQL
	// baseline mirrors. Later PostgreSQL migrations reuse the SQLite version
	// numbers so both chains stay aligned.
	sqliteBaselineVersion int64 = 20

	migrationHistoryTable = "schema_migrations"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

type migration = migrations.Asset

type appliedMigration = migrations.Applied

// MigrationStatus describes one embedded migration relative to an applied
// database. It is used by `agent-compose db status`.
type MigrationStatus struct {
	Version int64
	Name    string
	Applied bool
}

// Migrate upgrades an already-open PostgreSQL database. The caller owns the
// connection. Concurrent daemons serialize through a transaction-scoped
// advisory lock, so only one applies pending migrations.
// SQLiteBaselineVersion returns the SQLite migration version the PostgreSQL
// baseline mirrors. Later migrations use matching version numbers in both
// chains.
func SQLiteBaselineVersion() int64 { return sqliteBaselineVersion }

// LatestMigrationVersion returns the highest embedded PostgreSQL migration
// version. It is reported by `agent-compose db status`.
func LatestMigrationVersion() (int64, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	return migrations[len(migrations)-1].Version, nil
}

func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("PostgreSQL database is required")
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	return applyMigrations(ctx, db, migrations)
}

// Inspect reports which embedded migrations have been applied. It does not
// modify the database.
func Inspect(ctx context.Context, db *sql.DB) ([]MigrationStatus, error) {
	if db == nil {
		return nil, fmt.Errorf("PostgreSQL database is required")
	}
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	applied, exists, err := loadAppliedMigrations(ctx, db)
	if err != nil {
		return nil, err
	}
	if !exists {
		statuses := make([]MigrationStatus, len(migrations))
		for index, item := range migrations {
			statuses[index] = MigrationStatus{Version: item.Version, Name: item.Name}
		}
		return statuses, nil
	}
	if err := validateAppliedMigrations(migrations, applied); err != nil {
		return nil, err
	}
	statuses := make([]MigrationStatus, len(migrations))
	for index, item := range migrations {
		statuses[index] = MigrationStatus{
			Version: item.Version,
			Name:    item.Name,
			Applied: index < len(applied),
		}
	}
	return statuses, nil
}

func applyMigrations(ctx context.Context, db *sql.DB, migrations []migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin PostgreSQL migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	schema, err := currentSchema(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, advisoryKey("agent-compose:migrations:"+schema)); err != nil {
		return fmt.Errorf("acquire PostgreSQL migration lock: %w", err)
	}

	applied, exists, err := loadAppliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	if exists && len(applied) == 0 {
		return fmt.Errorf("PostgreSQL migration history is empty in a non-empty database; create an empty schema and rerun migrations")
	}
	if !exists {
		nonEmpty, err := schemaHasApplicationTables(ctx, tx)
		if err != nil {
			return err
		}
		if nonEmpty {
			return fmt.Errorf("unversioned PostgreSQL schema %q is not supported; create an empty schema and rerun migrations", schema)
		}
		if _, err := tx.ExecContext(ctx, `CREATE TABLE `+migrationHistoryTable+` (
			version BIGINT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			checksum TEXT NOT NULL,
			applied_at BIGINT NOT NULL
		)`); err != nil {
			return fmt.Errorf("create PostgreSQL migration history: %w", err)
		}
	}
	if err := validateAppliedMigrations(migrations, applied); err != nil {
		return err
	}

	pending := migrations[len(applied):]
	now := time.Now().UTC().Unix()
	for _, item := range pending {
		if _, err := tx.ExecContext(ctx, item.Statement); err != nil {
			return fmt.Errorf("apply PostgreSQL migration %s: %w", item.Name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+migrationHistoryTable+`(version, name, checksum, applied_at) VALUES($1, $2, $3, $4)`,
			item.Version, item.Name, item.Checksum, now); err != nil {
			return fmt.Errorf("record PostgreSQL migration %s: %w", item.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit PostgreSQL migrations: %w", err)
	}
	committed = true
	for _, item := range pending {
		slog.Info("PostgreSQL migration applied", "version", item.Version, "name", item.Name)
	}
	return nil
}

func currentSchema(ctx context.Context, tx *sql.Tx) (string, error) {
	var schema string
	if err := tx.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return "", fmt.Errorf("read PostgreSQL current schema: %w", err)
	}
	if strings.TrimSpace(schema) == "" {
		return "", fmt.Errorf("PostgreSQL search_path has no current schema")
	}
	return schema, nil
}

type migrationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadAppliedMigrations(ctx context.Context, queryer migrationQueryer) ([]appliedMigration, bool, error) {
	var exists bool
	if err := queryer.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = $1
	)`, migrationHistoryTable).Scan(&exists); err != nil {
		return nil, false, fmt.Errorf("inspect PostgreSQL migration history: %w", err)
	}
	if !exists {
		return nil, false, nil
	}
	rows, err := queryer.QueryContext(ctx, `SELECT version, name, checksum FROM `+migrationHistoryTable+` ORDER BY version ASC`) //nolint:rowserrcheck // migrations.ScanApplied checks rows.Err before returning.
	if err != nil {
		return nil, true, fmt.Errorf("load applied PostgreSQL migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items, err := migrations.ScanApplied(rows, "PostgreSQL")
	if err != nil {
		return nil, true, err
	}
	return items, true, nil
}

func schemaHasApplicationTables(ctx context.Context, tx *sql.Tx) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = current_schema()
		  AND table_type = 'BASE TABLE'
		  AND table_name <> $1`, migrationHistoryTable).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect PostgreSQL schema before migration: %w", err)
	}
	return count != 0, nil
}

func loadMigrations() ([]migration, error) {
	items, err := migrations.Load(embeddedMigrations, "PostgreSQL")
	if err != nil {
		return nil, err
	}
	if items[0].Version != baselineMigrationVersion {
		return nil, fmt.Errorf("first PostgreSQL migration must be version %06d", baselineMigrationVersion)
	}
	return items, nil
}

func validateAppliedMigrations(available []migration, applied []appliedMigration) error {
	return migrations.ValidateApplied("PostgreSQL", available, applied)
}

func advisoryKey(value string) int64 {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(value))
	return int64(hasher.Sum64())
}
