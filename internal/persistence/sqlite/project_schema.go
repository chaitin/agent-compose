package sqlite

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
			spec_json TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			PRIMARY KEY(project_id, revision),
			FOREIGN KEY(project_id) REFERENCES project(id) ON DELETE CASCADE
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_project_revision_hash ON project_revision(project_id, spec_hash);`,
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
		`CREATE TABLE IF NOT EXISTS project_scheduler (
			project_id TEXT NOT NULL,
			scheduler_id TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			managed_loader_id TEXT NOT NULL DEFAULT '',
			revision INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			trigger_count INTEGER NOT NULL DEFAULT 0,
			spec_json TEXT NOT NULL DEFAULT '{}',
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			PRIMARY KEY(project_id, scheduler_id),
			FOREIGN KEY(project_id) REFERENCES project(id) ON DELETE CASCADE,
			FOREIGN KEY(project_id, agent_name) REFERENCES project_agent(project_id, agent_name) ON DELETE CASCADE
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
	if err := s.ensureManagedResourceColumns(ctx); err != nil {
		return err
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
