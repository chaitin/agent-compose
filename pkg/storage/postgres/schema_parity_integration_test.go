package postgres

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

// TestPostgresSchemaParityIntegration guards against drift between the SQLite
// final schema and the PostgreSQL baseline: every application table and column
// must exist on both sides with the mapped type, and explicit index names must
// match. Defaults and nullability are intentionally not compared because the
// baseline translates them into PostgreSQL idioms.
func TestPostgresSchemaParityIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(testPostgresDSNEnv))
	if dsn == "" {
		t.Skipf("set %s to run PostgreSQL integration tests", testPostgresDSNEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sqliteDB, err := storagesqlite.Open(filepath.Join(t.TempDir(), "parity.db"), 5*time.Second)
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	t.Cleanup(func() { _ = sqliteDB.Close() })

	schema, _ := newTestSchema(t, ctx, dsn)
	target, err := Open(ctx, Config{URL: withSearchPath(dsn, schema), MaxOpenConns: 4, MaxIdleConns: 2, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("open PostgreSQL database: %v", err)
	}
	t.Cleanup(func() { _ = target.Close() })

	sqliteTables, err := sqliteSchema(ctx, sqliteDB.DB())
	if err != nil {
		t.Fatalf("inspect SQLite schema: %v", err)
	}
	postgresTables, err := postgresSchema(ctx, target.DB())
	if err != nil {
		t.Fatalf("inspect PostgreSQL schema: %v", err)
	}
	if strings.Join(tableNames(sqliteTables), ",") != strings.Join(tableNames(postgresTables), ",") {
		t.Fatalf("table sets differ:\nsqlite:   %v\npostgres: %v", tableNames(sqliteTables), tableNames(postgresTables))
	}
	for _, table := range tableNames(sqliteTables) {
		sqliteColumns := sqliteTables[table]
		postgresColumns := postgresTables[table]
		if len(sqliteColumns) != len(postgresColumns) {
			t.Fatalf("table %s column count = %d in SQLite, %d in PostgreSQL", table, len(sqliteColumns), len(postgresColumns))
		}
		for index := range sqliteColumns {
			if sqliteColumns[index].name != postgresColumns[index].name {
				t.Fatalf("table %s column %d name = %q in SQLite, %q in PostgreSQL", table, index, sqliteColumns[index].name, postgresColumns[index].name)
			}
			wantType := mapSQLiteType(sqliteColumns[index].typ)
			if wantType != postgresColumns[index].typ {
				t.Fatalf("table %s column %s type = %q in SQLite (%q), want PostgreSQL %q", table, sqliteColumns[index].name, postgresColumns[index].typ, sqliteColumns[index].typ, wantType)
			}
		}
	}

	sqliteIndexes, err := sqliteIndexNames(ctx, sqliteDB.DB())
	if err != nil {
		t.Fatalf("inspect SQLite indexes: %v", err)
	}
	postgresIndexes, err := postgresIndexNames(ctx, target.DB())
	if err != nil {
		t.Fatalf("inspect PostgreSQL indexes: %v", err)
	}
	if strings.Join(sqliteIndexes, ",") != strings.Join(postgresIndexes, ",") {
		t.Fatalf("explicit index sets differ:\nsqlite:   %v\npostgres: %v", sqliteIndexes, postgresIndexes)
	}
	sqliteVersion, err := storagesqlite.LatestMigrationVersion()
	if err != nil {
		t.Fatalf("read SQLite latest migration: %v", err)
	}
	if sqliteVersion < SQLiteBaselineVersion() {
		t.Fatalf("SQLite latest migration %d is older than the PostgreSQL baseline %d", sqliteVersion, SQLiteBaselineVersion())
	}
}

type schemaColumn struct {
	name string
	typ  string
}

func sqliteSchema(ctx context.Context, db *sql.DB) (map[string][]schemaColumn, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	result := make(map[string][]schemaColumn, len(tables))
	for _, table := range tables {
		columns, err := sqliteTableColumns(ctx, db, table)
		if err != nil {
			return nil, err
		}
		result[table] = columns
	}
	return result, rows.Err()
}

func sqliteTableColumns(ctx context.Context, db *sql.DB, table string) (columns []schemaColumn, err error) {
	info, err := db.QueryContext(ctx, `PRAGMA table_info(`+quoteTestIdentifier(table)+`)`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := info.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	for info.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if scanErr := info.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); scanErr != nil {
			return nil, scanErr
		}
		columns = append(columns, schemaColumn{name: name, typ: typ})
	}
	if rowsErr := info.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return columns, nil
}

func postgresSchema(ctx context.Context, db *sql.DB) (map[string][]schemaColumn, error) {
	rows, err := db.QueryContext(ctx, `SELECT table_name, column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name <> 'schema_migrations'
		ORDER BY table_name, ordinal_position`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make(map[string][]schemaColumn)
	for rows.Next() {
		var table, column, dataType string
		if err := rows.Scan(&table, &column, &dataType); err != nil {
			return nil, err
		}
		result[table] = append(result[table], schemaColumn{name: column, typ: dataType})
	}
	return result, rows.Err()
}

func sqliteIndexNames(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL AND name LIKE 'idx_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanStringRows(rows)
}

func postgresIndexNames(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname LIKE 'idx_%'
		ORDER BY indexname`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanStringRows(rows)
}

func scanStringRows(rows *sql.Rows) ([]string, error) {
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func mapSQLiteType(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "INTEGER":
		return "bigint"
	case "TEXT":
		return "text"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func tableNames(tables map[string][]schemaColumn) []string {
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func quoteTestIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
