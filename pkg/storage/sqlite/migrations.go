package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const baselineMigrationVersion int64 = 1

const legacyMigratorHint = "this data root requires migration; see the agent-compose documentation for the required procedure"

var migrationFilenamePattern = regexp.MustCompile(`^([0-9]{6})_([a-z0-9]+(?:_[a-z0-9]+)*)\.sql$`)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

type migration struct {
	version   int64
	name      string
	statement string
	checksum  string
}

// EmbeddedMigration is a read-only view of an immutable migration asset.
// Copy-migration tools use it to adopt an unversioned historical schema
// without duplicating the released SQL files.
type EmbeddedMigration struct {
	Version   int64
	Name      string
	Statement string
	Checksum  string
}

type appliedMigration struct {
	version  int64
	name     string
	checksum string
}

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
	end := sort.Search(len(migrations), func(index int) bool { return migrations[index].version > maxVersion })
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
		if item.version == version {
			return EmbeddedMigration{Version: item.version, Name: item.name, Statement: item.statement, Checksum: item.checksum}, true, nil
		}
	}
	return EmbeddedMigration{}, false, nil
}

func applyMigrations(ctx context.Context, db *sql.DB, migrationFS fs.FS) error {
	migrations, err := loadMigrations(migrationFS)
	if err != nil {
		return err
	}
	if migrations[0].version != baselineMigrationVersion {
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
			return fmt.Errorf("prepare SQLite migration %s: %w", item.name, err)
		}
		if _, err := conn.ExecContext(ctx, item.statement); err != nil {
			return fmt.Errorf("apply SQLite migration %s: %w", item.name, err)
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, checksum) VALUES(?, ?, ?)`,
			item.version, item.name, item.checksum); err != nil {
			return fmt.Errorf("record SQLite migration %s: %w", item.name, err)
		}
	}

	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit SQLite migrations: %w", err)
	}
	committed = true
	for _, item := range pending {
		slog.Info("SQLite migration applied", "version", item.version, "name", item.name)
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
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded SQLite migrations: %w", err)
	}

	items := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		match := migrationFilenamePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("invalid SQLite migration filename %q", entry.Name())
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid SQLite migration version in %q", entry.Name())
		}
		data, err := fs.ReadFile(migrationFS, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read SQLite migration %q: %w", entry.Name(), err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return nil, fmt.Errorf("SQLite migration %q is empty", entry.Name())
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
		return nil, fmt.Errorf("no embedded SQLite migrations found")
	}

	sort.Slice(items, func(i, j int) bool { return items[i].version < items[j].version })
	for index := 1; index < len(items); index++ {
		if items[index-1].version == items[index].version {
			return nil, fmt.Errorf("duplicate SQLite migration version %06d", items[index].version)
		}
	}
	return items, nil
}

func loadAppliedMigrations(ctx context.Context, conn migrationConn) ([]appliedMigration, error) {
	rows, err := conn.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		return nil, fmt.Errorf("load applied SQLite migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []appliedMigration
	for rows.Next() {
		var item appliedMigration
		if err := rows.Scan(&item.version, &item.name, &item.checksum); err != nil {
			return nil, fmt.Errorf("scan applied SQLite migration: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied SQLite migrations: %w", err)
	}
	return items, nil
}

func validateAppliedMigrations(available []migration, applied []appliedMigration) error {
	if len(applied) > len(available) {
		return fmt.Errorf("SQLite schema is newer than this binary: database has %d migrations, binary has %d", len(applied), len(available))
	}
	// An exact prefix check makes released files immutable: removing, reordering,
	// renaming, or editing any applied migration fails before schema work starts.
	for index, actual := range applied {
		expected := available[index]
		if actual.version != expected.version {
			return fmt.Errorf("SQLite migration history is not an embedded prefix: database version %06d, expected %06d", actual.version, expected.version)
		}
		if actual.name != expected.name {
			return fmt.Errorf("SQLite migration %06d name mismatch: database %q, binary %q", actual.version, actual.name, expected.name)
		}
		if actual.checksum != expected.checksum {
			return fmt.Errorf("SQLite migration %s checksum mismatch", actual.name)
		}
	}
	return nil
}
