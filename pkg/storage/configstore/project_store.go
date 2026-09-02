package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/idset"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// projectStore owns projects, revisions, agents, schedulers, runs, and sandboxes.
type projectStore struct {
	db *sql.DB
}

func (s *projectStore) UpsertProject(ctx context.Context, project ProjectRecord) (ProjectRecord, error) {
	project, err := projects.NormalizeRecord(project)
	if err != nil {
		return ProjectRecord{}, err
	}
	now := time.Now().UTC()
	project.CreatedAt = now
	project.UpdatedAt = now
	if _, err := s.db.ExecContext(ctx, `INSERT INTO project(
		id, name, short_id, source_path, source_json, current_revision, spec_hash, created_at, updated_at, removed_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		short_id = excluded.short_id,
		source_path = excluded.source_path,
		source_json = excluded.source_json,
		spec_hash = CASE WHEN excluded.spec_hash = '' THEN project.spec_hash ELSE excluded.spec_hash END,
		updated_at = excluded.updated_at,
		removed_at = 0`,
		project.ID, project.Name, project.ShortID, project.SourcePath, project.SourceJSON, project.CurrentRevision, project.SpecHash, project.CreatedAt.Unix(), project.UpdatedAt.Unix()); err != nil {
		return ProjectRecord{}, fmt.Errorf("upsert project %s: %w", project.ID, err)
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

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("get project revision conn: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("begin immediate project revision tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	commit := func() error {
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return fmt.Errorf("commit project revision tx: %w", err)
		}
		committed = true
		return nil
	}

	var currentRevision int64
	if err := conn.QueryRowContext(ctx, `SELECT current_revision FROM project WHERE id = ?`, revision.ProjectID).Scan(&currentRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectRevisionRecord{}, false, domain.ResourceError(domain.ErrNotFound, "project", revision.ProjectID, fmt.Sprintf("project %s not found", revision.ProjectID), err)
		}
		return ProjectRevisionRecord{}, false, fmt.Errorf("query current project revision %s: %w", revision.ProjectID, err)
	}
	if currentRevision > 0 {
		row := conn.QueryRowContext(ctx, `SELECT project_id, revision, spec_hash, spec_json, created_at
			FROM project_revision WHERE project_id = ? AND revision = ?`, revision.ProjectID, currentRevision)
		existing, err := projects.ScanProjectRevision(row.Scan)
		if err == nil {
			if existing.SpecHash == revision.SpecHash {
				if err := commit(); err != nil {
					return ProjectRevisionRecord{}, false, err
				}
				return existing, false, nil
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return ProjectRevisionRecord{}, false, err
		}
	}

	var nextRevision int64
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) + 1 FROM project_revision WHERE project_id = ?`, revision.ProjectID).Scan(&nextRevision); err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("query next project revision %s: %w", revision.ProjectID, err)
	}
	now := time.Now().UTC()
	revision.Revision = nextRevision
	revision.CreatedAt = now
	if _, err := conn.ExecContext(ctx, `INSERT INTO project_revision(project_id, revision, spec_hash, spec_json, created_at)
		VALUES(?, ?, ?, ?, ?)`, revision.ProjectID, revision.Revision, revision.SpecHash, revision.SpecJSON, revision.CreatedAt.Unix()); err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("insert project revision %s/%d: %w", revision.ProjectID, revision.Revision, err)
	}
	result, err := conn.ExecContext(ctx, `UPDATE project SET current_revision = ?, spec_hash = ?, updated_at = ?, removed_at = 0 WHERE id = ?`,
		revision.Revision, revision.SpecHash, now.Unix(), revision.ProjectID)
	if err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("update project revision pointer %s: %w", revision.ProjectID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return ProjectRevisionRecord{}, false, domain.ResourceError(domain.ErrNotFound, "project", revision.ProjectID, fmt.Sprintf("project %s not found", revision.ProjectID), nil)
	}
	if err := commit(); err != nil {
		return ProjectRevisionRecord{}, false, err
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
		id = ?, name = ?, short_id = ?, revision = ?, provider = ?, model = ?, image = ?, driver = ?, scheduler_enabled = ?, spec_json = ?, updated_at = ?
		WHERE project_id = ? AND agent_name = ?`,
		agent.ID, agent.Name, agent.ShortID, agent.Revision, agent.Provider, agent.Model, agent.Image, agent.Driver, BoolToInt(agent.SchedulerEnabled), agent.SpecJSON, now.Unix(),
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
		id, name, short_id, project_id, agent_name, revision, provider, model, image, driver, scheduler_enabled, spec_json, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agent.ID, agent.Name, agent.ShortID, agent.ProjectID, agent.AgentName, agent.Revision, agent.Provider, agent.Model, agent.Image, agent.Driver, BoolToInt(agent.SchedulerEnabled), agent.SpecJSON,
		agent.CreatedAt.Unix(), agent.UpdatedAt.Unix()); err != nil {
		return ProjectAgentRecord{}, fmt.Errorf("insert project agent %s/%s: %w", agent.ProjectID, agent.AgentName, err)
	}
	return s.GetProjectAgent(ctx, agent.ProjectID, agent.AgentName)
}

func (s *projectStore) GetProjectAgent(ctx context.Context, projectID, agentName string) (ProjectAgentRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, short_id, project_id, agent_name, revision, provider, model, image, driver, scheduler_enabled, spec_json, created_at, updated_at
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, short_id, project_id, agent_name, revision, provider, model, image, driver, scheduler_enabled, spec_json, created_at, updated_at
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

func (s *projectStore) ListProjectAgentsByIDs(ctx context.Context, agentIDs []string) (map[string]ProjectAgentRecord, error) {
	ids := idset.Normalize(agentIDs)
	if len(ids) == 0 {
		return map[string]ProjectAgentRecord{}, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, short_id, project_id, agent_name, revision, provider, model, image, driver, scheduler_enabled, spec_json, created_at, updated_at
		FROM project_agent WHERE id IN (`+placeholders(len(ids))+`) ORDER BY updated_at DESC, project_id ASC, agent_name ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query project agents by managed agent ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make(map[string]ProjectAgentRecord, len(ids))
	for rows.Next() {
		item, err := projects.ScanProjectAgent(rows.Scan)
		if err != nil {
			return nil, err
		}
		if _, exists := result[item.ID]; !exists {
			result[item.ID] = item
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project agents by managed agent ids: %w", err)
	}
	return result, nil
}

func (s *projectStore) UpsertProjectScheduler(ctx context.Context, scheduler ProjectSchedulerRecord) (ProjectSchedulerRecord, error) {
	scheduler, err := projects.NormalizeSchedulerRecord(scheduler)
	if err != nil {
		return ProjectSchedulerRecord{}, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE project_scheduler SET
		scheduler_id = ?, short_id = ?, agent_name = ?, revision = ?, enabled = ?, trigger_count = ?, spec_json = ?, updated_at = ?
		WHERE project_id = ? AND id = ?`,
		scheduler.ID, scheduler.ShortID, scheduler.AgentName, scheduler.Revision, BoolToInt(scheduler.Enabled), scheduler.TriggerCount, scheduler.SpecJSON, now.Unix(),
		scheduler.ProjectID, scheduler.ID)
	if err != nil {
		return ProjectSchedulerRecord{}, fmt.Errorf("update project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
	}
	if rows, _ := result.RowsAffected(); rows > 0 {
		return s.GetProjectScheduler(ctx, scheduler.ProjectID, scheduler.SchedulerID)
	}
	scheduler.CreatedAt = now
	scheduler.UpdatedAt = now
	if _, err := s.db.ExecContext(ctx, `INSERT INTO project_scheduler(
		id, short_id, project_id, scheduler_id, agent_name, revision, enabled, trigger_count, spec_json, last_error, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		scheduler.ID, scheduler.ShortID, scheduler.ProjectID, scheduler.ID, scheduler.AgentName, scheduler.Revision, BoolToInt(scheduler.Enabled), scheduler.TriggerCount, scheduler.SpecJSON, scheduler.LastError,
		scheduler.CreatedAt.Unix(), scheduler.UpdatedAt.Unix()); err != nil {
		return ProjectSchedulerRecord{}, fmt.Errorf("insert project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
	}
	return s.GetProjectScheduler(ctx, scheduler.ProjectID, scheduler.SchedulerID)
}

func (s *projectStore) GetProjectScheduler(ctx context.Context, projectID, schedulerID string) (ProjectSchedulerRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, short_id, project_id, id, agent_name, revision, enabled, trigger_count, spec_json, created_at, updated_at
		FROM project_scheduler WHERE project_id = ? AND id = ?`, strings.TrimSpace(projectID), strings.TrimSpace(schedulerID))
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
	result, err := s.db.ExecContext(ctx, `UPDATE project_scheduler SET enabled = ?, updated_at = ? WHERE project_id = ? AND id = ?`,
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, short_id, project_id, id, agent_name, revision, enabled, trigger_count, spec_json, created_at, updated_at
		FROM project_scheduler WHERE project_id = ? ORDER BY agent_name ASC, id ASC`, strings.TrimSpace(projectID))
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

func (s *projectStore) ListProjectSchedulersPage(ctx context.Context, query string, offset, limit int) ([]ProjectSchedulerRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	query = strings.ToLower(strings.TrimSpace(query))
	likeQuery := "%" + query + "%"
	rows, err := s.db.QueryContext(ctx, `WITH page AS (
		SELECT s.id, s.short_id, s.project_id, s.id, s.agent_name, s.revision, s.enabled, s.trigger_count, s.spec_json, s.created_at, s.updated_at, s.last_error
		FROM project_scheduler s JOIN project p ON p.id = s.project_id
		WHERE p.removed_at = 0
		AND s.revision = p.current_revision
		AND (? = '' OR lower(p.id) LIKE ? OR lower(p.name) LIKE ? OR lower(p.source_path) LIKE ?)
		ORDER BY s.project_id ASC, s.agent_name ASC, s.id ASC LIMIT ? OFFSET ?
	), run_stats AS (
		SELECT sr.scheduler_id AS id, COUNT(*) AS run_count, MAX(sr.started_at) AS latest_run_at
		FROM scheduler_run sr
		JOIN page ON page.id = sr.scheduler_id
		WHERE sr.trigger_id <> ''
		GROUP BY sr.scheduler_id
	)
	SELECT page.id, page.short_id, page.project_id, page.id, page.agent_name, page.revision, page.enabled, page.trigger_count, page.spec_json, page.created_at, page.updated_at,
		COALESCE(run_stats.run_count, 0),
		run_stats.latest_run_at,
		COALESCE(page.last_error, '')
	FROM page
	LEFT JOIN run_stats ON run_stats.id = page.id
	ORDER BY page.project_id ASC, page.agent_name ASC, page.id ASC`, query, likeQuery, likeQuery, likeQuery, limit, max(offset, 0))
	if err != nil {
		return nil, fmt.Errorf("query project scheduler page: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var items []ProjectSchedulerRecord
	for rows.Next() {
		item, scanErr := projects.ScanProjectSchedulerPage(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project scheduler page: %w", err)
	}
	return items, nil
}

func (s *projectStore) CountProjectSchedulers(ctx context.Context, query string) (int, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	likeQuery := "%" + query + "%"
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_scheduler s JOIN project p ON p.id = s.project_id
		WHERE p.removed_at = 0 AND s.revision = p.current_revision
		AND (? = '' OR lower(p.id) LIKE ? OR lower(p.name) LIKE ? OR lower(p.source_path) LIKE ?)`, query, likeQuery, likeQuery, likeQuery).Scan(&total); err != nil {
		return 0, fmt.Errorf("count project schedulers: %w", err)
	}
	return total, nil
}

func (s *projectStore) CreateProjectRun(ctx context.Context, run ProjectRunRecord) (ProjectRunRecord, error) {
	run, err := projects.NormalizeRunRecord(run)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	now := time.Now().UTC()
	run.CreatedAt = now
	run.UpdatedAt = now
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("begin create project run transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_run(
		run_id, project_id, project_name, project_revision, agent_name, agent_id, source, scheduler_id, scheduler_run_id, trigger_id, status,
		sandbox_id, exit_code, error, error_stack, prompt, output, result_json, logs_path, artifacts_dir, cleanup_error, cleanup_policy, sandbox_created, driver, image_ref,
		started_at, completed_at, duration_ms, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.RunID, run.ProjectID, run.ProjectName, run.ProjectRevision, run.AgentName, run.AgentID, run.Source, run.SchedulerID, nullableSchedulerRunID(run.SchedulerRunID), run.TriggerID, run.Status,
		run.SandboxID, run.ExitCode, run.Error, run.ErrorStack, run.Prompt, run.Output, run.ResultJSON, run.LogsPath, run.ArtifactsDir, run.CleanupError, run.CleanupPolicy, BoolToInt(run.SandboxCreated), run.Driver, run.ImageRef,
		domain.NonZeroTimeUnixMilli(run.StartedAt), domain.NonZeroTimeUnixMilli(run.CompletedAt), run.DurationMs, run.CreatedAt.Unix(), run.UpdatedAt.Unix()); err != nil {
		return ProjectRunRecord{}, fmt.Errorf("insert project run %s: %w", run.RunID, err)
	}
	if err := insertProjectRunLabelsTx(ctx, tx, run.RunID, run.Labels); err != nil {
		return ProjectRunRecord{}, err
	}
	stored, err := getProjectRunTx(ctx, tx, run.RunID)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectRunRecord{}, fmt.Errorf("commit create project run transaction: %w", err)
	}
	return stored, nil
}

func (s *projectStore) UpdateProjectRun(ctx context.Context, run ProjectRunRecord) (ProjectRunRecord, error) {
	run, err := projects.NormalizeRunRecord(run)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE project_run SET
		project_id = ?, project_name = ?, project_revision = ?, agent_name = ?, agent_id = ?, source = ?, scheduler_id = ?, scheduler_run_id = ?, trigger_id = ?, status = ?,
		sandbox_id = ?, exit_code = ?, error = ?, error_stack = ?, prompt = ?, output = ?, result_json = ?, logs_path = ?, artifacts_dir = ?, cleanup_error = ?, cleanup_policy = ?, sandbox_created = ?, driver = ?, image_ref = ?,
		started_at = ?, completed_at = ?, duration_ms = ?, updated_at = ?
		WHERE run_id = ?`,
		run.ProjectID, run.ProjectName, run.ProjectRevision, run.AgentName, run.AgentID, run.Source, run.SchedulerID, nullableSchedulerRunID(run.SchedulerRunID), run.TriggerID, run.Status,
		run.SandboxID, run.ExitCode, run.Error, run.ErrorStack, run.Prompt, run.Output, run.ResultJSON, run.LogsPath, run.ArtifactsDir, run.CleanupError, run.CleanupPolicy, BoolToInt(run.SandboxCreated), run.Driver, run.ImageRef,
		domain.NonZeroTimeUnixMilli(run.StartedAt), domain.NonZeroTimeUnixMilli(run.CompletedAt), run.DurationMs, now.Unix(), run.RunID)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("update project run %s: %w", run.RunID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return ProjectRunRecord{}, domain.ResourceError(domain.ErrNotFound, "project run", run.RunID, fmt.Sprintf("project run %s not found", run.RunID), nil)
	}
	return s.GetProjectRun(ctx, run.RunID)
}

func nullableSchedulerRunID(id string) any {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	return id
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
	labels, err := loadProjectRunLabels(ctx, s.db, item.RunID)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	item.Labels = labels
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
	where, args := projectRunFilter(options)
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

func (s *projectStore) CountProjectRuns(ctx context.Context, options ProjectRunListOptions) (int, error) {
	where, args := projectRunFilter(options)
	query := `SELECT COUNT(*) FROM project_run`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count project runs: %w", err)
	}
	return total, nil
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
	row := s.db.QueryRowContext(ctx, `SELECT id, name, short_id, source_path, source_json, current_revision, spec_hash, created_at, updated_at, removed_at
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
