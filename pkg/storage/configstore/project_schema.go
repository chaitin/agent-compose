package configstore

import (
	"context"
	"fmt"
)

func (s *projectStore) ensureProjectSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS project (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			short_id TEXT NOT NULL DEFAULT '',
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
		`DROP INDEX IF EXISTS idx_project_revision_hash;`,
		`CREATE INDEX IF NOT EXISTS idx_project_revision_hash ON project_revision(project_id, spec_hash);`,
		`CREATE TABLE IF NOT EXISTS project_agent (
			id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			short_id TEXT NOT NULL DEFAULT '',
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
			id TEXT NOT NULL DEFAULT '',
			short_id TEXT NOT NULL DEFAULT '',
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
			sandbox_id TEXT NOT NULL DEFAULT '',
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
		`CREATE INDEX IF NOT EXISTS idx_project_run_scheduler ON project_run(project_id, scheduler_id, trigger_id);`,
		`CREATE TABLE IF NOT EXISTS project_run_event (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			kind TEXT NOT NULL,
			text TEXT NOT NULL DEFAULT '',
			agent TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL DEFAULT '',
			success INTEGER NOT NULL DEFAULT 0,
			exit_code INTEGER NOT NULL DEFAULT 0,
			stop_reason TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER) * 1000),
			UNIQUE(run_id, seq),
			FOREIGN KEY(run_id) REFERENCES project_run(run_id) ON DELETE CASCADE
		);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create project schema: %w", err)
		}
	}
	if err := s.ensureProjectColumns(ctx); err != nil {
		return err
	}
	indexStatements := []string{
		`CREATE INDEX IF NOT EXISTS idx_project_short_id ON project(short_id);`,
		`CREATE INDEX IF NOT EXISTS idx_project_agent_id ON project_agent(id);`,
		`CREATE INDEX IF NOT EXISTS idx_project_agent_name ON project_agent(agent_name, project_id);`,
		`CREATE INDEX IF NOT EXISTS idx_project_scheduler_id ON project_scheduler(id);`,
		`CREATE INDEX IF NOT EXISTS idx_project_run_sandbox ON project_run(sandbox_id);`,
		`CREATE INDEX IF NOT EXISTS idx_project_run_event_sequence ON project_run_event(run_id, seq);`,
	}
	for _, stmt := range indexStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create project schema: %w", err)
		}
	}
	if err := s.ensureManagedResourceColumns(ctx); err != nil {
		return err
	}
	return nil
}

func (s *projectStore) EnsureProjectSchema(ctx context.Context) error {
	return s.ensureProjectSchema(ctx)
}

func (s *projectStore) ensureProjectColumns(ctx context.Context) error {
	projectColumns := []struct {
		name       string
		definition string
	}{
		{name: "short_id", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range projectColumns {
		if err := ensureColumn(ctx, s.db, "project", column.name, column.definition); err != nil {
			return fmt.Errorf("ensure project column %s: %w", column.name, err)
		}
	}

	agentColumns := []struct {
		name       string
		definition string
	}{
		{name: "id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "name", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "short_id", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range agentColumns {
		if err := ensureColumn(ctx, s.db, "project_agent", column.name, column.definition); err != nil {
			return fmt.Errorf("ensure project agent column %s: %w", column.name, err)
		}
	}

	schedulerColumns := []struct {
		name       string
		definition string
	}{
		{name: "id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "short_id", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range schedulerColumns {
		if err := ensureColumn(ctx, s.db, "project_scheduler", column.name, column.definition); err != nil {
			return fmt.Errorf("ensure project scheduler column %s: %w", column.name, err)
		}
	}

	runColumns := []struct {
		name       string
		definition string
	}{
		{name: "sandbox_id", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range runColumns {
		if err := ensureColumn(ctx, s.db, "project_run", column.name, column.definition); err != nil {
			return fmt.Errorf("ensure project run column %s: %w", column.name, err)
		}
	}
	return nil
}

func (s *projectStore) ensureManagedResourceColumns(ctx context.Context) error {
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
