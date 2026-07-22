package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	storagesqlite "agent-compose/pkg/storage/sqlite"
)

func (s *ConfigStore) initSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("config store is required")
	}
	return storagesqlite.Migrate(ctx, s.db)
}

func (s *ConfigStore) InitSchema(ctx context.Context) error {
	return s.initSchema(ctx)
}

func sqliteTableColumnTypes(ctx context.Context, db *sql.DB, table string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT name, type FROM pragma_table_info('%s')`, strings.ReplaceAll(table, "'", "''")))
	if err != nil {
		return nil, fmt.Errorf("query SQLite schema for %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	columns := make(map[string]string)
	for rows.Next() {
		var name string
		var columnType string
		if err := rows.Scan(&name, &columnType); err != nil {
			return nil, fmt.Errorf("scan SQLite schema for %s: %w", table, err)
		}
		columns[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(columnType)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate SQLite schema for %s: %w", table, err)
	}
	return columns, nil
}

func isIntegerColumnType(columnType string) bool {
	return strings.Contains(strings.ToUpper(strings.TrimSpace(columnType)), "INT")
}
