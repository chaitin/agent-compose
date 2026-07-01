package agentcompose

import (
	"context"
	"fmt"
)

func (s *ConfigStore) ensureProjectSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS project (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			source_path TEXT NOT NULL DEFAULT '',
			source_json TEXT NOT NULL DEFAULT '{}',
			current_revision INTEGER NOT NULL DEFAULT 0,
			spec_hash TEXT NOT NULL DEFAULT '',
			bundle_hash TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			removed_at INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE INDEX IF NOT EXISTS idx_project_name ON project(name, removed_at);`,
		`CREATE INDEX IF NOT EXISTS idx_project_source_path ON project(source_path);`,
		`CREATE TABLE IF NOT EXISTS project_revision (
			project_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			spec_hash TEXT NOT NULL,
			bundle_hash TEXT NOT NULL DEFAULT '',
			spec_json TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			PRIMARY KEY(project_id, revision),
			FOREIGN KEY(project_id) REFERENCES project(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS project_agent (
			project_id TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			managed_agent_id TEXT NOT NULL DEFAULT '',
			revision INTEGER NOT NULL DEFAULT 0,
			provider TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			image TEXT NOT NULL DEFAULT '',
			driver TEXT NOT NULL DEFAULT '',
			scheduler_enabled INTEGER NOT NULL DEFAULT 0,
			spec_json TEXT NOT NULL DEFAULT '{}',
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			PRIMARY KEY(project_id, agent_name),
			FOREIGN KEY(project_id) REFERENCES project(id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_project_agent_managed_agent ON project_agent(managed_agent_id);`,
		`CREATE TABLE IF NOT EXISTS project_service (
			project_id TEXT NOT NULL,
			service_name TEXT NOT NULL,
			revision INTEGER NOT NULL DEFAULT 0,
			runtime TEXT NOT NULL DEFAULT '',
			entry TEXT NOT NULL DEFAULT '',
			input_schema_ref TEXT NOT NULL DEFAULT '',
			output_schema_ref TEXT NOT NULL DEFAULT '',
			error_schema_ref TEXT NOT NULL DEFAULT '',
			timeout_ms INTEGER NOT NULL DEFAULT 0,
			spec_json TEXT NOT NULL DEFAULT '{}',
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			PRIMARY KEY(project_id, service_name),
			FOREIGN KEY(project_id) REFERENCES project(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS project_scheduler (
			project_id TEXT NOT NULL,
			scheduler_id TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			target_type TEXT NOT NULL DEFAULT 'agent',
			target_name TEXT NOT NULL DEFAULT '',
			managed_loader_id TEXT NOT NULL DEFAULT '',
			revision INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			trigger_count INTEGER NOT NULL DEFAULT 0,
			spec_json TEXT NOT NULL DEFAULT '{}',
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			PRIMARY KEY(project_id, scheduler_id),
			FOREIGN KEY(project_id) REFERENCES project(id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_project_scheduler_agent ON project_scheduler(project_id, agent_name);`,
		`CREATE INDEX IF NOT EXISTS idx_project_scheduler_managed_loader ON project_scheduler(managed_loader_id);`,
		`CREATE TABLE IF NOT EXISTS project_run (
			run_id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			project_name TEXT NOT NULL DEFAULT '',
			project_revision INTEGER NOT NULL DEFAULT 0,
			agent_name TEXT NOT NULL DEFAULT '',
			managed_agent_id TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			scheduler_id TEXT NOT NULL DEFAULT '',
			trigger_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			session_id TEXT NOT NULL DEFAULT '',
			exit_code INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			prompt TEXT NOT NULL DEFAULT '',
			output TEXT NOT NULL DEFAULT '',
			result_json TEXT NOT NULL DEFAULT '',
			logs_path TEXT NOT NULL DEFAULT '',
			artifacts_dir TEXT NOT NULL DEFAULT '',
			cleanup_error TEXT NOT NULL DEFAULT '',
			driver TEXT NOT NULL DEFAULT '',
			image_ref TEXT NOT NULL DEFAULT '',
			started_at INTEGER NOT NULL DEFAULT 0,
			completed_at INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			FOREIGN KEY(project_id) REFERENCES project(id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_project_run_project_status ON project_run(project_id, status, created_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_project_run_agent ON project_run(project_id, agent_name, created_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_project_run_session ON project_run(session_id);`,
		`CREATE INDEX IF NOT EXISTS idx_project_run_scheduler ON project_run(project_id, scheduler_id, trigger_id);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create project schema: %w", err)
		}
	}
	if err := s.ensureProjectColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureProjectRevisionColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureProjectSchedulerTargetSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureManagedResourceColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureProjectRunColumns(ctx); err != nil {
		return err
	}
	return nil
}

func (s *ConfigStore) ensureProjectColumns(ctx context.Context) error {
	if err := ensureColumn(ctx, s.db, "project", "bundle_hash", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure project bundle_hash column: %w", err)
	}
	return nil
}

func (s *ConfigStore) ensureProjectRevisionColumns(ctx context.Context) error {
	if err := ensureColumn(ctx, s.db, "project_revision", "bundle_hash", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure project revision bundle_hash column: %w", err)
	}
	statements := []string{
		`DROP INDEX IF EXISTS idx_project_revision_hash;`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_project_revision_hash_bundle ON project_revision(project_id, spec_hash, bundle_hash);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create project revision bundle index: %w", err)
		}
	}
	return nil
}

func (s *ConfigStore) ensureProjectSchedulerTargetSchema(ctx context.Context) error {
	if err := ensureColumn(ctx, s.db, "project_scheduler", "target_type", "TEXT NOT NULL DEFAULT 'agent'"); err != nil {
		return fmt.Errorf("ensure project scheduler target_type column: %w", err)
	}
	if err := ensureColumn(ctx, s.db, "project_scheduler", "target_name", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure project scheduler target_name column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE project_scheduler SET target_type = 'agent' WHERE target_type = ''`); err != nil {
		return fmt.Errorf("backfill project scheduler target_type: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE project_scheduler SET target_name = agent_name WHERE target_name = ''`); err != nil {
		return fmt.Errorf("backfill project scheduler target_name: %w", err)
	}
	if err := s.rebuildProjectSchedulerWithoutAgentFK(ctx); err != nil {
		return err
	}
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_project_scheduler_agent ON project_scheduler(project_id, agent_name);`,
		`CREATE INDEX IF NOT EXISTS idx_project_scheduler_target ON project_scheduler(project_id, target_type, target_name);`,
		`CREATE INDEX IF NOT EXISTS idx_project_scheduler_managed_loader ON project_scheduler(managed_loader_id);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create project scheduler target index: %w", err)
		}
	}
	return nil
}

func (s *ConfigStore) rebuildProjectSchedulerWithoutAgentFK(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_list(project_scheduler)`)
	if err != nil {
		return fmt.Errorf("inspect project_scheduler foreign keys: %w", err)
	}
	hasAgentFK := false
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan project_scheduler foreign keys: %w", err)
		}
		if table == "project_agent" {
			hasAgentFK = true
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close project_scheduler foreign keys: %w", err)
	}
	if !hasAgentFK {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys for project_scheduler rebuild: %w", err)
	}
	defer func() { _, _ = s.db.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`) }()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS project_scheduler_new (
			project_id TEXT NOT NULL,
			scheduler_id TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			target_type TEXT NOT NULL DEFAULT 'agent',
			target_name TEXT NOT NULL DEFAULT '',
			managed_loader_id TEXT NOT NULL DEFAULT '',
			revision INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			trigger_count INTEGER NOT NULL DEFAULT 0,
			spec_json TEXT NOT NULL DEFAULT '{}',
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			PRIMARY KEY(project_id, scheduler_id),
			FOREIGN KEY(project_id) REFERENCES project(id) ON DELETE CASCADE
		);`,
		`INSERT INTO project_scheduler_new(
			project_id, scheduler_id, agent_name, target_type, target_name, managed_loader_id, revision, enabled, trigger_count, spec_json, created_at, updated_at
		)
		SELECT project_id, scheduler_id, agent_name,
			CASE WHEN target_type = '' THEN 'agent' ELSE target_type END,
			CASE WHEN target_name = '' THEN agent_name ELSE target_name END,
			managed_loader_id, revision, enabled, trigger_count, spec_json, created_at, updated_at
		FROM project_scheduler;`,
		`DROP TABLE project_scheduler;`,
		`ALTER TABLE project_scheduler_new RENAME TO project_scheduler;`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("rebuild project_scheduler without agent foreign key: %w", err)
		}
	}
	return nil
}

func (s *ConfigStore) ensureProjectRunColumns(ctx context.Context) error {
	columns := []struct {
		name       string
		definition string
	}{
		{name: "client_request_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "target_type", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "target_name", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "input_json", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "output_json", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "runtime_context_json", definition: "TEXT NOT NULL DEFAULT '{}'"},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, s.db, "project_run", column.name, column.definition); err != nil {
			return fmt.Errorf("ensure project run column %s: %w", column.name, err)
		}
	}
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_project_run_client_request ON project_run(client_request_id);`,
		`CREATE INDEX IF NOT EXISTS idx_project_run_target ON project_run(project_id, target_type, target_name, created_at DESC);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create project run context index: %w", err)
		}
	}
	return nil
}

func (s *ConfigStore) ensureManagedResourceColumns(ctx context.Context) error {
	agentColumns := []struct {
		name       string
		definition string
	}{
		{name: "managed_project_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "managed_project_revision", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "managed_agent_name", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range agentColumns {
		if err := ensureColumn(ctx, s.db, "agent_definition", column.name, column.definition); err != nil {
			return fmt.Errorf("ensure agent definition managed column %s: %w", column.name, err)
		}
	}

	loaderColumns := []struct {
		name       string
		definition string
	}{
		{name: "managed_project_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "managed_project_revision", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "managed_agent_name", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "managed_scheduler_id", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range loaderColumns {
		if err := ensureColumn(ctx, s.db, "loader", column.name, column.definition); err != nil {
			return fmt.Errorf("ensure loader managed column %s: %w", column.name, err)
		}
	}

	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_agent_definition_managed_project ON agent_definition(managed_project_id, managed_agent_name);`,
		`CREATE INDEX IF NOT EXISTS idx_loader_managed_project ON loader(managed_project_id, managed_agent_name, managed_scheduler_id);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create managed resource index: %w", err)
		}
	}
	return nil
}
