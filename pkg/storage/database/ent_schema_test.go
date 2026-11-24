package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/schema/field"

	"github.com/chaitin/agent-compose/ent/dao/migrate"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

// TestSchemaMatchesReleasedDatabase is the contract that lets the storage layer
// treat ent/schema (through the generated DAO) as the model of record: every
// generated ent table must map to a table produced by the released migrations,
// every generated column to one of its columns with a compatible type, and the
// Optional marker to the column's database default (the runtime never applies
// ent's own DDL).
//
// It reads the generated migrate schema instead of loading ent/schema through
// entc: the generated package is what the stores query, and it keeps the test
// runnable on every supported Go toolchain. `task check:ent` is what guarantees
// ent/schema and ent/dao agree.
func TestSchemaMatchesReleasedDatabase(t *testing.T) {
	db, err := storagesqlite.Open(filepath.Join(t.TempDir(), "schema.db"), 5*time.Second)
	if err != nil {
		t.Fatalf("open migrated SQLite database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	columns, err := sqliteColumns(ctx, db.DB())
	if err != nil {
		t.Fatalf("read SQLite schema: %v", err)
	}

	entTables := make(map[string]struct{}, len(migrate.Tables))
	for _, table := range migrate.Tables {
		entTables[table.Name] = struct{}{}
		tableColumns, ok := columns[table.Name]
		if !ok {
			t.Errorf("ent table %q is not created by the released migrations", table.Name)
			continue
		}
		byName := make(map[string]columnInfo, len(tableColumns))
		for _, column := range tableColumns {
			byName[column.name] = column
		}
		var idColumn string
		if len(table.PrimaryKey) > 0 {
			idColumn = table.PrimaryKey[0].Name
		}
		for _, column := range table.Columns {
			released, ok := byName[column.Name]
			if !ok {
				t.Errorf("ent table %s column %s is missing from the released migrations", table.Name, column.Name)
				continue
			}
			wantType := sqliteTypeForEnt(column.Type)
			if wantType != "" && !strings.EqualFold(released.typ, wantType) {
				t.Errorf("ent table %s column %s is %s, want %s", table.Name, column.Name, released.typ, wantType)
			}
			if column.Name == idColumn {
				// project_run_label is the one released table without any
				// primary key; ent still needs one ID field, so run_id is the
				// query identity and every statement also constrains key.
				if !released.pk && hasPrimaryKey(tableColumns) {
					t.Errorf("ent table %s ID column %s is not a primary key in the released schema", table.Name, column.Name)
				}
				continue
			}
			// A column is Optional (nullable for ent) when the database supplies
			// a default or the column is nullable; ent would otherwise require
			// the field on every Create even though the released INSERT omitted
			// it.
			wantOptional := released.defaultValue != "" || !released.notNull
			if column.Nullable != wantOptional {
				t.Errorf("ent table %s column %s: Nullable=%v, but the released column has default %q / notNull=%v", table.Name, column.Name, column.Nullable, released.defaultValue, released.notNull)
			}
		}
		for _, released := range tableColumns {
			found := false
			for _, column := range table.Columns {
				if column.Name == released.name {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("released table %s column %s has no ent column on the generated model", table.Name, released.name)
			}
		}
	}

	var missing []string
	for table := range columns {
		if _, ok := entTables[table]; !ok {
			missing = append(missing, table)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("released tables without an ent table: %v", missing)
	}
}

type columnInfo struct {
	name         string
	typ          string
	notNull      bool
	defaultValue string
	pk           bool
}

func sqliteColumns(ctx context.Context, db *sql.DB) (map[string][]columnInfo, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make(map[string][]columnInfo)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tableColumns, err := sqliteTableColumns(ctx, db, table)
		if err != nil {
			return nil, err
		}
		result[table] = tableColumns
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func sqliteTableColumns(ctx context.Context, db *sql.DB, table string) ([]columnInfo, error) {
	info, err := db.QueryContext(ctx, `PRAGMA table_info("`+strings.ReplaceAll(table, `"`, `""`)+`")`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = info.Close() }()
	var tableColumns []columnInfo
	for info.Next() {
		var (
			cid        int
			name       string
			typ        string
			notNull    int
			defaultV   any
			primaryKey int
		)
		if err := info.Scan(&cid, &name, &typ, &notNull, &defaultV, &primaryKey); err != nil {
			return nil, err
		}
		defaultText := ""
		if defaultV != nil {
			defaultText = strings.TrimSpace(toString(defaultV))
		}
		tableColumns = append(tableColumns, columnInfo{
			name:         name,
			typ:          typ,
			notNull:      notNull != 0,
			defaultValue: defaultText,
			pk:           primaryKey > 0,
		})
	}
	if err := info.Err(); err != nil {
		return nil, err
	}
	return tableColumns, nil
}

func hasPrimaryKey(columns []columnInfo) bool {
	for _, column := range columns {
		if column.pk {
			return true
		}
	}
	return false
}

func sqliteTypeForEnt(typ field.Type) string {
	switch typ {
	case field.TypeString:
		return "TEXT"
	case field.TypeInt, field.TypeInt64:
		return "INTEGER"
	case field.TypeBool:
		return "INTEGER"
	default:
		return ""
	}
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}
