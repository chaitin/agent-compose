package config

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const StoredUnixMillisecondThreshold int64 = 10_000_000_000

type EnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

func NormalizeEnvItems(items []EnvVar) []EnvVar {
	if len(items) == 0 {
		return nil
	}
	merged := make(map[string]EnvVar, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		item.Name = name
		merged[name] = item
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]EnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}

func ParseStoredUnixTimeAuto(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	if value >= StoredUnixMillisecondThreshold {
		return time.UnixMilli(value).UTC()
	}
	return time.Unix(value, 0).UTC()
}

func ParseStoredTime(value any) time.Time {
	switch typed := value.(type) {
	case nil:
		return time.Time{}
	case int64:
		return ParseStoredUnixTimeAuto(typed)
	case int:
		return ParseStoredUnixTimeAuto(int64(typed))
	case float64:
		return ParseStoredUnixTimeAuto(int64(typed))
	case []byte:
		return ParseStoredTime(string(typed))
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return time.Time{}
		}
		if unixValue, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return ParseStoredUnixTimeAuto(unixValue)
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
			if parsed, err := time.Parse(layout, trimmed); err == nil {
				return parsed.UTC()
			}
		}
	}
	return time.Time{}
}

func ParseStoredLoaderTriggerTime(value any) time.Time {
	switch typed := value.(type) {
	case nil:
		return time.Time{}
	case int64:
		return ParseStoredUnixTimeAuto(typed)
	case int:
		return ParseStoredUnixTimeAuto(int64(typed))
	case float64:
		return ParseStoredUnixTimeAuto(int64(typed))
	case []byte:
		return ParseStoredLoaderTriggerTime(string(typed))
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return time.Time{}
		}
		if unixValue, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return ParseStoredUnixTimeAuto(unixValue)
		}
		return ParseStoredTime(trimmed)
	default:
		return ParseStoredTime(value)
	}
}

func NormalizeSQLiteTimestampExpr(columnName string) string {
	return fmt.Sprintf(`CASE
		WHEN trim(COALESCE(%[1]s, '')) = '' THEN CAST(strftime('%%s','now') AS INTEGER)
		WHEN trim(COALESCE(%[1]s, '')) NOT GLOB '*[^0-9]*' THEN CAST(%[1]s AS INTEGER)
		ELSE COALESCE(CAST(strftime('%%s', %[1]s) AS INTEGER), CAST(strftime('%%s','now') AS INTEGER))
	END`, columnName)
}

func BoolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TableColumnTypes(ctx context.Context, db *sql.DB, tableName string) (map[string]string, error) {
	trimmedTableName := strings.TrimSpace(tableName)
	if trimmedTableName == "" {
		return nil, fmt.Errorf("schema table name is required")
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT name, type FROM pragma_table_info('%s')`, strings.ReplaceAll(trimmedTableName, "'", "''")))
	if err != nil {
		return nil, fmt.Errorf("query schema for %s: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()

	columnTypes := make(map[string]string)
	for rows.Next() {
		var name string
		var columnType string
		if err := rows.Scan(&name, &columnType); err != nil {
			return nil, fmt.Errorf("scan schema for %s: %w", tableName, err)
		}
		columnTypes[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(columnType)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema for %s: %w", tableName, err)
	}
	return columnTypes, nil
}

func EnsureColumn(ctx context.Context, db *sql.DB, table, column, definition string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition)
	return err
}
