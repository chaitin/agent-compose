package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"agent-compose/pkg/compose"
	"agent-compose/pkg/model"
)

const (
	ProjectRunStatusPending   = model.ProjectRunStatusPending
	ProjectRunStatusRunning   = model.ProjectRunStatusRunning
	ProjectRunStatusSucceeded = model.ProjectRunStatusSucceeded
	ProjectRunStatusFailed    = model.ProjectRunStatusFailed
	ProjectRunStatusCanceled  = model.ProjectRunStatusCanceled

	ProjectRunSourceManual    = model.ProjectRunSourceManual
	ProjectRunSourceScheduler = model.ProjectRunSourceScheduler
	ProjectRunSourceAPI       = model.ProjectRunSourceAPI
)

type ProjectRecord = model.ProjectRecord
type ProjectRevisionRecord = model.ProjectRevisionRecord
type ProjectAgentRecord = model.ProjectAgentRecord
type ProjectSchedulerRecord = model.ProjectSchedulerRecord
type ProjectRunRecord = model.ProjectRunRecord
type ProjectListOptions = model.ProjectListOptions
type ProjectRunListOptions = model.ProjectRunListOptions
type ProjectListResult = model.ProjectListResult

func StableProjectID(name, sourcePath string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("project name is required")
	}
	if !isProjectStableIdentifier(name) {
		return "", fmt.Errorf("project name %q is not a stable identifier", name)
	}
	return stableReadableID("project", name, name+"|"+normalizeProjectSourcePath(sourcePath)), nil
}

func StableManagedAgentID(projectID, agentName string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	if projectID == "" || agentName == "" {
		return "", fmt.Errorf("project id and agent name are required")
	}
	if !isProjectStableIdentifier(agentName) {
		return "", fmt.Errorf("agent name %q is not a stable identifier", agentName)
	}
	return stableReadableID("agent", agentName, projectID+"|"+agentName), nil
}

func StableProjectSchedulerID(projectID, agentName, schedulerName string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	schedulerName = strings.TrimSpace(schedulerName)
	if schedulerName == "" {
		schedulerName = "default"
	}
	if projectID == "" || agentName == "" {
		return "", fmt.Errorf("project id and agent name are required")
	}
	if !isProjectStableIdentifier(agentName) {
		return "", fmt.Errorf("agent name %q is not a stable identifier", agentName)
	}
	if !isProjectStableIdentifier(schedulerName) {
		return "", fmt.Errorf("scheduler name %q is not a stable identifier", schedulerName)
	}
	return stableReadableID("scheduler", agentName+"-"+schedulerName, projectID+"|"+agentName+"|"+schedulerName), nil
}

func StableManagedLoaderID(projectID, agentName, schedulerName string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	schedulerName = strings.TrimSpace(schedulerName)
	if schedulerName == "" {
		schedulerName = "default"
	}
	if projectID == "" || agentName == "" {
		return "", fmt.Errorf("project id and agent name are required")
	}
	if !isProjectStableIdentifier(agentName) {
		return "", fmt.Errorf("agent name %q is not a stable identifier", agentName)
	}
	if !isProjectStableIdentifier(schedulerName) {
		return "", fmt.Errorf("scheduler name %q is not a stable identifier", schedulerName)
	}
	return stableReadableID("loader", agentName+"-"+schedulerName, projectID+"|"+agentName+"|"+schedulerName), nil
}

func StableManagedTriggerID(projectID, agentName, schedulerName, triggerName string, triggerIndex int) (string, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	schedulerName = strings.TrimSpace(schedulerName)
	triggerName = strings.TrimSpace(triggerName)
	if schedulerName == "" {
		schedulerName = "default"
	}
	if projectID == "" || agentName == "" || triggerIndex < 0 {
		return "", fmt.Errorf("project id, agent name, and trigger index are required")
	}
	if !isProjectStableIdentifier(agentName) {
		return "", fmt.Errorf("agent name %q is not a stable identifier", agentName)
	}
	if !isProjectStableIdentifier(schedulerName) {
		return "", fmt.Errorf("scheduler name %q is not a stable identifier", schedulerName)
	}
	readable := triggerName
	seedPart := "name:" + triggerName
	if readable == "" {
		readable = fmt.Sprintf("trigger-%d", triggerIndex+1)
		seedPart = fmt.Sprintf("path:triggers[%d]", triggerIndex)
	}
	return stableReadableID("trigger", readable, projectID+"|"+agentName+"|"+schedulerName+"|"+seedPart), nil
}

func StableProjectRunID(projectID, agentName, source, idempotencyKey string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	source = strings.TrimSpace(source)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if projectID == "" || agentName == "" || source == "" || idempotencyKey == "" {
		return "", fmt.Errorf("project id, agent name, source, and idempotency key are required")
	}
	if !isProjectStableIdentifier(agentName) {
		return "", fmt.Errorf("agent name %q is not a stable identifier", agentName)
	}
	return stableReadableID("run", agentName, projectID+"|"+agentName+"|"+source+"|"+idempotencyKey), nil
}

func StableReadableID(prefix, readable, seed string) string {
	return stableReadableID(prefix, readable, seed)
}

func NewProjectRecordFromSpec(spec *compose.NormalizedProjectSpec, sourcePath string) (ProjectRecord, error) {
	if spec == nil {
		return ProjectRecord{}, fmt.Errorf("project spec is required")
	}
	sourcePath = normalizeProjectSourcePath(sourcePath)
	projectID, err := StableProjectID(spec.Name, sourcePath)
	if err != nil {
		return ProjectRecord{}, err
	}
	specHash, err := spec.Hash()
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("hash project spec: %w", err)
	}
	sourceJSON, err := encodeProjectSourceJSON(sourcePath)
	if err != nil {
		return ProjectRecord{}, err
	}
	return ProjectRecord{
		ID:         projectID,
		Name:       strings.TrimSpace(spec.Name),
		SourcePath: sourcePath,
		SourceJSON: sourceJSON,
		SpecHash:   specHash,
	}, nil
}

func NewProjectAgentRecordFromSpec(projectID string, revision int64, agent compose.NormalizedAgentSpec) (ProjectAgentRecord, error) {
	managedAgentID, err := StableManagedAgentID(projectID, agent.Name)
	if err != nil {
		return ProjectAgentRecord{}, err
	}
	specJSON, err := marshalCanonicalProjectJSON(agent)
	if err != nil {
		return ProjectAgentRecord{}, fmt.Errorf("marshal project agent %s spec: %w", agent.Name, err)
	}
	driver := ""
	if agent.Driver != nil {
		driver = agent.Driver.Name
	}
	return ProjectAgentRecord{
		ProjectID:        strings.TrimSpace(projectID),
		AgentName:        strings.TrimSpace(agent.Name),
		ManagedAgentID:   managedAgentID,
		Revision:         revision,
		Provider:         strings.TrimSpace(agent.Provider),
		Model:            strings.TrimSpace(agent.Model),
		Image:            strings.TrimSpace(agent.Image),
		Driver:           strings.TrimSpace(driver),
		SchedulerEnabled: agent.Scheduler != nil && agent.Scheduler.Enabled,
		SpecJSON:         string(specJSON),
	}, nil
}

func NewProjectSchedulerRecordFromSpec(projectID string, revision int64, agent compose.NormalizedAgentSpec) (ProjectSchedulerRecord, bool, error) {
	if agent.Scheduler == nil {
		return ProjectSchedulerRecord{}, false, nil
	}
	schedulerID, err := StableProjectSchedulerID(projectID, agent.Name, "")
	if err != nil {
		return ProjectSchedulerRecord{}, false, err
	}
	loaderID, err := StableManagedLoaderID(projectID, agent.Name, "")
	if err != nil {
		return ProjectSchedulerRecord{}, false, err
	}
	specJSON, err := marshalCanonicalProjectJSON(agent.Scheduler)
	if err != nil {
		return ProjectSchedulerRecord{}, false, fmt.Errorf("marshal project scheduler %s spec: %w", agent.Name, err)
	}
	return ProjectSchedulerRecord{
		ProjectID:       strings.TrimSpace(projectID),
		SchedulerID:     schedulerID,
		AgentName:       strings.TrimSpace(agent.Name),
		ManagedLoaderID: loaderID,
		Revision:        revision,
		Enabled:         agent.Scheduler.Enabled,
		TriggerCount:    len(agent.Scheduler.Triggers),
		SpecJSON:        string(specJSON),
	}, true, nil
}

func (s *ConfigStore) UpsertProject(ctx context.Context, project ProjectRecord) (ProjectRecord, error) {
	project, err := normalizeProjectRecord(project)
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
			return ProjectRecord{}, fmt.Errorf("project %s not found", project.ID)
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

func (s *ConfigStore) SaveProjectRevision(ctx context.Context, revision ProjectRevisionRecord) (ProjectRevisionRecord, bool, error) {
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
	existing, err := scanProjectRevision(row.Scan)
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
		return ProjectRevisionRecord{}, false, fmt.Errorf("project %s not found", revision.ProjectID)
	}
	if err := tx.Commit(); err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("commit project revision tx: %w", err)
	}
	return revision, true, nil
}

func (s *ConfigStore) GetProject(ctx context.Context, projectID string) (ProjectRecord, error) {
	item, found, err := s.getProject(ctx, projectID, false)
	if err != nil {
		return ProjectRecord{}, err
	}
	if !found {
		return ProjectRecord{}, fmt.Errorf("project %s not found: %w", strings.TrimSpace(projectID), sql.ErrNoRows)
	}
	return item, nil
}

func (s *ConfigStore) ListProjects(ctx context.Context, options ProjectListOptions) (ProjectListResult, error) {
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
		item, err := scanProject(rows.Scan)
		if err != nil {
			return ProjectListResult{}, err
		}
		if !options.IncludeRemoved && !item.RemovedAt.IsZero() {
			continue
		}
		if query != "" && !projectMatchesQuery(item, query) {
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

func (s *ConfigStore) GetProjectRevision(ctx context.Context, projectID string, revision int64) (ProjectRevisionRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT project_id, revision, spec_hash, spec_json, created_at
		FROM project_revision WHERE project_id = ? AND revision = ?`, strings.TrimSpace(projectID), revision)
	item, err := scanProjectRevision(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectRevisionRecord{}, fmt.Errorf("project revision %s/%d not found: %w", strings.TrimSpace(projectID), revision, err)
		}
		return ProjectRevisionRecord{}, err
	}
	return item, nil
}

func (s *ConfigStore) UpsertProjectAgent(ctx context.Context, agent ProjectAgentRecord) (ProjectAgentRecord, error) {
	agent, err := normalizeProjectAgentRecord(agent)
	if err != nil {
		return ProjectAgentRecord{}, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE project_agent SET
		managed_agent_id = ?, revision = ?, provider = ?, model = ?, image = ?, driver = ?, scheduler_enabled = ?, spec_json = ?, updated_at = ?
		WHERE project_id = ? AND agent_name = ?`,
		agent.ManagedAgentID, agent.Revision, agent.Provider, agent.Model, agent.Image, agent.Driver, boolToInt(agent.SchedulerEnabled), agent.SpecJSON, now.Unix(),
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
		agent.ProjectID, agent.AgentName, agent.ManagedAgentID, agent.Revision, agent.Provider, agent.Model, agent.Image, agent.Driver, boolToInt(agent.SchedulerEnabled), agent.SpecJSON,
		agent.CreatedAt.Unix(), agent.UpdatedAt.Unix()); err != nil {
		return ProjectAgentRecord{}, fmt.Errorf("insert project agent %s/%s: %w", agent.ProjectID, agent.AgentName, err)
	}
	return s.GetProjectAgent(ctx, agent.ProjectID, agent.AgentName)
}

func (s *ConfigStore) GetProjectAgent(ctx context.Context, projectID, agentName string) (ProjectAgentRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT project_id, agent_name, managed_agent_id, revision, provider, model, image, driver, scheduler_enabled, spec_json, created_at, updated_at
		FROM project_agent WHERE project_id = ? AND agent_name = ?`, strings.TrimSpace(projectID), strings.TrimSpace(agentName))
	item, err := scanProjectAgent(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectAgentRecord{}, fmt.Errorf("project agent %s/%s not found: %w", strings.TrimSpace(projectID), strings.TrimSpace(agentName), err)
		}
		return ProjectAgentRecord{}, err
	}
	return item, nil
}

func (s *ConfigStore) ListProjectAgents(ctx context.Context, projectID string) ([]ProjectAgentRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, agent_name, managed_agent_id, revision, provider, model, image, driver, scheduler_enabled, spec_json, created_at, updated_at
		FROM project_agent WHERE project_id = ? ORDER BY agent_name ASC`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("query project agents %s: %w", strings.TrimSpace(projectID), err)
	}
	defer func() { _ = rows.Close() }()
	var items []ProjectAgentRecord
	for rows.Next() {
		item, err := scanProjectAgent(rows.Scan)
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

func (s *ConfigStore) UpsertProjectScheduler(ctx context.Context, scheduler ProjectSchedulerRecord) (ProjectSchedulerRecord, error) {
	scheduler, err := normalizeProjectSchedulerRecord(scheduler)
	if err != nil {
		return ProjectSchedulerRecord{}, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE project_scheduler SET
		agent_name = ?, managed_loader_id = ?, revision = ?, enabled = ?, trigger_count = ?, spec_json = ?, updated_at = ?
		WHERE project_id = ? AND scheduler_id = ?`,
		scheduler.AgentName, scheduler.ManagedLoaderID, scheduler.Revision, boolToInt(scheduler.Enabled), scheduler.TriggerCount, scheduler.SpecJSON, now.Unix(),
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
		scheduler.ProjectID, scheduler.SchedulerID, scheduler.AgentName, scheduler.ManagedLoaderID, scheduler.Revision, boolToInt(scheduler.Enabled), scheduler.TriggerCount, scheduler.SpecJSON,
		scheduler.CreatedAt.Unix(), scheduler.UpdatedAt.Unix()); err != nil {
		return ProjectSchedulerRecord{}, fmt.Errorf("insert project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
	}
	return s.GetProjectScheduler(ctx, scheduler.ProjectID, scheduler.SchedulerID)
}

func (s *ConfigStore) GetProjectScheduler(ctx context.Context, projectID, schedulerID string) (ProjectSchedulerRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT project_id, scheduler_id, agent_name, managed_loader_id, revision, enabled, trigger_count, spec_json, created_at, updated_at
		FROM project_scheduler WHERE project_id = ? AND scheduler_id = ?`, strings.TrimSpace(projectID), strings.TrimSpace(schedulerID))
	item, err := scanProjectScheduler(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectSchedulerRecord{}, fmt.Errorf("project scheduler %s/%s not found: %w", strings.TrimSpace(projectID), strings.TrimSpace(schedulerID), err)
		}
		return ProjectSchedulerRecord{}, err
	}
	return item, nil
}

func (s *ConfigStore) SetProjectSchedulerEnabled(ctx context.Context, projectID, schedulerID string, enabled bool) (ProjectSchedulerRecord, error) {
	projectID = strings.TrimSpace(projectID)
	schedulerID = strings.TrimSpace(schedulerID)
	if projectID == "" || schedulerID == "" {
		return ProjectSchedulerRecord{}, fmt.Errorf("project scheduler id is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE project_scheduler SET enabled = ?, updated_at = ? WHERE project_id = ? AND scheduler_id = ?`,
		boolToInt(enabled), time.Now().UTC().Unix(), projectID, schedulerID)
	if err != nil {
		return ProjectSchedulerRecord{}, fmt.Errorf("update project scheduler %s/%s enabled state: %w", projectID, schedulerID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return ProjectSchedulerRecord{}, fmt.Errorf("project scheduler %s/%s not found", projectID, schedulerID)
	}
	return s.GetProjectScheduler(ctx, projectID, schedulerID)
}

func (s *ConfigStore) ListProjectSchedulers(ctx context.Context, projectID string) ([]ProjectSchedulerRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, scheduler_id, agent_name, managed_loader_id, revision, enabled, trigger_count, spec_json, created_at, updated_at
		FROM project_scheduler WHERE project_id = ? ORDER BY agent_name ASC, scheduler_id ASC`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("query project schedulers %s: %w", strings.TrimSpace(projectID), err)
	}
	defer func() { _ = rows.Close() }()
	var items []ProjectSchedulerRecord
	for rows.Next() {
		item, err := scanProjectScheduler(rows.Scan)
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

func (s *ConfigStore) CreateProjectRun(ctx context.Context, run ProjectRunRecord) (ProjectRunRecord, error) {
	run, err := normalizeProjectRunRecord(run)
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
		nonZeroTimeUnixMilli(run.StartedAt), nonZeroTimeUnixMilli(run.CompletedAt), run.DurationMs, run.CreatedAt.Unix(), run.UpdatedAt.Unix()); err != nil {
		return ProjectRunRecord{}, fmt.Errorf("insert project run %s: %w", run.RunID, err)
	}
	return s.GetProjectRun(ctx, run.RunID)
}

func (s *ConfigStore) UpdateProjectRun(ctx context.Context, run ProjectRunRecord) (ProjectRunRecord, error) {
	run, err := normalizeProjectRunRecord(run)
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
		nonZeroTimeUnixMilli(run.StartedAt), nonZeroTimeUnixMilli(run.CompletedAt), run.DurationMs, now.Unix(), run.RunID)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("update project run %s: %w", run.RunID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return ProjectRunRecord{}, fmt.Errorf("project run %s not found", run.RunID)
	}
	return s.GetProjectRun(ctx, run.RunID)
}

func (s *ConfigStore) GetProjectRun(ctx context.Context, runID string) (ProjectRunRecord, error) {
	row := s.db.QueryRowContext(ctx, selectProjectRunSQL()+` WHERE run_id = ?`, strings.TrimSpace(runID))
	item, err := scanProjectRun(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectRunRecord{}, fmt.Errorf("project run %s not found: %w", strings.TrimSpace(runID), err)
		}
		return ProjectRunRecord{}, err
	}
	return item, nil
}

func (s *ConfigStore) ListProjectRuns(ctx context.Context, projectID string, limit int) ([]ProjectRunRecord, error) {
	return s.ListProjectRunsByOptions(ctx, ProjectRunListOptions{ProjectID: projectID, Limit: limit})
}

func (s *ConfigStore) ListProjectRunsByOptions(ctx context.Context, options ProjectRunListOptions) ([]ProjectRunRecord, error) {
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
		args = append(args, normalizeProjectRunStatus(status))
	}
	if source := strings.TrimSpace(options.Source); source != "" {
		where = append(where, "source = ?")
		args = append(args, normalizeProjectRunSource(source))
	}
	query := selectProjectRunSQL()
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
		item, err := scanProjectRun(rows.Scan)
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

func (s *ConfigStore) getProject(ctx context.Context, projectID string, includeRemoved bool) (ProjectRecord, bool, error) {
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
	item, err := scanProject(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectRecord{}, false, nil
		}
		return ProjectRecord{}, false, err
	}
	return item, true, nil
}

func (s *ConfigStore) GetProjectIfExists(ctx context.Context, projectID string, includeRemoved bool) (ProjectRecord, bool, error) {
	return s.getProject(ctx, projectID, includeRemoved)
}

func normalizeProjectRecord(project ProjectRecord) (ProjectRecord, error) {
	project.ID = strings.TrimSpace(project.ID)
	project.Name = strings.TrimSpace(project.Name)
	project.SourcePath = normalizeProjectSourcePath(project.SourcePath)
	project.SourceJSON = strings.TrimSpace(project.SourceJSON)
	project.SpecHash = strings.TrimSpace(project.SpecHash)
	if project.ID == "" {
		return ProjectRecord{}, fmt.Errorf("project id is required")
	}
	if project.Name == "" {
		return ProjectRecord{}, fmt.Errorf("project name is required")
	}
	if project.SourceJSON == "" {
		sourceJSON, err := encodeProjectSourceJSON(project.SourcePath)
		if err != nil {
			return ProjectRecord{}, err
		}
		project.SourceJSON = sourceJSON
	}
	if !json.Valid([]byte(project.SourceJSON)) {
		return ProjectRecord{}, fmt.Errorf("project source_json must be valid JSON")
	}
	if project.CurrentRevision < 0 {
		return ProjectRecord{}, fmt.Errorf("project current revision cannot be negative")
	}
	return project, nil
}

func normalizeProjectAgentRecord(agent ProjectAgentRecord) (ProjectAgentRecord, error) {
	agent.ProjectID = strings.TrimSpace(agent.ProjectID)
	agent.AgentName = strings.TrimSpace(agent.AgentName)
	agent.ManagedAgentID = strings.TrimSpace(agent.ManagedAgentID)
	agent.Provider = strings.TrimSpace(agent.Provider)
	agent.Model = strings.TrimSpace(agent.Model)
	agent.Image = strings.TrimSpace(agent.Image)
	agent.Driver = strings.TrimSpace(agent.Driver)
	agent.SpecJSON = strings.TrimSpace(agent.SpecJSON)
	if agent.ProjectID == "" || agent.AgentName == "" {
		return ProjectAgentRecord{}, fmt.Errorf("project id and agent name are required")
	}
	if agent.ManagedAgentID == "" {
		managedAgentID, err := StableManagedAgentID(agent.ProjectID, agent.AgentName)
		if err != nil {
			return ProjectAgentRecord{}, err
		}
		agent.ManagedAgentID = managedAgentID
	}
	if agent.Revision < 0 {
		return ProjectAgentRecord{}, fmt.Errorf("project agent revision cannot be negative")
	}
	if agent.SpecJSON == "" {
		agent.SpecJSON = "{}"
	}
	if !json.Valid([]byte(agent.SpecJSON)) {
		return ProjectAgentRecord{}, fmt.Errorf("project agent spec_json must be valid JSON")
	}
	return agent, nil
}

func normalizeProjectSchedulerRecord(scheduler ProjectSchedulerRecord) (ProjectSchedulerRecord, error) {
	scheduler.ProjectID = strings.TrimSpace(scheduler.ProjectID)
	scheduler.SchedulerID = strings.TrimSpace(scheduler.SchedulerID)
	scheduler.AgentName = strings.TrimSpace(scheduler.AgentName)
	scheduler.ManagedLoaderID = strings.TrimSpace(scheduler.ManagedLoaderID)
	scheduler.SpecJSON = strings.TrimSpace(scheduler.SpecJSON)
	if scheduler.ProjectID == "" || scheduler.AgentName == "" {
		return ProjectSchedulerRecord{}, fmt.Errorf("project id and agent name are required")
	}
	if scheduler.SchedulerID == "" {
		schedulerID, err := StableProjectSchedulerID(scheduler.ProjectID, scheduler.AgentName, "")
		if err != nil {
			return ProjectSchedulerRecord{}, err
		}
		scheduler.SchedulerID = schedulerID
	}
	if scheduler.ManagedLoaderID == "" {
		loaderID, err := StableManagedLoaderID(scheduler.ProjectID, scheduler.AgentName, "")
		if err != nil {
			return ProjectSchedulerRecord{}, err
		}
		scheduler.ManagedLoaderID = loaderID
	}
	if scheduler.Revision < 0 {
		return ProjectSchedulerRecord{}, fmt.Errorf("project scheduler revision cannot be negative")
	}
	if scheduler.TriggerCount < 0 {
		return ProjectSchedulerRecord{}, fmt.Errorf("project scheduler trigger count cannot be negative")
	}
	if scheduler.SpecJSON == "" {
		scheduler.SpecJSON = "{}"
	}
	if !json.Valid([]byte(scheduler.SpecJSON)) {
		return ProjectSchedulerRecord{}, fmt.Errorf("project scheduler spec_json must be valid JSON")
	}
	return scheduler, nil
}

func normalizeProjectRunRecord(run ProjectRunRecord) (ProjectRunRecord, error) {
	run.RunID = strings.TrimSpace(run.RunID)
	run.ProjectID = strings.TrimSpace(run.ProjectID)
	run.ProjectName = strings.TrimSpace(run.ProjectName)
	run.AgentName = strings.TrimSpace(run.AgentName)
	run.ManagedAgentID = strings.TrimSpace(run.ManagedAgentID)
	run.Source = strings.TrimSpace(run.Source)
	run.SchedulerID = strings.TrimSpace(run.SchedulerID)
	run.TriggerID = strings.TrimSpace(run.TriggerID)
	run.Status = normalizeProjectRunStatus(run.Status)
	run.SessionID = strings.TrimSpace(run.SessionID)
	run.ResultJSON = strings.TrimSpace(run.ResultJSON)
	run.LogsPath = strings.TrimSpace(run.LogsPath)
	run.ArtifactsDir = strings.TrimSpace(run.ArtifactsDir)
	run.Driver = strings.TrimSpace(run.Driver)
	run.ImageRef = strings.TrimSpace(run.ImageRef)
	if run.RunID == "" || run.ProjectID == "" {
		return ProjectRunRecord{}, fmt.Errorf("project run id and project id are required")
	}
	if run.ProjectRevision < 0 {
		return ProjectRunRecord{}, fmt.Errorf("project run revision cannot be negative")
	}
	if run.ResultJSON == "" {
		run.ResultJSON = "{}"
	}
	if !json.Valid([]byte(run.ResultJSON)) {
		return ProjectRunRecord{}, fmt.Errorf("project run result_json must be valid JSON")
	}
	return run, nil
}

func normalizeProjectRunStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case ProjectRunStatusPending:
		return ProjectRunStatusPending
	case ProjectRunStatusRunning:
		return ProjectRunStatusRunning
	case ProjectRunStatusSucceeded:
		return ProjectRunStatusSucceeded
	case ProjectRunStatusFailed:
		return ProjectRunStatusFailed
	case ProjectRunStatusCanceled:
		return ProjectRunStatusCanceled
	default:
		return ProjectRunStatusPending
	}
}

func NormalizeProjectRunStatus(status string) string {
	return normalizeProjectRunStatus(status)
}

func scanProject(scan func(dest ...any) error) (ProjectRecord, error) {
	var item ProjectRecord
	var createdAtRaw any
	var updatedAtRaw any
	var removedAtRaw any
	if err := scan(&item.ID, &item.Name, &item.SourcePath, &item.SourceJSON, &item.CurrentRevision, &item.SpecHash, &createdAtRaw, &updatedAtRaw, &removedAtRaw); err != nil {
		return ProjectRecord{}, fmt.Errorf("scan project: %w", err)
	}
	item.CreatedAt = parseStoredTime(createdAtRaw)
	item.UpdatedAt = parseStoredTime(updatedAtRaw)
	item.RemovedAt = parseStoredTime(removedAtRaw)
	return item, nil
}

func scanProjectRevision(scan func(dest ...any) error) (ProjectRevisionRecord, error) {
	var item ProjectRevisionRecord
	var createdAtRaw any
	if err := scan(&item.ProjectID, &item.Revision, &item.SpecHash, &item.SpecJSON, &createdAtRaw); err != nil {
		return ProjectRevisionRecord{}, fmt.Errorf("scan project revision: %w", err)
	}
	item.CreatedAt = parseStoredTime(createdAtRaw)
	return item, nil
}

func scanProjectAgent(scan func(dest ...any) error) (ProjectAgentRecord, error) {
	var item ProjectAgentRecord
	var schedulerEnabled int
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.ProjectID, &item.AgentName, &item.ManagedAgentID, &item.Revision, &item.Provider, &item.Model, &item.Image, &item.Driver, &schedulerEnabled, &item.SpecJSON, &createdAtRaw, &updatedAtRaw); err != nil {
		return ProjectAgentRecord{}, fmt.Errorf("scan project agent: %w", err)
	}
	item.SchedulerEnabled = schedulerEnabled != 0
	item.CreatedAt = parseStoredTime(createdAtRaw)
	item.UpdatedAt = parseStoredTime(updatedAtRaw)
	return item, nil
}

func scanProjectScheduler(scan func(dest ...any) error) (ProjectSchedulerRecord, error) {
	var item ProjectSchedulerRecord
	var enabled int
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.ProjectID, &item.SchedulerID, &item.AgentName, &item.ManagedLoaderID, &item.Revision, &enabled, &item.TriggerCount, &item.SpecJSON, &createdAtRaw, &updatedAtRaw); err != nil {
		return ProjectSchedulerRecord{}, fmt.Errorf("scan project scheduler: %w", err)
	}
	item.Enabled = enabled != 0
	item.CreatedAt = parseStoredTime(createdAtRaw)
	item.UpdatedAt = parseStoredTime(updatedAtRaw)
	return item, nil
}

func scanProjectRun(scan func(dest ...any) error) (ProjectRunRecord, error) {
	var item ProjectRunRecord
	var startedAtRaw any
	var completedAtRaw any
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(
		&item.RunID, &item.ProjectID, &item.ProjectName, &item.ProjectRevision, &item.AgentName, &item.ManagedAgentID, &item.Source, &item.SchedulerID, &item.TriggerID, &item.Status,
		&item.SessionID, &item.ExitCode, &item.Error, &item.Prompt, &item.Output, &item.ResultJSON, &item.LogsPath, &item.ArtifactsDir, &item.CleanupError, &item.Driver, &item.ImageRef,
		&startedAtRaw, &completedAtRaw, &item.DurationMs, &createdAtRaw, &updatedAtRaw,
	); err != nil {
		return ProjectRunRecord{}, fmt.Errorf("scan project run: %w", err)
	}
	item.StartedAt = parseStoredUnixTimeAuto(asInt64Time(startedAtRaw))
	item.CompletedAt = parseStoredUnixTimeAuto(asInt64Time(completedAtRaw))
	item.CreatedAt = parseStoredTime(createdAtRaw)
	item.UpdatedAt = parseStoredTime(updatedAtRaw)
	return item, nil
}

func selectProjectRunSQL() string {
	return `SELECT run_id, project_id, project_name, project_revision, agent_name, managed_agent_id, source, scheduler_id, trigger_id, status,
		session_id, exit_code, error, prompt, output, result_json, logs_path, artifacts_dir, cleanup_error, driver, image_ref,
		started_at, completed_at, duration_ms, created_at, updated_at FROM project_run`
}

func projectMatchesQuery(item ProjectRecord, query string) bool {
	return strings.Contains(strings.ToLower(item.ID), query) ||
		strings.Contains(strings.ToLower(item.Name), query) ||
		strings.Contains(strings.ToLower(item.SourcePath), query)
}

func normalizeProjectSourcePath(sourcePath string) string {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return ""
	}
	if abs, err := filepath.Abs(sourcePath); err == nil {
		sourcePath = abs
	}
	return filepath.Clean(sourcePath)
}

func encodeProjectSourceJSON(sourcePath string) (string, error) {
	data, err := json.Marshal(struct {
		ComposePath string `json:"compose_path,omitempty"`
	}{ComposePath: normalizeProjectSourcePath(sourcePath)})
	if err != nil {
		return "", fmt.Errorf("marshal project source: %w", err)
	}
	return string(data), nil
}

func marshalCanonicalProjectJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func stableReadableID(prefix, readable, seed string) string {
	readable = strings.ToLower(strings.TrimSpace(readable))
	var b strings.Builder
	for _, r := range readable {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	readable = strings.Trim(b.String(), "-_")
	if readable == "" {
		readable = "item"
	}
	if len(readable) > 48 {
		readable = strings.Trim(readable[:48], "-_")
	}
	sum := sha256.Sum256([]byte(seed))
	return prefix + "-" + readable + "-" + hex.EncodeToString(sum[:6])
}

func isProjectStableIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for i, r := range value {
		switch {
		case i == 0 && r >= 'a' && r <= 'z':
		case i > 0 && r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		case i > 0 && (r == '-' || r == '_'):
		default:
			return false
		}
	}
	return true
}

func asInt64Time(value any) int64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case []byte:
		return asInt64Time(string(typed))
	case string:
		parsed, _ := parseInt64String(typed)
		return parsed
	default:
		return 0
	}
}

func parseInt64String(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	var parsed int64
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
		parsed = parsed*10 + int64(r-'0')
	}
	return parsed, true
}
