package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	baselineMigrationVersion int64 = 1
	// sqliteBaselineVersion is the released SQLite migration the PostgreSQL
	// baseline mirrors. Later PostgreSQL migrations reuse the SQLite version
	// numbers so both chains stay aligned.
	sqliteBaselineVersion int64 = 20

	migrationHistoryTable = "schema_migrations"
)

var migrationFilenamePattern = regexp.MustCompile(`^([0-9]{6})_([a-z0-9]+(?:_[a-z0-9]+)*)\.sql$`)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

type migration struct {
	version   int64
	name      string
	statement string
	checksum  string
}

type appliedMigration struct {
	version  int64
	name     string
	checksum string
}

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
	return migrations[len(migrations)-1].version, nil
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
			statuses[index] = MigrationStatus{Version: item.version, Name: item.name}
		}
		return statuses, nil
	}
	if err := validateAppliedMigrations(migrations, applied); err != nil {
		return nil, err
	}
	statuses := make([]MigrationStatus, len(migrations))
	for index, item := range migrations {
		statuses[index] = MigrationStatus{
			Version: item.version,
			Name:    item.name,
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

	applied, exists, err := loadAppliedMigrationsTx(ctx, tx)
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
		if _, err := tx.ExecContext(ctx, item.statement); err != nil {
			return fmt.Errorf("apply PostgreSQL migration %s: %w", item.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+migrationHistoryTable+`(version, name, checksum, applied_at) VALUES($1, $2, $3, $4)`,
			item.version, item.name, item.checksum, now); err != nil {
			return fmt.Errorf("record PostgreSQL migration %s: %w", item.name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit PostgreSQL migrations: %w", err)
	}
	committed = true
	for _, item := range pending {
		slog.Info("PostgreSQL migration applied", "version", item.version, "name", item.name)
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

func loadAppliedMigrations(ctx context.Context, db *sql.DB) ([]appliedMigration, bool, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = $1
	)`, migrationHistoryTable).Scan(&exists); err != nil {
		return nil, false, fmt.Errorf("inspect PostgreSQL migration history: %w", err)
	}
	if !exists {
		return nil, false, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT version, name, checksum FROM `+migrationHistoryTable+` ORDER BY version ASC`)
	if err != nil {
		return nil, true, fmt.Errorf("load applied PostgreSQL migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items, err := scanAppliedMigrations(rows)
	if err != nil {
		return nil, true, err
	}
	return items, true, nil
}

func loadAppliedMigrationsTx(ctx context.Context, tx *sql.Tx) ([]appliedMigration, bool, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = $1
	)`, migrationHistoryTable).Scan(&exists); err != nil {
		return nil, false, fmt.Errorf("inspect PostgreSQL migration history: %w", err)
	}
	if !exists {
		return nil, false, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT version, name, checksum FROM `+migrationHistoryTable+` ORDER BY version ASC`)
	if err != nil {
		return nil, true, fmt.Errorf("load applied PostgreSQL migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items, err := scanAppliedMigrations(rows)
	if err != nil {
		return nil, true, err
	}
	return items, true, nil
}

func scanAppliedMigrations(rows *sql.Rows) ([]appliedMigration, error) {
	var items []appliedMigration
	for rows.Next() {
		var item appliedMigration
		if err := rows.Scan(&item.version, &item.name, &item.checksum); err != nil {
			return nil, fmt.Errorf("scan applied PostgreSQL migration: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied PostgreSQL migrations: %w", err)
	}
	return items, nil
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
	entries, err := fs.ReadDir(embeddedMigrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded PostgreSQL migrations: %w", err)
	}
	items := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		match := migrationFilenamePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("invalid PostgreSQL migration filename %q", entry.Name())
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid PostgreSQL migration version in %q", entry.Name())
		}
		data, err := fs.ReadFile(embeddedMigrations, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read PostgreSQL migration %q: %w", entry.Name(), err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return nil, fmt.Errorf("PostgreSQL migration %q is empty", entry.Name())
		}
		sum := sha256.Sum256(data)
		items = append(items, migration{
			version:   version,
			name:      strings.TrimSuffix(entry.Name(), ".sql"),
			statement: string(data),
			checksum:  hex.EncodeToString(sum[:]),
		})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no embedded PostgreSQL migrations found")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].version < items[j].version })
	if items[0].version != baselineMigrationVersion {
		return nil, fmt.Errorf("first PostgreSQL migration must be version %06d", baselineMigrationVersion)
	}
	for index := 1; index < len(items); index++ {
		if items[index-1].version == items[index].version {
			return nil, fmt.Errorf("duplicate PostgreSQL migration version %06d", items[index].version)
		}
	}
	return items, nil
}

func validateAppliedMigrations(available []migration, applied []appliedMigration) error {
	if len(applied) > len(available) {
		return fmt.Errorf("PostgreSQL schema is newer than this binary: database has %d migrations, binary has %d", len(applied), len(available))
	}
	for index, actual := range applied {
		expected := available[index]
		if actual.version != expected.version {
			return fmt.Errorf("PostgreSQL migration history is not an embedded prefix: database version %06d, expected %06d", actual.version, expected.version)
		}
		if actual.name != expected.name {
			return fmt.Errorf("PostgreSQL migration %06d name mismatch: database %q, binary %q", actual.version, actual.name, expected.name)
		}
		if actual.checksum != expected.checksum {
			return fmt.Errorf("PostgreSQL migration %s checksum mismatch", actual.name)
		}
	}
	return nil
}

func advisoryKey(value string) int64 {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(value))
	return int64(hasher.Sum64())
}
