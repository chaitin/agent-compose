package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/runs"
)

// projectStore owns projects, revisions, agents, schedulers, runs, and sessions.
type projectStore struct {
	db *sql.DB
}

func (s *projectStore) UpsertProject(ctx context.Context, project ProjectRecord) (ProjectRecord, error) {
	project, err := projects.NormalizeRecord(project)
	if err != nil {
		return ProjectRecord{}, err
	}
	now := time.Now().UTC()
	existing, found, err := s.getProject(ctx, project.ID, true)
	if err != nil {
		return ProjectRecord{}, err
	}
	if found {
		project.CreatedAt = existing.CreatedAt
		project.CurrentRevision = existing.CurrentRevision
		if project.SpecHash == "" {
			project.SpecHash = existing.SpecHash
		}
		project.UpdatedAt = now
		project.RemovedAt = time.Time{}
		result, err := s.db.ExecContext(ctx, `UPDATE project SET
			name = ?, source_path = ?, source_json = ?, spec_hash = ?, updated_at = ?, removed_at = 0
			WHERE id = ?`,
			project.Name, project.SourcePath, project.SourceJSON, project.SpecHash, project.UpdatedAt.Unix(), project.ID)
		if err != nil {
			return ProjectRecord{}, fmt.Errorf("update project %s: %w", project.ID, err)
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", project.ID, fmt.Sprintf("project %s not found", project.ID), nil)
		}
		return s.GetProject(ctx, project.ID)
	}

	project.CreatedAt = now
	project.UpdatedAt = now
	if _, err := s.db.ExecContext(ctx, `INSERT INTO project(
		id, name, source_path, source_json, current_revision, spec_hash, created_at, updated_at, removed_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		project.ID, project.Name, project.SourcePath, project.SourceJSON, project.CurrentRevision, project.SpecHash, project.CreatedAt.Unix(), project.UpdatedAt.Unix()); err != nil {
		return ProjectRecord{}, fmt.Errorf("insert project %s: %w", project.ID, err)
	}
	return s.GetProject(ctx, project.ID)
}

func (s *projectStore) MarkProjectRemoved(ctx context.Context, projectID string) (ProjectRecord, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ProjectRecord{}, fmt.Errorf("project id is required")
	}
	existing, found, err := s.getProject(ctx, projectID, true)
	if err != nil {
		return ProjectRecord{}, err
	}
	if !found {
		return ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", projectID, fmt.Sprintf("project %s not found", projectID), sql.ErrNoRows)
	}
	if !existing.RemovedAt.IsZero() {
		return existing, nil
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE project SET updated_at = ?, removed_at = ? WHERE id = ? AND removed_at = 0`,
		now.Unix(), now.Unix(), projectID)
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("mark project %s removed: %w", projectID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return s.getProjectRequired(ctx, projectID, true)
	}
	return s.getProjectRequired(ctx, projectID, true)
}

func (s *projectStore) SaveProjectRevision(ctx context.Context, revision ProjectRevisionRecord) (ProjectRevisionRecord, bool, error) {
	revision.ProjectID = strings.TrimSpace(revision.ProjectID)
	revision.SpecHash = strings.TrimSpace(revision.SpecHash)
	revision.SpecJSON = strings.TrimSpace(revision.SpecJSON)
	if revision.ProjectID == "" || revision.SpecHash == "" || revision.SpecJSON == "" {
		return ProjectRevisionRecord{}, false, fmt.Errorf("project id, spec hash, and spec json are required")
	}
	if !json.Valid([]byte(revision.SpecJSON)) {
		return ProjectRevisionRecord{}, false, fmt.Errorf("project revision spec_json must be valid JSON")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("begin project revision tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `SELECT project_id, revision, spec_hash, spec_json, created_at
		FROM project_revision WHERE project_id = ? AND spec_hash = ?`, revision.ProjectID, revision.SpecHash)
	existing, err := projects.ScanProjectRevision(row.Scan)
	if err == nil {
		return existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ProjectRevisionRecord{}, false, err
	}

	var nextRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) + 1 FROM project_revision WHERE project_id = ?`, revision.ProjectID).Scan(&nextRevision); err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("query next project revision %s: %w", revision.ProjectID, err)
	}
	now := time.Now().UTC()
	revision.Revision = nextRevision
	revision.CreatedAt = now
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_revision(project_id, revision, spec_hash, spec_json, created_at)
		VALUES(?, ?, ?, ?, ?)`, revision.ProjectID, revision.Revision, revision.SpecHash, revision.SpecJSON, revision.CreatedAt.Unix()); err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("insert project revision %s/%d: %w", revision.ProjectID, revision.Revision, err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE project SET current_revision = ?, spec_hash = ?, updated_at = ?, removed_at = 0 WHERE id = ?`,
		revision.Revision, revision.SpecHash, now.Unix(), revision.ProjectID)
	if err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("update project revision pointer %s: %w", revision.ProjectID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return ProjectRevisionRecord{}, false, domain.ResourceError(domain.ErrNotFound, "project", revision.ProjectID, fmt.Sprintf("project %s not found", revision.ProjectID), nil)
	}
	if err := tx.Commit(); err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("commit project revision tx: %w", err)
	}
	return revision, true, nil
}

func (s *projectStore) GetProject(ctx context.Context, projectID string) (ProjectRecord, error) {
	item, found, err := s.getProject(ctx, projectID, false)
	if err != nil {
		return ProjectRecord{}, err
	}
	if !found {
		id := strings.TrimSpace(projectID)
		return ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", id, fmt.Sprintf("project %s not found", id), sql.ErrNoRows)
	}
	return item, nil
}

func (s *projectStore) ListProjects(ctx context.Context, options ProjectListOptions) (ProjectListResult, error) {
	limit := options.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := options.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, source_path, source_json, current_revision, spec_hash, created_at, updated_at, removed_at
		FROM project ORDER BY updated_at DESC, created_at DESC, id ASC`)
	if err != nil {
		return ProjectListResult{}, fmt.Errorf("query projects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	query := strings.ToLower(strings.TrimSpace(options.Query))
	matched := make([]ProjectRecord, 0)
	for rows.Next() {
		item, err := projects.ScanProject(rows.Scan)
		if err != nil {
			return ProjectListResult{}, err
		}
		if !options.IncludeRemoved && !item.RemovedAt.IsZero() {
			continue
		}
		if query != "" && !projects.RecordMatchesQuery(item, query) {
			continue
		}
		matched = append(matched, item)
	}
	if err := rows.Err(); err != nil {
		return ProjectListResult{}, fmt.Errorf("iterate projects: %w", err)
	}
	total := len(matched)
	end := offset + limit
	if offset > total {
		offset = total
	}
	if end > total {
		end = total
	}
	return ProjectListResult{
		Projects:   matched[offset:end],
		TotalCount: total,
		HasMore:    end < total,
		NextOffset: end,
	}, nil
}

func (s *projectStore) GetProjectRevision(ctx context.Context, projectID string, revision int64) (ProjectRevisionRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT project_id, revision, spec_hash, spec_json, created_at
		FROM project_revision WHERE project_id = ? AND revision = ?`, strings.TrimSpace(projectID), revision)
	item, err := projects.ScanProjectRevision(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			id := fmt.Sprintf("%s/%d", strings.TrimSpace(projectID), revision)
			return ProjectRevisionRecord{}, domain.ResourceError(domain.ErrNotFound, "project revision", id, fmt.Sprintf("project revision %s not found", id), err)
		}
		return ProjectRevisionRecord{}, err
	}
	return item, nil
}

func (s *projectStore) UpsertProjectAgent(ctx context.Context, agent ProjectAgentRecord) (ProjectAgentRecord, error) {
	agent, err := projects.NormalizeAgentRecord(agent)
	if err != nil {
		return ProjectAgentRecord{}, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE project_agent SET
		managed_agent_id = ?, revision = ?, provider = ?, model = ?, image = ?, driver = ?, scheduler_enabled = ?, spec_json = ?, updated_at = ?
		WHERE project_id = ? AND agent_name = ?`,
		agent.ManagedAgentID, agent.Revision, agent.Provider, agent.Model, agent.Image, agent.Driver, BoolToInt(agent.SchedulerEnabled), agent.SpecJSON, now.Unix(),
		agent.ProjectID, agent.AgentName)
	if err != nil {
		return ProjectAgentRecord{}, fmt.Errorf("update project agent %s/%s: %w", agent.ProjectID, agent.AgentName, err)
	}
	if rows, _ := result.RowsAffected(); rows > 0 {
		return s.GetProjectAgent(ctx, agent.ProjectID, agent.AgentName)
	}
	agent.CreatedAt = now
	agent.UpdatedAt = now
	if _, err := s.db.ExecContext(ctx, `INSERT INTO project_agent(
		project_id, agent_name, managed_agent_id, revision, provider, model, image, driver, scheduler_enabled, spec_json, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agent.ProjectID, agent.AgentName, agent.ManagedAgentID, agent.Revision, agent.Provider, agent.Model, agent.Image, agent.Driver, BoolToInt(agent.SchedulerEnabled), agent.SpecJSON,
		agent.CreatedAt.Unix(), agent.UpdatedAt.Unix()); err != nil {
		return ProjectAgentRecord{}, fmt.Errorf("insert project agent %s/%s: %w", agent.ProjectID, agent.AgentName, err)
	}
	return s.GetProjectAgent(ctx, agent.ProjectID, agent.AgentName)
}

func (s *projectStore) GetProjectAgent(ctx context.Context, projectID, agentName string) (ProjectAgentRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT project_id, agent_name, managed_agent_id, revision, provider, model, image, driver, scheduler_enabled, spec_json, created_at, updated_at
		FROM project_agent WHERE project_id = ? AND agent_name = ?`, strings.TrimSpace(projectID), strings.TrimSpace(agentName))
	item, err := projects.ScanProjectAgent(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			id := strings.TrimSpace(projectID) + "/" + strings.TrimSpace(agentName)
			return ProjectAgentRecord{}, domain.ResourceError(domain.ErrNotFound, "project agent", id, fmt.Sprintf("project agent %s not found", id), err)
		}
		return ProjectAgentRecord{}, err
	}
	return item, nil
}

func (s *projectStore) ListProjectAgents(ctx context.Context, projectID string) ([]ProjectAgentRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, agent_name, managed_agent_id, revision, provider, model, image, driver, scheduler_enabled, spec_json, created_at, updated_at
		FROM project_agent WHERE project_id = ? ORDER BY agent_name ASC`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("query project agents %s: %w", strings.TrimSpace(projectID), err)
	}
	defer func() { _ = rows.Close() }()
	var items []ProjectAgentRecord
	for rows.Next() {
		item, err := projects.ScanProjectAgent(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project agents %s: %w", strings.TrimSpace(projectID), err)
	}
	return items, nil
}

func (s *projectStore) UpsertProjectScheduler(ctx context.Context, scheduler ProjectSchedulerRecord) (ProjectSchedulerRecord, error) {
	scheduler, err := projects.NormalizeSchedulerRecord(scheduler)
	if err != nil {
		return ProjectSchedulerRecord{}, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE project_scheduler SET
		agent_name = ?, managed_loader_id = ?, revision = ?, enabled = ?, trigger_count = ?, spec_json = ?, updated_at = ?
		WHERE project_id = ? AND scheduler_id = ?`,
		scheduler.AgentName, scheduler.ManagedLoaderID, scheduler.Revision, BoolToInt(scheduler.Enabled), scheduler.TriggerCount, scheduler.SpecJSON, now.Unix(),
		scheduler.ProjectID, scheduler.SchedulerID)
	if err != nil {
		return ProjectSchedulerRecord{}, fmt.Errorf("update project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
	}
	if rows, _ := result.RowsAffected(); rows > 0 {
		return s.GetProjectScheduler(ctx, scheduler.ProjectID, scheduler.SchedulerID)
	}
	scheduler.CreatedAt = now
	scheduler.UpdatedAt = now
	if _, err := s.db.ExecContext(ctx, `INSERT INTO project_scheduler(
		project_id, scheduler_id, agent_name, managed_loader_id, revision, enabled, trigger_count, spec_json, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		scheduler.ProjectID, scheduler.SchedulerID, scheduler.AgentName, scheduler.ManagedLoaderID, scheduler.Revision, BoolToInt(scheduler.Enabled), scheduler.TriggerCount, scheduler.SpecJSON,
		scheduler.CreatedAt.Unix(), scheduler.UpdatedAt.Unix()); err != nil {
		return ProjectSchedulerRecord{}, fmt.Errorf("insert project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
	}
	return s.GetProjectScheduler(ctx, scheduler.ProjectID, scheduler.SchedulerID)
}

func (s *projectStore) GetProjectScheduler(ctx context.Context, projectID, schedulerID string) (ProjectSchedulerRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT project_id, scheduler_id, agent_name, managed_loader_id, revision, enabled, trigger_count, spec_json, created_at, updated_at
		FROM project_scheduler WHERE project_id = ? AND scheduler_id = ?`, strings.TrimSpace(projectID), strings.TrimSpace(schedulerID))
	item, err := projects.ScanProjectScheduler(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			id := strings.TrimSpace(projectID) + "/" + strings.TrimSpace(schedulerID)
			return ProjectSchedulerRecord{}, domain.ResourceError(domain.ErrNotFound, "project scheduler", id, fmt.Sprintf("project scheduler %s not found", id), err)
		}
		return ProjectSchedulerRecord{}, err
	}
	return item, nil
}

func (s *projectStore) SetProjectSchedulerEnabled(ctx context.Context, projectID, schedulerID string, enabled bool) (ProjectSchedulerRecord, error) {
	projectID = strings.TrimSpace(projectID)
	schedulerID = strings.TrimSpace(schedulerID)
	if projectID == "" || schedulerID == "" {
		return ProjectSchedulerRecord{}, fmt.Errorf("project scheduler id is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE project_scheduler SET enabled = ?, updated_at = ? WHERE project_id = ? AND scheduler_id = ?`,
		BoolToInt(enabled), time.Now().UTC().Unix(), projectID, schedulerID)
	if err != nil {
		return ProjectSchedulerRecord{}, fmt.Errorf("update project scheduler %s/%s enabled state: %w", projectID, schedulerID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		id := projectID + "/" + schedulerID
		return ProjectSchedulerRecord{}, domain.ResourceError(domain.ErrNotFound, "project scheduler", id, fmt.Sprintf("project scheduler %s not found", id), nil)
	}
	return s.GetProjectScheduler(ctx, projectID, schedulerID)
}

func (s *projectStore) ListProjectSchedulers(ctx context.Context, projectID string) ([]ProjectSchedulerRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, scheduler_id, agent_name, managed_loader_id, revision, enabled, trigger_count, spec_json, created_at, updated_at
		FROM project_scheduler WHERE project_id = ? ORDER BY agent_name ASC, scheduler_id ASC`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("query project schedulers %s: %w", strings.TrimSpace(projectID), err)
	}
	defer func() { _ = rows.Close() }()
	var items []ProjectSchedulerRecord
	for rows.Next() {
		item, err := projects.ScanProjectScheduler(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project schedulers %s: %w", strings.TrimSpace(projectID), err)
	}
	return items, nil
}

func (s *projectStore) CreateProjectRun(ctx context.Context, run ProjectRunRecord) (ProjectRunRecord, error) {
	run, err := projects.NormalizeRunRecord(run)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	now := time.Now().UTC()
	run.CreatedAt = now
	run.UpdatedAt = now
	if _, err := s.db.ExecContext(ctx, `INSERT INTO project_run(
		run_id, project_id, project_name, project_revision, agent_name, managed_agent_id, source, scheduler_id, trigger_id, status,
		session_id, exit_code, error, prompt, output, result_json, logs_path, artifacts_dir, cleanup_error, driver, image_ref,
		started_at, completed_at, duration_ms, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.RunID, run.ProjectID, run.ProjectName, run.ProjectRevision, run.AgentName, run.ManagedAgentID, run.Source, run.SchedulerID, run.TriggerID, run.Status,
		run.SessionID, run.ExitCode, run.Error, run.Prompt, run.Output, run.ResultJSON, run.LogsPath, run.ArtifactsDir, run.CleanupError, run.Driver, run.ImageRef,
		domain.NonZeroTimeUnixMilli(run.StartedAt), domain.NonZeroTimeUnixMilli(run.CompletedAt), run.DurationMs, run.CreatedAt.Unix(), run.UpdatedAt.Unix()); err != nil {
		return ProjectRunRecord{}, fmt.Errorf("insert project run %s: %w", run.RunID, err)
	}
	return s.GetProjectRun(ctx, run.RunID)
}

func (s *projectStore) UpdateProjectRun(ctx context.Context, run ProjectRunRecord) (ProjectRunRecord, error) {
	run, err := projects.NormalizeRunRecord(run)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE project_run SET
		project_id = ?, project_name = ?, project_revision = ?, agent_name = ?, managed_agent_id = ?, source = ?, scheduler_id = ?, trigger_id = ?, status = ?,
		session_id = ?, exit_code = ?, error = ?, prompt = ?, output = ?, result_json = ?, logs_path = ?, artifacts_dir = ?, cleanup_error = ?, driver = ?, image_ref = ?,
		started_at = ?, completed_at = ?, duration_ms = ?, updated_at = ?
		WHERE run_id = ?`,
		run.ProjectID, run.ProjectName, run.ProjectRevision, run.AgentName, run.ManagedAgentID, run.Source, run.SchedulerID, run.TriggerID, run.Status,
		run.SessionID, run.ExitCode, run.Error, run.Prompt, run.Output, run.ResultJSON, run.LogsPath, run.ArtifactsDir, run.CleanupError, run.Driver, run.ImageRef,
		domain.NonZeroTimeUnixMilli(run.StartedAt), domain.NonZeroTimeUnixMilli(run.CompletedAt), run.DurationMs, now.Unix(), run.RunID)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("update project run %s: %w", run.RunID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return ProjectRunRecord{}, domain.ResourceError(domain.ErrNotFound, "project run", run.RunID, fmt.Sprintf("project run %s not found", run.RunID), nil)
	}
	return s.GetProjectRun(ctx, run.RunID)
}

func (s *projectStore) GetProjectRun(ctx context.Context, runID string) (ProjectRunRecord, error) {
	row := s.db.QueryRowContext(ctx, projects.SelectProjectRunSQL()+` WHERE run_id = ?`, strings.TrimSpace(runID))
	item, err := projects.ScanProjectRun(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			id := strings.TrimSpace(runID)
			return ProjectRunRecord{}, domain.ResourceError(domain.ErrNotFound, "project run", id, fmt.Sprintf("project run %s not found", id), err)
		}
		return ProjectRunRecord{}, err
	}
	return item, nil
}

func (s *projectStore) ListProjectRuns(ctx context.Context, projectID string, limit int) ([]ProjectRunRecord, error) {
	return s.ListProjectRunsByOptions(ctx, ProjectRunListOptions{ProjectID: projectID, Limit: limit})
}

func (s *projectStore) ListProjectRunsByOptions(ctx context.Context, options ProjectRunListOptions) ([]ProjectRunRecord, error) {
	limit := options.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := options.Offset
	if offset < 0 {
		offset = 0
	}
	where := make([]string, 0, 6)
	args := make([]any, 0, 8)
	if projectID := strings.TrimSpace(options.ProjectID); projectID != "" {
		where = append(where, "project_id = ?")
		args = append(args, projectID)
	}
	if agentName := strings.TrimSpace(options.AgentName); agentName != "" {
		where = append(where, "agent_name = ?")
		args = append(args, agentName)
	}
	if sessionID := strings.TrimSpace(options.SessionID); sessionID != "" {
		where = append(where, "session_id = ?")
		args = append(args, sessionID)
	}
	if schedulerID := strings.TrimSpace(options.SchedulerID); schedulerID != "" {
		where = append(where, "scheduler_id = ?")
		args = append(args, schedulerID)
	}
	if status := strings.TrimSpace(options.Status); status != "" {
		where = append(where, "status = ?")
		args = append(args, projects.NormalizeRunStatus(status))
	}
	if source := strings.TrimSpace(options.Source); source != "" {
		where = append(where, "source = ?")
		args = append(args, runs.NormalizeSource(source))
	}
	query := projects.SelectProjectRunSQL()
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY created_at DESC, run_id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query project runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var items []ProjectRunRecord
	for rows.Next() {
		item, err := projects.ScanProjectRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project runs: %w", err)
	}
	return items, nil
}

func (s *projectStore) getProject(ctx context.Context, projectID string, includeRemoved bool) (ProjectRecord, bool, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ProjectRecord{}, false, fmt.Errorf("project id is required")
	}
	where := "id = ? AND removed_at = 0"
	if includeRemoved {
		where = "id = ?"
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, name, source_path, source_json, current_revision, spec_hash, created_at, updated_at, removed_at
		FROM project WHERE `+where, projectID)
	item, err := projects.ScanProject(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectRecord{}, false, nil
		}
		return ProjectRecord{}, false, err
	}
	return item, true, nil
}

func (s *projectStore) getProjectRequired(ctx context.Context, projectID string, includeRemoved bool) (ProjectRecord, error) {
	item, found, err := s.getProject(ctx, projectID, includeRemoved)
	if err != nil {
		return ProjectRecord{}, err
	}
	if !found {
		id := strings.TrimSpace(projectID)
		return ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", id, fmt.Sprintf("project %s not found", id), sql.ErrNoRows)
	}
	return item, nil
}

func (s *projectStore) GetProjectIfExists(ctx context.Context, projectID string, includeRemoved bool) (ProjectRecord, bool, error) {
	return s.getProject(ctx, projectID, includeRemoved)
}
