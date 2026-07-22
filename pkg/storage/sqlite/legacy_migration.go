package sqlite

import (
	"context"
	"fmt"
	"strings"

	"agent-compose/pkg/storage/storeutil"
)

// prepareLegacySchema upgrades only schema shapes that existed before
// schema_migrations. Complete schema creation remains owned by the baseline SQL.
func prepareLegacySchema(ctx context.Context, conn migrationConn) error {
	if err := renameLegacyColumns(ctx, conn); err != nil {
		return err
	}
	if err := recoverLegacyLoaderBinding(ctx, conn); err != nil {
		return err
	}
	if err := rebuildLegacyTimestampTables(ctx, conn); err != nil {
		return err
	}
	if err := addLegacyColumns(ctx, conn); err != nil {
		return err
	}
	if err := rebuildLegacyLoaderBinding(ctx, conn); err != nil {
		return err
	}
	return nil
}

// finalizeLegacySchema runs after the baseline has created every destination
// table, but before the baseline version is recorded.
func finalizeLegacySchema(ctx context.Context, conn migrationConn) error {
	if err := migrateLegacyLoaderTimestamps(ctx, conn); err != nil {
		return err
	}
	return copyLegacyEventSandboxLinks(ctx, conn)
}

func renameLegacyColumns(ctx context.Context, conn migrationConn) error {
	for _, item := range []struct {
		table   string
		legacy  string
		current string
	}{
		{table: "loader", legacy: "session_policy", current: "sandbox_policy"},
		{table: "loader_binding", legacy: "session_id", current: "sandbox_id"},
		{table: "loader_event", legacy: "linked_session_id", current: "linked_sandbox_id"},
		{table: "loader_event", legacy: "linked_agent_session_id", current: "linked_agent_thread_id"},
		{table: "llm_facade_token", legacy: "session_id", current: "sandbox_id"},
	} {
		columns, err := sqliteTableColumnTypes(ctx, conn, item.table)
		if err != nil {
			return err
		}
		if _, legacyExists := columns[item.legacy]; !legacyExists {
			continue
		}
		if _, currentExists := columns[item.current]; currentExists {
			if _, err := conn.ExecContext(ctx, fmt.Sprintf(
				`UPDATE %q SET %q = %q WHERE %q = ''`, item.table, item.current, item.legacy, item.current,
			)); err != nil {
				return fmt.Errorf("copy legacy SQLite column %s.%s to %s: %w", item.table, item.legacy, item.current, err)
			}
			continue
		}
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(
			`ALTER TABLE %q RENAME COLUMN %q TO %q`, item.table, item.legacy, item.current,
		)); err != nil {
			return fmt.Errorf("rename legacy SQLite column %s.%s to %s: %w", item.table, item.legacy, item.current, err)
		}
	}
	return nil
}

func recoverLegacyLoaderBinding(ctx context.Context, conn migrationConn) error {
	// Historical releases recognized loader_binding_legacy as an interrupted
	// table rebuild. Recover that supported on-disk state before the baseline
	// attempts its idempotent CREATE TABLE.
	legacyColumns, err := sqliteTableColumnTypes(ctx, conn, "loader_binding_legacy")
	if err != nil {
		return err
	}
	if len(legacyColumns) == 0 {
		return nil
	}
	columns, err := sqliteTableColumnTypes(ctx, conn, "loader_binding")
	if err != nil {
		return err
	}
	if len(columns) > 0 {
		if _, ok := columns["trigger_id"]; !ok {
			return fmt.Errorf("recover loader binding trigger migration: loader_binding and loader_binding_legacy both use legacy schema")
		}
	}
	if len(columns) == 0 {
		if _, err := conn.ExecContext(ctx, `CREATE TABLE loader_binding (
			loader_id TEXT NOT NULL,
			trigger_id TEXT NOT NULL DEFAULT '',
			sandbox_id TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			PRIMARY KEY(loader_id, trigger_id)
		)`); err != nil {
			return fmt.Errorf("recover loader binding trigger migration: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO loader_binding(loader_id, trigger_id, sandbox_id, created_at, updated_at)
		SELECT loader_id, '', sandbox_id, created_at, updated_at FROM loader_binding_legacy`); err != nil {
		return fmt.Errorf("recover loader binding trigger migration: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `DROP TABLE loader_binding_legacy`); err != nil {
		return fmt.Errorf("drop recovered loader binding legacy table: %w", err)
	}
	return nil
}

func rebuildLegacyTimestampTables(ctx context.Context, conn migrationConn) error {
	if columns, err := sqliteTableColumnTypes(ctx, conn, "global_env"); err != nil {
		return err
	} else if len(columns) > 0 && !isIntegerColumnType(columns["updated_at"]) {
		if err := rebuildLegacyGlobalEnv(ctx, conn); err != nil {
			return err
		}
	}

	columns, err := sqliteTableColumnTypes(ctx, conn, "workspace_config")
	if err != nil {
		return err
	}
	if len(columns) > 0 && (!isIntegerColumnType(columns["created_at"]) || !isIntegerColumnType(columns["updated_at"])) {
		return rebuildLegacyWorkspaceConfig(ctx, conn)
	}
	return nil
}

func rebuildLegacyGlobalEnv(ctx context.Context, conn migrationConn) error {
	statements := []string{
		`DROP TABLE IF EXISTS global_env_legacy`,
		`ALTER TABLE global_env RENAME TO global_env_legacy`,
		`CREATE TABLE global_env (
			name TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			secret INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
		)`,
		fmt.Sprintf(`INSERT INTO global_env(name, value, secret, updated_at)
			SELECT name, value, secret, %s FROM global_env_legacy`, normalizeSQLiteTimestampExpr("updated_at")),
		`DROP TABLE global_env_legacy`,
	}
	return execLegacyStatements(ctx, conn, "migrate global env schema", statements)
}

func rebuildLegacyWorkspaceConfig(ctx context.Context, conn migrationConn) error {
	statements := []string{
		`DROP TABLE IF EXISTS workspace_config_legacy`,
		`ALTER TABLE workspace_config RENAME TO workspace_config_legacy`,
		`CREATE TABLE workspace_config (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			config_json TEXT NOT NULL DEFAULT '{}',
			comment TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
		)`,
		fmt.Sprintf(`INSERT INTO workspace_config(id, name, type, config_json, comment, created_at, updated_at)
			SELECT id, name, type, config_json, comment, %s, %s FROM workspace_config_legacy`,
			normalizeSQLiteTimestampExpr("created_at"), normalizeSQLiteTimestampExpr("updated_at")),
		`DROP TABLE workspace_config_legacy`,
	}
	return execLegacyStatements(ctx, conn, "migrate workspace config schema", statements)
}

func addLegacyColumns(ctx context.Context, conn migrationConn) error {
	columns := []struct {
		table      string
		name       string
		definition string
	}{
		{table: "agent_definition", name: "capset_ids", definition: "TEXT NOT NULL DEFAULT '[]'"},
		{table: "agent_definition", name: "volumes_json", definition: "TEXT NOT NULL DEFAULT '[]'"},
		{table: "agent_definition", name: "skills", definition: "TEXT NOT NULL DEFAULT '[]'"},
		{table: "agent_definition", name: "managed_project_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "agent_definition", name: "managed_project_revision", definition: "INTEGER NOT NULL DEFAULT 0"},
		{table: "agent_definition", name: "managed_agent_name", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "llm_provider", name: "use_generic_responses_text_parts", definition: "INTEGER NOT NULL DEFAULT 0"},
		{table: "loader", name: "agent_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "loader", name: "capset_ids", definition: "TEXT NOT NULL DEFAULT '[]'"},
		{table: "loader", name: "volumes_json", definition: "TEXT NOT NULL DEFAULT '[]'"},
		{table: "loader", name: "managed_project_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "loader", name: "managed_project_revision", definition: "INTEGER NOT NULL DEFAULT 0"},
		{table: "loader", name: "managed_agent_name", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "loader", name: "managed_scheduler_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "project", name: "short_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "project_agent", name: "id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "project_agent", name: "name", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "project_agent", name: "short_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "project_scheduler", name: "id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "project_scheduler", name: "short_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "project_run", name: "sandbox_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "event", name: "replay_of_event_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "event", name: "claim_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "event", name: "claim_until", definition: "INTEGER NOT NULL DEFAULT 0"},
		{table: "event", name: "attempt_count", definition: "INTEGER NOT NULL DEFAULT 0"},
		{table: "event", name: "next_attempt_at", definition: "INTEGER NOT NULL DEFAULT 0"},
		{table: "event", name: "last_error", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "event", name: "dead_letter_at", definition: "INTEGER NOT NULL DEFAULT 0"},
		{table: "webhook_source", name: "token_header", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		if err := addLegacyColumn(ctx, conn, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	return nil
}

func addLegacyColumn(ctx context.Context, conn migrationConn, table, column, definition string) error {
	columns, err := sqliteTableColumnTypes(ctx, conn, table)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return nil
	}
	if _, exists := columns[column]; exists {
		return nil
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %q ADD COLUMN %q %s`, table, column, definition)); err != nil {
		return fmt.Errorf("add legacy SQLite column %s.%s: %w", table, column, err)
	}
	return nil
}

func rebuildLegacyLoaderBinding(ctx context.Context, conn migrationConn) error {
	columns, err := sqliteTableColumnTypes(ctx, conn, "loader_binding")
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return nil
	}
	if _, exists := columns["trigger_id"]; exists {
		return nil
	}
	statements := []string{
		`ALTER TABLE loader_binding RENAME TO loader_binding_legacy`,
		`CREATE TABLE loader_binding (
			loader_id TEXT NOT NULL,
			trigger_id TEXT NOT NULL DEFAULT '',
			sandbox_id TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			PRIMARY KEY(loader_id, trigger_id)
		)`,
		`INSERT INTO loader_binding(loader_id, trigger_id, sandbox_id, created_at, updated_at)
			SELECT loader_id, '', sandbox_id, created_at, updated_at FROM loader_binding_legacy`,
		`DROP TABLE loader_binding_legacy`,
	}
	return execLegacyStatements(ctx, conn, "migrate loader binding trigger scope", statements)
}

func migrateLegacyLoaderTimestamps(ctx context.Context, conn migrationConn) error {
	statements := []string{
		fmt.Sprintf(`UPDATE loader_trigger SET next_fire_at = next_fire_at * 1000 WHERE next_fire_at > 0 AND next_fire_at < %d`, storeutil.StoredUnixMillisecondThreshold),
		fmt.Sprintf(`UPDATE loader_trigger SET last_fired_at = last_fired_at * 1000 WHERE last_fired_at > 0 AND last_fired_at < %d`, storeutil.StoredUnixMillisecondThreshold),
		fmt.Sprintf(`UPDATE loader_run SET started_at = started_at * 1000 WHERE started_at > 0 AND started_at < %d`, storeutil.StoredUnixMillisecondThreshold),
		fmt.Sprintf(`UPDATE loader_run SET completed_at = completed_at * 1000 WHERE completed_at > 0 AND completed_at < %d`, storeutil.StoredUnixMillisecondThreshold),
		fmt.Sprintf(`UPDATE loader_event SET created_at = created_at * 1000 WHERE created_at > 0 AND created_at < %d`, storeutil.StoredUnixMillisecondThreshold),
	}
	return execLegacyStatements(ctx, conn, "migrate loader timestamp precision", statements)
}

func copyLegacyEventSandboxLinks(ctx context.Context, conn migrationConn) error {
	exists, err := sqliteTableExists(ctx, conn, "event_session_link")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO event_sandbox_link(
		event_id, sandbox_id, relation, loader_id, run_id, trigger_id, loader_event_id, created_at
	) SELECT event_id, session_id, relation, loader_id, run_id, trigger_id, loader_event_id, created_at
	FROM event_session_link`); err != nil {
		return fmt.Errorf("copy legacy event_session_link rows: %w", err)
	}
	// Keep event_session_link intact for the existing compatibility window; new
	// databases never receive that legacy table from the baseline.
	return nil
}

func sqliteTableColumnTypes(ctx context.Context, conn migrationConn, table string) (map[string]string, error) {
	rows, err := conn.QueryContext(ctx, fmt.Sprintf(`SELECT name, type FROM pragma_table_info('%s')`, strings.ReplaceAll(table, "'", "''")))
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

func sqliteTableExists(ctx context.Context, conn migrationConn, table string) (bool, error) {
	var count int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		return false, fmt.Errorf("query SQLite table %s: %w", table, err)
	}
	return count > 0, nil
}

func execLegacyStatements(ctx context.Context, conn migrationConn, operation string, statements []string) error {
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%s: %w", operation, err)
		}
	}
	return nil
}

func normalizeSQLiteTimestampExpr(column string) string {
	return fmt.Sprintf(`CASE
		WHEN trim(COALESCE(%[1]s, '')) = '' THEN CAST(strftime('%%s','now') AS INTEGER)
		WHEN trim(COALESCE(%[1]s, '')) NOT GLOB '*[^0-9]*' THEN CAST(%[1]s AS INTEGER)
		ELSE COALESCE(CAST(strftime('%%s', %[1]s) AS INTEGER), CAST(strftime('%%s','now') AS INTEGER))
	END`, column)
}

func isIntegerColumnType(columnType string) bool {
	return strings.Contains(strings.ToUpper(strings.TrimSpace(columnType)), "INT")
}
