package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/project"
	"github.com/chaitin/agent-compose/ent/dao/projectagent"
	"github.com/chaitin/agent-compose/ent/dao/projectrevision"
	"github.com/chaitin/agent-compose/ent/dao/projectrun"
	"github.com/chaitin/agent-compose/ent/dao/projectscheduler"
	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/identity"
	"github.com/chaitin/agent-compose/pkg/idset"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/dialect"
	"github.com/chaitin/agent-compose/pkg/storedtime"
)

// projectStore owns projects, revisions, agents, schedulers, runs, and sandboxes.
type projectStore struct {
	db      *sql.DB
	dialect dialect.Dialect
	client  *dao.Client
}

func (s *projectStore) UpsertProject(ctx context.Context, project ProjectRecord) (ProjectRecord, error) {
	project, err := projects.NormalizeRecord(project)
	if err != nil {
		return ProjectRecord{}, err
	}
	now := time.Now().UTC()
	project.CreatedAt = now
	project.UpdatedAt = now
	// Kept as SQL: the ON CONFLICT update keeps the stored spec_hash when the
	// incoming hash is empty, a CASE over the existing row that ent's upsert
	// setters cannot express without changing that behaviour.
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
	affected, err := s.client.Project.Update().
		Where(project.IDEQ(projectID), project.RemovedAtEQ(0)).
		SetUpdatedAt(now.Unix()).
		SetRemovedAt(now.Unix()).
		Save(ctx)
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("mark project %s removed: %w", projectID, err)
	}
	if affected == 0 {
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
	// Kept as SQL: the revision allocator reserves SQLite's write lock with
	// BEGIN IMMEDIATE (and locks the project row on PostgreSQL) before reading
	// current_revision and MAX(revision); an ent transaction begins deferred and
	// cannot keep these statements on this explicitly reserved connection.
	if _, err := conn.ExecContext(ctx, s.dialect.BeginWriteStatement()); err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("begin project revision tx: %w", err)
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
	// Kept as SQL: reads the row locked by the write reservation above.
	if err := conn.QueryRowContext(ctx, `SELECT current_revision FROM project WHERE id = ?`+s.dialect.ForUpdateClause(), revision.ProjectID).Scan(&currentRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectRevisionRecord{}, false, domain.ResourceError(domain.ErrNotFound, "project", revision.ProjectID, fmt.Sprintf("project %s not found", revision.ProjectID), err)
		}
		return ProjectRevisionRecord{}, false, fmt.Errorf("query current project revision %s: %w", revision.ProjectID, err)
	}
	if currentRevision > 0 {
		// Kept as SQL: part of the same explicitly reserved transaction.
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
	// Kept as SQL: COALESCE(MAX(revision), 0) + 1 must run under the same write
	// reservation as the insert, which an ent aggregate cannot join.
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) + 1 FROM project_revision WHERE project_id = ?`, revision.ProjectID).Scan(&nextRevision); err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("query next project revision %s: %w", revision.ProjectID, err)
	}
	now := time.Now().UTC()
	revision.Revision = nextRevision
	revision.CreatedAt = now
	// Kept as SQL: inserts inside the explicitly reserved transaction.
	if _, err := conn.ExecContext(ctx, `INSERT INTO project_revision(project_id, revision, spec_hash, spec_json, created_at)
		VALUES(?, ?, ?, ?, ?)`, revision.ProjectID, revision.Revision, revision.SpecHash, revision.SpecJSON, revision.CreatedAt.Unix()); err != nil {
		return ProjectRevisionRecord{}, false, fmt.Errorf("insert project revision %s/%d: %w", revision.ProjectID, revision.Revision, err)
	}
	// Kept as SQL: updates the project row in the same explicitly reserved
	// transaction as the insert above.
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
	projectID = strings.TrimSpace(projectID)
	entity, err := s.client.ProjectRevision.Query().
		Where(projectrevision.IDEQ(projectID), projectrevision.RevisionEQ(revision)).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			id := fmt.Sprintf("%s/%d", projectID, revision)
			return ProjectRevisionRecord{}, domain.ResourceError(domain.ErrNotFound, "project revision", id, fmt.Sprintf("project revision %s not found", id), scanNotFound("project revision"))
		}
		return ProjectRevisionRecord{}, err
	}
	return projectRevisionFromEntity(entity), nil
}

func (s *projectStore) UpsertProjectAgent(ctx context.Context, agent ProjectAgentRecord) (ProjectAgentRecord, error) {
	agent, err := projects.NormalizeAgentRecord(agent)
	if err != nil {
		return ProjectAgentRecord{}, err
	}
	now := time.Now().UTC()
	affected, err := s.client.ProjectAgent.Update().
		Where(projectagent.ProjectIDEQ(agent.ProjectID), projectagent.AgentNameEQ(agent.AgentName)).
		SetName(agent.Name).
		SetShortID(agent.ShortID).
		SetRevision(agent.Revision).
		SetProvider(agent.Provider).
		SetModel(agent.Model).
		SetImage(agent.Image).
		SetDriver(agent.Driver).
		SetSchedulerEnabled(BoolToInt(agent.SchedulerEnabled)).
		SetSpecJSON(agent.SpecJSON).
		SetUpdatedAt(now.Unix()).
		// The released UPDATE also rewrote the physical id, which ent's update
		// builder treats as immutable; set it explicitly to keep that behaviour.
		Modify(func(u *entsql.UpdateBuilder) {
			u.Set(projectagent.FieldID, agent.ID)
		}).
		Save(ctx)
	if err != nil {
		return ProjectAgentRecord{}, fmt.Errorf("update project agent %s/%s: %w", agent.ProjectID, agent.AgentName, err)
	}
	if affected > 0 {
		return s.GetProjectAgent(ctx, agent.ProjectID, agent.AgentName)
	}
	agent.CreatedAt = now
	agent.UpdatedAt = now
	if _, err := s.client.ProjectAgent.Create().
		SetID(agent.ID).
		SetName(agent.Name).
		SetShortID(agent.ShortID).
		SetProjectID(agent.ProjectID).
		SetAgentName(agent.AgentName).
		SetRevision(agent.Revision).
		SetProvider(agent.Provider).
		SetModel(agent.Model).
		SetImage(agent.Image).
		SetDriver(agent.Driver).
		SetSchedulerEnabled(BoolToInt(agent.SchedulerEnabled)).
		SetSpecJSON(agent.SpecJSON).
		SetCreatedAt(agent.CreatedAt.Unix()).
		SetUpdatedAt(agent.UpdatedAt.Unix()).
		Save(ctx); err != nil {
		return ProjectAgentRecord{}, fmt.Errorf("insert project agent %s/%s: %w", agent.ProjectID, agent.AgentName, err)
	}
	return s.GetProjectAgent(ctx, agent.ProjectID, agent.AgentName)
}

func (s *projectStore) GetProjectAgent(ctx context.Context, projectID, agentName string) (ProjectAgentRecord, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	entity, err := s.client.ProjectAgent.Query().
		Where(projectagent.ProjectIDEQ(projectID), projectagent.AgentNameEQ(agentName)).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			id := projectID + "/" + agentName
			return ProjectAgentRecord{}, domain.ResourceError(domain.ErrNotFound, "project agent", id, fmt.Sprintf("project agent %s not found", id), scanNotFound("project agent"))
		}
		return ProjectAgentRecord{}, err
	}
	return projectAgentFromEntity(entity), nil
}

func (s *projectStore) ListProjectAgents(ctx context.Context, projectID string) ([]ProjectAgentRecord, error) {
	projectID = strings.TrimSpace(projectID)
	entities, err := s.client.ProjectAgent.Query().
		Where(projectagent.ProjectIDEQ(projectID)).
		Order(projectagent.ByAgentName()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query project agents %s: %w", projectID, err)
	}
	var items []ProjectAgentRecord
	for _, entity := range entities {
		items = append(items, projectAgentFromEntity(entity))
	}
	return items, nil
}

func (s *projectStore) ListProjectAgentsByIDs(ctx context.Context, agentIDs []string) (map[string]ProjectAgentRecord, error) {
	ids := idset.Normalize(agentIDs)
	if len(ids) == 0 {
		return map[string]ProjectAgentRecord{}, nil
	}
	result := make(map[string]ProjectAgentRecord, len(ids))
	if err := forEachIDBatch(ids, func(batch []string) error {
		entities, err := s.client.ProjectAgent.Query().
			Where(projectagent.IDIn(batch...)).
			Order(
				projectagent.ByUpdatedAt(entsql.OrderDesc()),
				projectagent.ByProjectID(),
				projectagent.ByAgentName(),
			).
			All(ctx)
		if err != nil {
			return fmt.Errorf("query project agents by managed agent ids: %w", err)
		}
		for _, entity := range entities {
			item := projectAgentFromEntity(entity)
			if _, exists := result[item.ID]; !exists {
				result[item.ID] = item
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *projectStore) UpsertProjectScheduler(ctx context.Context, scheduler ProjectSchedulerRecord) (ProjectSchedulerRecord, error) {
	scheduler, err := projects.NormalizeSchedulerRecord(scheduler)
	if err != nil {
		return ProjectSchedulerRecord{}, err
	}
	now := time.Now().UTC()
	affected, err := s.client.ProjectScheduler.Update().
		Where(projectscheduler.ProjectIDEQ(scheduler.ProjectID), projectscheduler.IDEQ(scheduler.ID)).
		SetSchedulerID(scheduler.SchedulerID).
		SetShortID(scheduler.ShortID).
		SetAgentName(scheduler.AgentName).
		SetRevision(scheduler.Revision).
		SetEnabled(BoolToInt(scheduler.Enabled)).
		SetTriggerCount(scheduler.TriggerCount).
		SetSpecJSON(scheduler.SpecJSON).
		SetUpdatedAt(now.Unix()).
		Save(ctx)
	if err != nil {
		return ProjectSchedulerRecord{}, fmt.Errorf("update project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
	}
	if affected > 0 {
		return s.GetProjectScheduler(ctx, scheduler.ProjectID, scheduler.SchedulerID)
	}
	scheduler.CreatedAt = now
	scheduler.UpdatedAt = now
	if _, err := s.client.ProjectScheduler.Create().
		SetID(scheduler.ID).
		SetSchedulerID(scheduler.ID).
		SetShortID(scheduler.ShortID).
		SetProjectID(scheduler.ProjectID).
		SetAgentName(scheduler.AgentName).
		SetRevision(scheduler.Revision).
		SetEnabled(BoolToInt(scheduler.Enabled)).
		SetTriggerCount(scheduler.TriggerCount).
		SetSpecJSON(scheduler.SpecJSON).
		SetLastError(scheduler.LastError).
		SetCreatedAt(scheduler.CreatedAt.Unix()).
		SetUpdatedAt(scheduler.UpdatedAt.Unix()).
		Save(ctx); err != nil {
		return ProjectSchedulerRecord{}, fmt.Errorf("insert project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
	}
	return s.GetProjectScheduler(ctx, scheduler.ProjectID, scheduler.SchedulerID)
}

func (s *projectStore) GetProjectScheduler(ctx context.Context, projectID, schedulerID string) (ProjectSchedulerRecord, error) {
	projectID = strings.TrimSpace(projectID)
	schedulerID = strings.TrimSpace(schedulerID)
	entity, err := s.client.ProjectScheduler.Query().
		Where(projectscheduler.ProjectIDEQ(projectID), projectscheduler.IDEQ(schedulerID)).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			id := projectID + "/" + schedulerID
			return ProjectSchedulerRecord{}, domain.ResourceError(domain.ErrNotFound, "project scheduler", id, fmt.Sprintf("project scheduler %s not found", id), scanNotFound("project scheduler"))
		}
		return ProjectSchedulerRecord{}, err
	}
	return projectSchedulerFromEntity(entity), nil
}

func (s *projectStore) SetProjectSchedulerEnabled(ctx context.Context, projectID, schedulerID string, enabled bool) (ProjectSchedulerRecord, error) {
	projectID = strings.TrimSpace(projectID)
	schedulerID = strings.TrimSpace(schedulerID)
	if projectID == "" || schedulerID == "" {
		return ProjectSchedulerRecord{}, fmt.Errorf("project scheduler id is required")
	}
	affected, err := s.client.ProjectScheduler.Update().
		Where(projectscheduler.ProjectIDEQ(projectID), projectscheduler.IDEQ(schedulerID)).
		SetEnabled(BoolToInt(enabled)).
		SetUpdatedAt(time.Now().UTC().Unix()).
		Save(ctx)
	if err != nil {
		return ProjectSchedulerRecord{}, fmt.Errorf("update project scheduler %s/%s enabled state: %w", projectID, schedulerID, err)
	}
	if affected == 0 {
		id := projectID + "/" + schedulerID
		return ProjectSchedulerRecord{}, domain.ResourceError(domain.ErrNotFound, "project scheduler", id, fmt.Sprintf("project scheduler %s not found", id), nil)
	}
	return s.GetProjectScheduler(ctx, projectID, schedulerID)
}

func (s *projectStore) ListProjectSchedulers(ctx context.Context, projectID string) ([]ProjectSchedulerRecord, error) {
	projectID = strings.TrimSpace(projectID)
	entities, err := s.client.ProjectScheduler.Query().
		Where(projectscheduler.ProjectIDEQ(projectID)).
		Order(projectscheduler.ByAgentName(), projectscheduler.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query project schedulers %s: %w", projectID, err)
	}
	var items []ProjectSchedulerRecord
	for _, entity := range entities {
		items = append(items, projectSchedulerFromEntity(entity))
	}
	return items, nil
}

func (s *projectStore) ListProjectSchedulersPage(ctx context.Context, query string, offset, limit int) ([]ProjectSchedulerRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	query = strings.ToLower(strings.TrimSpace(query))
	likeQuery := "%" + query + "%"
	// Kept as SQL: joins project and aggregates scheduler_run counts with a CTE
	// and a LEFT JOIN, which ent cannot express without splitting the snapshot.
	rows, err := s.db.QueryContext(ctx, `WITH page AS (
		SELECT s.id, s.short_id, s.project_id, s.agent_name, s.revision, s.enabled, s.trigger_count, s.spec_json, s.created_at, s.updated_at, s.last_error
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
	// Kept as SQL: COUNT over a join between project_scheduler and project.
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
	if err := validateHistoryArtifactPaths(nil, run.LogsPath, run.ArtifactsDir); err != nil {
		return ProjectRunRecord{}, err
	}
	now := time.Now().UTC()
	run.CreatedAt = now
	run.UpdatedAt = now
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("begin create project run transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if _, err := client.ProjectRun.Create().
		SetID(run.RunID).
		SetProjectID(run.ProjectID).
		SetProjectName(run.ProjectName).
		SetProjectRevision(run.ProjectRevision).
		SetAgentName(run.AgentName).
		SetAgentID(run.AgentID).
		SetSource(run.Source).
		SetSchedulerID(run.SchedulerID).
		SetNillableSchedulerRunID(schedulerRunIDPtr(run.SchedulerRunID)).
		SetTriggerID(run.TriggerID).
		SetStatus(run.Status).
		SetSandboxID(run.SandboxID).
		SetExitCode(run.ExitCode).
		SetError(run.Error).
		SetErrorStack(run.ErrorStack).
		SetPrompt(run.Prompt).
		SetOutput(run.Output).
		SetResultJSON(run.ResultJSON).
		SetLogsPath(run.LogsPath).
		SetArtifactsDir(run.ArtifactsDir).
		SetCleanupError(run.CleanupError).
		SetCleanupPolicy(run.CleanupPolicy).
		SetSandboxCreated(BoolToInt(run.SandboxCreated)).
		SetDriver(run.Driver).
		SetImageRef(run.ImageRef).
		SetStartedAt(domain.NonZeroTimeUnixMilli(run.StartedAt)).
		SetCompletedAt(domain.NonZeroTimeUnixMilli(run.CompletedAt)).
		SetDurationMs(run.DurationMs).
		SetCreatedAt(run.CreatedAt.Unix()).
		SetUpdatedAt(run.UpdatedAt.Unix()).
		SetExecutionID(run.ExecutionID).
		SetDetach(BoolToInt(run.Detach)).
		SetDeadline(domain.NonZeroTimeUnixMilli(run.Deadline)).
		SetDetachKind(run.DetachKind).
		Save(ctx); err != nil {
		return ProjectRunRecord{}, fmt.Errorf("insert project run %s: %w", run.RunID, err)
	}
	if err := insertProjectRunLabels(ctx, client, run.RunID, run.Labels); err != nil {
		return ProjectRunRecord{}, err
	}
	stored, err := getProjectRunByClient(ctx, client, run.RunID)
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
	if err := validateHistoryArtifactPaths(nil, run.LogsPath, run.ArtifactsDir); err != nil {
		return ProjectRunRecord{}, err
	}
	now := time.Now().UTC()
	update := s.client.ProjectRun.Update().
		Where(projectrun.IDEQ(run.RunID)).
		SetProjectID(run.ProjectID).
		SetProjectName(run.ProjectName).
		SetProjectRevision(run.ProjectRevision).
		SetAgentName(run.AgentName).
		SetAgentID(run.AgentID).
		SetSource(run.Source).
		SetSchedulerID(run.SchedulerID).
		SetTriggerID(run.TriggerID).
		SetStatus(run.Status).
		SetSandboxID(run.SandboxID).
		SetExitCode(run.ExitCode).
		SetError(run.Error).
		SetErrorStack(run.ErrorStack).
		SetPrompt(run.Prompt).
		SetOutput(run.Output).
		SetResultJSON(run.ResultJSON).
		SetLogsPath(run.LogsPath).
		SetArtifactsDir(run.ArtifactsDir).
		SetCleanupError(run.CleanupError).
		SetCleanupPolicy(run.CleanupPolicy).
		SetSandboxCreated(BoolToInt(run.SandboxCreated)).
		SetDriver(run.Driver).
		SetImageRef(run.ImageRef).
		SetStartedAt(domain.NonZeroTimeUnixMilli(run.StartedAt)).
		SetCompletedAt(domain.NonZeroTimeUnixMilli(run.CompletedAt)).
		SetDurationMs(run.DurationMs).
		SetUpdatedAt(now.Unix()).
		SetExecutionID(run.ExecutionID).
		SetDetach(BoolToInt(run.Detach)).
		SetDeadline(domain.NonZeroTimeUnixMilli(run.Deadline)).
		SetDetachKind(run.DetachKind)
	if ptr := schedulerRunIDPtr(run.SchedulerRunID); ptr != nil {
		update.SetSchedulerRunID(*ptr)
	} else {
		update.ClearSchedulerRunID()
	}
	affected, err := update.Save(ctx)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("update project run %s: %w", run.RunID, err)
	}
	if affected == 0 {
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

// schedulerRunIDPtr is the ent form of nullableSchedulerRunID: nil leaves the
// nillable scheduler_run_id column unset on insert, or clears it on update.
func schedulerRunIDPtr(id string) *string {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	return &id
}

func (s *projectStore) GetProjectRun(ctx context.Context, runID string) (ProjectRunRecord, error) {
	runID = strings.TrimSpace(runID)
	item, err := getProjectRunByClient(ctx, s.client, runID)
	if err != nil {
		if dao.IsNotFound(err) {
			return ProjectRunRecord{}, domain.ResourceError(domain.ErrNotFound, "project run", runID, fmt.Sprintf("project run %s not found", runID), scanNotFound("project run"))
		}
		return ProjectRunRecord{}, err
	}
	return item, nil
}

// getProjectRunByClient reads one run and its labels through an ent client,
// which may be bound to an open transaction.
func getProjectRunByClient(ctx context.Context, client *dao.Client, runID string) (ProjectRunRecord, error) {
	entity, err := client.ProjectRun.Query().Where(projectrun.IDEQ(runID)).Only(ctx)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("scan project run: %w", err)
	}
	item := projectRunFromEntity(entity)
	labels, err := loadProjectRunLabelsByClient(ctx, client, item.RunID)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	item.Labels = labels
	return item, nil
}

func (s *projectStore) ListProjectRuns(ctx context.Context, projectID string, limit int) ([]ProjectRunRecord, error) {
	result, err := s.ListProjectRunsByOptions(ctx, ProjectRunListOptions{ProjectID: projectID, Limit: limit})
	return result.Runs, err
}

func (s *projectStore) ListProjectRunsByOptions(ctx context.Context, options ProjectRunListOptions) (domain.ProjectRunListResult, error) {
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
	where, args, eventScopeTruncated, err := s.projectRunFilterWithEventScope(ctx, options)
	if err != nil {
		return domain.ProjectRunListResult{}, err
	}
	query := projects.SelectProjectRunSQL()
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY created_at DESC, run_id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	// Kept as SQL: the filter assembly (project_run_filter.go and the event
	// scope) emits SQL fragments, including a correlated EXISTS over
	// project_run_label; ent cannot accept those without rewriting the shared
	// filter builder.
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.ProjectRunListResult{}, fmt.Errorf("query project runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var items []ProjectRunRecord
	for rows.Next() {
		item, err := projects.ScanProjectRun(rows.Scan)
		if err != nil {
			return domain.ProjectRunListResult{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.ProjectRunListResult{}, fmt.Errorf("iterate project runs: %w", err)
	}
	return domain.ProjectRunListResult{Runs: items, EventScopeTruncated: eventScopeTruncated}, nil
}

func (s *projectStore) CountProjectRuns(ctx context.Context, options ProjectRunListOptions) (int, bool, error) {
	where, args, eventScopeTruncated, err := s.projectRunFilterWithEventScope(ctx, options)
	if err != nil {
		return 0, false, err
	}
	query := `SELECT COUNT(*) FROM project_run`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	var total int
	// Kept as SQL: COUNT aggregate over the shared dynamic filter above.
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, false, fmt.Errorf("count project runs: %w", err)
	}
	return total, eventScopeTruncated, nil
}

// projectRunFilterWithEventScope extends the static run filter with the
// scheduler-run set recorded against options.EventID. The event scope is
// resolved fresh on every call; deliveries written between a list and its
// matching count can skew totals, the same way concurrent runs already can.
// An event without recorded deliveries filters to an empty result rather than
// no filter. eventScopeTruncated reports that the event scope hit its cap, so
// runs recorded only against events beyond it are missing from the result.
//
// The derived IN list is bounded only indirectly, through MaxEventScopeEvents
// in eventRunScope, so each list/count pair re-resolves the scope and
// carries one bound parameter per scheduler run. Rewriting this as an
// EXISTS/JOIN subquery over event_delivery would remove both the parameter
// fan-out and the duplicate resolution, at the cost of restructuring the
// filter assembly; revisit if event-scoped listing shows up in query profiles.
func (s *projectStore) projectRunFilterWithEventScope(ctx context.Context, options ProjectRunListOptions) ([]string, []any, bool, error) {
	where, args := projectRunFilter(options)
	eventID := strings.TrimSpace(options.EventID)
	if eventID == "" {
		return where, args, false, nil
	}
	schedulerRunIDs, eventScopeTruncated, err := eventRunScope(ctx, s.db, eventID)
	if err != nil {
		return nil, nil, false, err
	}
	if len(schedulerRunIDs) == 0 {
		where = append(where, "0 = 1")
		return where, args, eventScopeTruncated, nil
	}
	clause := "scheduler_run_id IN (" + placeholders(len(schedulerRunIDs)) + ")"
	for _, id := range schedulerRunIDs {
		args = append(args, id)
	}
	where = append(where, clause)
	return where, args, eventScopeTruncated, nil
}

func (s *projectStore) getProject(ctx context.Context, projectID string, includeRemoved bool) (ProjectRecord, bool, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ProjectRecord{}, false, fmt.Errorf("project id is required")
	}
	query := s.client.Project.Query().Where(project.IDEQ(projectID))
	if !includeRemoved {
		query.Where(project.RemovedAtEQ(0))
	}
	entity, err := query.Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return ProjectRecord{}, false, nil
		}
		return ProjectRecord{}, false, err
	}
	return projectFromEntity(entity), true, nil
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

// scanNotFound reproduces the scanner error text the released SQL readers
// wrapped around sql.ErrNoRows, so classified resource errors keep the same
// error string.
func scanNotFound(entity string) error {
	return fmt.Errorf("scan %s: %w", entity, sql.ErrNoRows)
}

// projectFromEntity maps a project row to the domain record, filling the
// derived short id exactly like projects.ScanProject.
func projectFromEntity(entity *dao.Project) ProjectRecord {
	item := ProjectRecord{
		ID:              entity.ID,
		Name:            entity.Name,
		ShortID:         entity.ShortID,
		SourcePath:      entity.SourcePath,
		SourceJSON:      entity.SourceJSON,
		CurrentRevision: entity.CurrentRevision,
		SpecHash:        entity.SpecHash,
		CreatedAt:       storedtime.ParseStoredTime(entity.CreatedAt),
		UpdatedAt:       storedtime.ParseStoredTime(entity.UpdatedAt),
		RemovedAt:       storedtime.ParseStoredTime(entity.RemovedAt),
	}
	if item.ShortID == "" {
		item.ShortID = identity.ShortID(item.ID)
	}
	return item
}

func projectRevisionFromEntity(entity *dao.ProjectRevision) ProjectRevisionRecord {
	return ProjectRevisionRecord{
		ProjectID: entity.ID,
		Revision:  entity.Revision,
		SpecHash:  entity.SpecHash,
		SpecJSON:  entity.SpecJSON,
		CreatedAt: storedtime.ParseStoredTime(entity.CreatedAt),
	}
}

func projectAgentFromEntity(entity *dao.ProjectAgent) ProjectAgentRecord {
	item := ProjectAgentRecord{
		ID:               entity.ID,
		Name:             entity.Name,
		ShortID:          entity.ShortID,
		ProjectID:        entity.ProjectID,
		AgentName:        entity.AgentName,
		Revision:         entity.Revision,
		Provider:         entity.Provider,
		Model:            entity.Model,
		Image:            entity.Image,
		Driver:           entity.Driver,
		SchedulerEnabled: entity.SchedulerEnabled != 0,
		SpecJSON:         entity.SpecJSON,
		CreatedAt:        storedtime.ParseStoredTime(entity.CreatedAt),
		UpdatedAt:        storedtime.ParseStoredTime(entity.UpdatedAt),
	}
	if item.Name == "" {
		item.Name = item.AgentName
	}
	if item.ShortID == "" {
		item.ShortID = identity.ShortID(item.ID)
	}
	return item
}

// projectSchedulerFromEntity maps a scheduler row for the reads that do not
// select last_error, so LastError stays empty like the released scanner. The
// released SELECTs read the scheduler id column twice, deliberately ignoring
// the migration-only scheduler_id alias, so SchedulerID mirrors ID.
func projectSchedulerFromEntity(entity *dao.ProjectScheduler) ProjectSchedulerRecord {
	item := ProjectSchedulerRecord{
		ID:           entity.ID,
		ShortID:      entity.ShortID,
		ProjectID:    entity.ProjectID,
		SchedulerID:  entity.ID,
		AgentName:    entity.AgentName,
		Revision:     entity.Revision,
		Enabled:      entity.Enabled != 0,
		TriggerCount: entity.TriggerCount,
		SpecJSON:     entity.SpecJSON,
		CreatedAt:    storedtime.ParseStoredTime(entity.CreatedAt),
		UpdatedAt:    storedtime.ParseStoredTime(entity.UpdatedAt),
	}
	if item.ID == "" {
		item.ID = item.SchedulerID
	}
	if item.ShortID == "" {
		item.ShortID = identity.ShortID(item.ID)
	}
	return item
}

// projectRunFromEntity maps a run row exactly like projects.ScanProjectRun.
func projectRunFromEntity(entity *dao.ProjectRun) ProjectRunRecord {
	item := ProjectRunRecord{
		RunID:           entity.ID,
		ProjectID:       entity.ProjectID,
		ProjectName:     entity.ProjectName,
		ProjectRevision: entity.ProjectRevision,
		AgentName:       entity.AgentName,
		AgentID:         entity.AgentID,
		Source:          entity.Source,
		SchedulerID:     entity.SchedulerID,
		TriggerID:       entity.TriggerID,
		Status:          entity.Status,
		SandboxID:       entity.SandboxID,
		ExitCode:        entity.ExitCode,
		Error:           entity.Error,
		ErrorStack:      entity.ErrorStack,
		Prompt:          entity.Prompt,
		Output:          entity.Output,
		ResultJSON:      entity.ResultJSON,
		LogsPath:        entity.LogsPath,
		ArtifactsDir:    entity.ArtifactsDir,
		CleanupError:    entity.CleanupError,
		CleanupPolicy:   entity.CleanupPolicy,
		SandboxCreated:  entity.SandboxCreated != 0,
		Driver:          entity.Driver,
		ImageRef:        entity.ImageRef,
		StartedAt:       storedtime.ParseStoredUnixTimeAuto(entity.StartedAt),
		CompletedAt:     storedtime.ParseStoredUnixTimeAuto(entity.CompletedAt),
		DurationMs:      entity.DurationMs,
		CreatedAt:       storedtime.ParseStoredTime(entity.CreatedAt),
		UpdatedAt:       storedtime.ParseStoredTime(entity.UpdatedAt),
		ExecutionID:     entity.ExecutionID,
		Detach:          entity.Detach != 0,
		Deadline:        storedtime.ParseStoredUnixTimeAuto(entity.Deadline),
		DetachKind:      entity.DetachKind,
	}
	if entity.SchedulerRunID != nil {
		item.SchedulerRunID = *entity.SchedulerRunID
	}
	return item
}
