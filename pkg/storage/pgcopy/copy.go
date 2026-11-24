// Package pgcopy copies an offline SQLite application database into an empty,
// already-migrated PostgreSQL database. It never writes the source.
package pgcopy

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/chaitin/agent-compose/pkg/storage/postgres"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

const defaultBatchSize = 200

// Options controls an offline copy. BatchSize defaults to 200.
type Options struct {
	BatchSize int
}

// TableResult reports the copied row count for one table.
type TableResult struct {
	Table string
	Rows  int64
}

// Result summarizes a completed copy.
type Result struct {
	Tables        []TableResult
	EventSequence int64
}

// Copy copies sourcePath into target. The source is opened read-only and must
// be at the latest embedded SQLite migration. target must be empty and fully
// migrated; Copy refuses to modify a target with rows.
func Copy(ctx context.Context, sourcePath string, target *sql.DB, options Options) (Result, error) {
	if strings.TrimSpace(sourcePath) == "" {
		return Result{}, fmt.Errorf("SQLite source path is required")
	}
	if target == nil {
		return Result{}, fmt.Errorf("PostgreSQL target database is required")
	}
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}

	source, err := openSource(ctx, sourcePath)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = source.Close() }()
	// SQLite AUTOINCREMENT keeps its high-water mark in sqlite_sequence even
	// after rows are deleted. MAX(sequence) alone would reissue identities that
	// clients have already observed.
	sourceSequence, err := sourceEventSequence(ctx, source)
	if err != nil {
		return Result{}, err
	}

	if err := verifyTargetMigrated(ctx, target); err != nil {
		return Result{}, err
	}
	if err := verifyTargetEmpty(ctx, target); err != nil {
		return Result{}, err
	}
	if err := verifySourceTables(ctx, source); err != nil {
		return Result{}, err
	}

	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("begin PostgreSQL copy transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	result := Result{Tables: make([]TableResult, 0, len(copyTables))}
	for _, table := range copyTables {
		rows, err := copyTable(ctx, source, tx, table, batchSize)
		if err != nil {
			return Result{}, err
		}
		result.Tables = append(result.Tables, TableResult{Table: table, Rows: rows})
	}
	eventSequence, err := resetEventSequence(ctx, tx, sourceSequence)
	if err != nil {
		return Result{}, err
	}
	result.EventSequence = eventSequence

	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("commit PostgreSQL copy: %w", err)
	}
	committed = true
	return result, nil
}

func openSource(ctx context.Context, path string) (*sql.DB, error) {
	uri := &url.URL{Scheme: "file", Path: path}
	query := uri.Query()
	query.Set("mode", "ro")
	query.Set("_pragma", "busy_timeout(5000)")
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, fmt.Errorf("open SQLite source: %w", err)
	}
	db.SetMaxOpenConns(1)
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("check SQLite source integrity: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(integrity), "ok") {
		_ = db.Close()
		return nil, fmt.Errorf("SQLite source integrity check failed: %s", integrity)
	}
	version, err := storagesqlite.AppliedMigrationVersion(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	latest, err := storagesqlite.LatestMigrationVersion()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if version != latest {
		_ = db.Close()
		return nil, fmt.Errorf("SQLite source schema version is %d, expected %d; start the daemon once with the current binary to upgrade it before copying", version, latest)
	}
	return db, nil
}

func verifyTargetMigrated(ctx context.Context, target *sql.DB) error {
	statuses, err := postgres.Inspect(ctx, target)
	if err != nil {
		return err
	}
	for _, status := range statuses {
		if !status.Applied {
			return fmt.Errorf("PostgreSQL target is not fully migrated (pending %06d_%s); run `agent-compose db migrate` first", status.Version, status.Name)
		}
	}
	return nil
}

func verifyTargetEmpty(ctx context.Context, target *sql.DB) error {
	for _, table := range copyTables {
		var count int64
		if err := target.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(table)).Scan(&count); err != nil {
			return fmt.Errorf("count PostgreSQL table %s: %w", table, err)
		}
		if count != 0 {
			return fmt.Errorf("PostgreSQL target table %s is not empty (%d rows); use an empty database", table, count)
		}
	}
	return nil
}

func verifySourceTables(ctx context.Context, source *sql.DB) error {
	rows, err := source.QueryContext(ctx, `SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'
		ORDER BY name`)
	if err != nil {
		return fmt.Errorf("list SQLite tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan SQLite table: %w", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate SQLite tables: %w", err)
	}
	expected := append([]string(nil), copyTables...)
	sort.Strings(expected)
	if strings.Join(tables, "\x00") != strings.Join(expected, "\x00") {
		return fmt.Errorf("SQLite source tables do not match this binary: got %v, expected %v", tables, expected)
	}
	return nil
}

func copyTable(ctx context.Context, source *sql.DB, tx *sql.Tx, table string, batchSize int) (int64, error) {
	sourceColumns, err := sourceColumns(ctx, source, table)
	if err != nil {
		return 0, err
	}
	targetColumns, err := targetColumns(ctx, tx, table)
	if err != nil {
		return 0, err
	}
	if strings.Join(sourceColumns, "\x00") != strings.Join(targetColumns, "\x00") {
		return 0, fmt.Errorf("table %s columns differ: source %v, target %v", table, sourceColumns, targetColumns)
	}

	selectSQL := `SELECT ` + quoteIdentifiers(sourceColumns) + ` FROM ` + quoteIdentifier(table)
	rows, err := source.QueryContext(ctx, selectSQL)
	if err != nil {
		return 0, fmt.Errorf("read SQLite table %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var copied int64
	batch := make([][]any, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := insertBatch(ctx, tx, table, sourceColumns, batch); err != nil {
			return err
		}
		copied += int64(len(batch))
		batch = batch[:0]
		return nil
	}

	for rows.Next() {
		values := make([]any, len(sourceColumns))
		ptrs := make([]any, len(sourceColumns))
		for index := range values {
			ptrs[index] = &values[index]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return 0, fmt.Errorf("scan SQLite row from %s: %w", table, err)
		}
		batch = append(batch, values)
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return 0, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate SQLite table %s: %w", table, err)
	}
	if err := flush(); err != nil {
		return 0, err
	}
	var targetCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(table)).Scan(&targetCount); err != nil {
		return 0, fmt.Errorf("count copied table %s: %w", table, err)
	}
	if targetCount != copied {
		return 0, fmt.Errorf("copied table %s has %d rows, want %d", table, targetCount, copied)
	}
	return copied, nil
}

func sourceColumns(ctx context.Context, source *sql.DB, table string) ([]string, error) {
	rows, err := source.QueryContext(ctx, `PRAGMA table_info(`+quoteIdentifier(table)+`)`)
	if err != nil {
		return nil, fmt.Errorf("inspect SQLite table %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	var columns []string
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("scan SQLite column for %s: %w", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate SQLite columns for %s: %w", table, err)
	}
	return columns, nil
}

func targetColumns(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1
		ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("inspect PostgreSQL table %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL column for %s: %w", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate PostgreSQL columns for %s: %w", table, err)
	}
	return columns, nil
}

func insertBatch(ctx context.Context, tx *sql.Tx, table string, columns []string, batch [][]any) error {
	var builder strings.Builder
	builder.WriteString(`INSERT INTO `)
	builder.WriteString(quoteIdentifier(table))
	builder.WriteString(` (`)
	builder.WriteString(quoteIdentifiers(columns))
	builder.WriteString(`) VALUES `)
	args := make([]any, 0, len(batch)*len(columns))
	for rowIndex, row := range batch {
		if rowIndex > 0 {
			builder.WriteByte(',')
		}
		builder.WriteByte('(')
		for columnIndex := range columns {
			if columnIndex > 0 {
				builder.WriteByte(',')
			}
			builder.WriteByte('$')
			builder.WriteString(strconv.Itoa(rowIndex*len(columns) + columnIndex + 1))
			args = append(args, row[columnIndex])
		}
		builder.WriteByte(')')
	}
	if _, err := tx.ExecContext(ctx, builder.String(), args...); err != nil {
		return fmt.Errorf("insert %s rows into PostgreSQL: %w", table, err)
	}
	return nil
}

func sourceEventSequence(ctx context.Context, source *sql.DB) (int64, error) {
	var sequence int64
	if err := source.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'event'), 0)`).Scan(&sequence); err != nil {
		return 0, fmt.Errorf("read SQLite event sequence: %w", err)
	}
	return sequence, nil
}

func resetEventSequence(ctx context.Context, tx *sql.Tx, sourceSequence int64) (int64, error) {
	var maxSequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) FROM event`).Scan(&maxSequence); err != nil {
		return 0, fmt.Errorf("read copied event sequence: %w", err)
	}
	targetSequence := max(maxSequence, sourceSequence)
	var setValue int64
	if targetSequence == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT setval(pg_get_serial_sequence('event', 'sequence'), 1, false)`).Scan(&setValue); err != nil {
			return 0, fmt.Errorf("reset empty event sequence: %w", err)
		}
		return 0, nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT setval(pg_get_serial_sequence('event', 'sequence'), $1, true)`, targetSequence).Scan(&setValue); err != nil {
		return 0, fmt.Errorf("advance event sequence: %w", err)
	}
	return targetSequence, nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteIdentifiers(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = quoteIdentifier(value)
	}
	return strings.Join(quoted, ", ")
}
