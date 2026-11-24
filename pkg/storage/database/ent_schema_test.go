package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
	"entgo.io/ent/schema/field"

	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

// TestSchemaMatchesReleasedDatabase is the contract that lets the storage layer
// treat ent/schema as the model of record: every ent entity must map to a table
// produced by the released migrations, every ent field to one of its columns
// with a compatible type, and the Optional marker to the column's database
// default (the runtime never applies ent's own DDL).
func TestSchemaMatchesReleasedDatabase(t *testing.T) {
	graph, err := entc.LoadGraph("../../../ent/schema", &gen.Config{
		Package: "github.com/chaitin/agent-compose/ent/dao",
	})
	if err != nil {
		t.Fatalf("load ent graph: %v", err)
	}

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

	entTables := make(map[string]struct{}, len(graph.Nodes))
	for _, node := range graph.Nodes {
		table := node.Table()
		entTables[table] = struct{}{}
		tableColumns, ok := columns[table]
		if !ok {
			t.Errorf("ent entity %s maps to table %q, which the released migrations do not create", node.Name, table)
			continue
		}
		byName := make(map[string]columnInfo, len(tableColumns))
		for _, column := range tableColumns {
			byName[column.name] = column
		}
		entFields := append([]*gen.Field{}, node.Fields...)
		if node.ID != nil {
			entFields = append(entFields, node.ID)
		}
		for _, f := range entFields {
			storageKey := f.StorageKey()
			column, ok := byName[storageKey]
			if !ok {
				t.Errorf("entity %s field %q maps to missing column %s.%s", node.Name, f.Name, table, storageKey)
				continue
			}
			wantType := sqliteTypeForEnt(f.Type.Type)
			if wantType != "" && !strings.EqualFold(column.typ, wantType) {
				t.Errorf("entity %s field %q: column %s.%s is %s, want %s", node.Name, f.Name, table, storageKey, column.typ, wantType)
			}
			isID := node.ID != nil && f.Name == node.ID.Name
			if isID {
				// project_run_label is the one released table without any
				// primary key; ent still needs one ID field, so run_id is the
				// query identity and every statement also constrains key.
				if !column.pk && hasPrimaryKey(tableColumns) {
					t.Errorf("entity %s ID field %q maps to non-primary-key column %s.%s", node.Name, f.Name, table, storageKey)
				}
				continue
			}
			// A field is Optional when the database supplies a default (or the
			// column is nullable); ent would otherwise require the field on
			// every Create even though the released INSERT omitted it.
			wantOptional := column.defaultValue != "" || !column.notNull
			if f.Optional != wantOptional {
				t.Errorf("entity %s field %q: Optional=%v, but column %s.%s has default %q / notNull=%v", node.Name, f.Name, f.Optional, table, storageKey, column.defaultValue, column.notNull)
			}
		}
		for _, column := range tableColumns {
			found := false
			for _, f := range entFields {
				if f.StorageKey() == column.name {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("table %s column %s has no ent field on %s", table, column.name, node.Name)
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
		t.Errorf("released tables without an ent entity: %v", missing)
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
		info, err := db.QueryContext(ctx, `PRAGMA table_info("`+strings.ReplaceAll(table, `"`, `""`)+`")`)
		if err != nil {
			return nil, err
		}
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
				_ = info.Close()
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
		if err := info.Close(); err != nil {
			return nil, err
		}
		if err := info.Err(); err != nil {
			return nil, err
		}
		result[table] = tableColumns
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
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
