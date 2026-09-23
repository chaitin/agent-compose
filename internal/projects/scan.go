package projects

import (
	"database/sql"
	"fmt"
	"github.com/chaitin/agent-compose/pkg/identity"
	"github.com/chaitin/agent-compose/pkg/storedtime"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func ScanProject(scan func(dest ...any) error) (domain.ProjectRecord, error) {
	var item domain.ProjectRecord
	var createdAtRaw any
	var updatedAtRaw any
	var removedAtRaw any
	if err := scan(&item.ID, &item.Name, &item.ShortID, &item.SourcePath, &item.SourceJSON, &item.CurrentRevision, &item.SpecHash, &createdAtRaw, &updatedAtRaw, &removedAtRaw); err != nil {
		return domain.ProjectRecord{}, fmt.Errorf("scan project: %w", err)
	}
	item.CreatedAt = storedtime.ParseStoredTime(createdAtRaw)
	item.UpdatedAt = storedtime.ParseStoredTime(updatedAtRaw)
	item.RemovedAt = storedtime.ParseStoredTime(removedAtRaw)
	if item.ShortID == "" {
		item.ShortID = identity.ShortID(item.ID)
	}
	return item, nil
}

func ScanProjectRevision(scan func(dest ...any) error) (domain.ProjectRevisionRecord, error) {
	var item domain.ProjectRevisionRecord
	var createdAtRaw any
	if err := scan(&item.ProjectID, &item.Revision, &item.SpecHash, &item.SpecJSON, &createdAtRaw); err != nil {
		return domain.ProjectRevisionRecord{}, fmt.Errorf("scan project revision: %w", err)
	}
	item.CreatedAt = storedtime.ParseStoredTime(createdAtRaw)
	return item, nil
}

func ScanProjectAgent(scan func(dest ...any) error) (domain.ProjectAgentRecord, error) {
	var item domain.ProjectAgentRecord
	var schedulerEnabled int
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.ID, &item.Name, &item.ShortID, &item.ProjectID, &item.AgentName, &item.Revision, &item.Provider, &item.Model, &item.Image, &item.Driver, &schedulerEnabled, &item.SpecJSON, &createdAtRaw, &updatedAtRaw); err != nil {
		return domain.ProjectAgentRecord{}, fmt.Errorf("scan project agent: %w", err)
	}
	if item.Name == "" {
		item.Name = item.AgentName
	}
	if item.ShortID == "" {
		item.ShortID = identity.ShortID(item.ID)
	}
	item.SchedulerEnabled = schedulerEnabled != 0
	item.CreatedAt = storedtime.ParseStoredTime(createdAtRaw)
	item.UpdatedAt = storedtime.ParseStoredTime(updatedAtRaw)
	return item, nil
}

func ScanProjectScheduler(scan func(dest ...any) error) (domain.ProjectSchedulerRecord, error) {
	var item domain.ProjectSchedulerRecord
	var enabled int
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.ID, &item.ShortID, &item.ProjectID, &item.SchedulerID, &item.AgentName, &item.Revision, &enabled, &item.TriggerCount, &item.SpecJSON, &createdAtRaw, &updatedAtRaw); err != nil {
		return domain.ProjectSchedulerRecord{}, fmt.Errorf("scan project scheduler: %w", err)
	}
	if item.ID == "" {
		item.ID = item.SchedulerID
	}
	if item.ShortID == "" {
		item.ShortID = identity.ShortID(item.ID)
	}
	item.Enabled = enabled != 0
	item.CreatedAt = storedtime.ParseStoredTime(createdAtRaw)
	item.UpdatedAt = storedtime.ParseStoredTime(updatedAtRaw)
	return item, nil
}

func ScanProjectSchedulerPage(scan func(dest ...any) error) (domain.ProjectSchedulerRecord, error) {
	var item domain.ProjectSchedulerRecord
	var enabled int
	var createdAtRaw any
	var updatedAtRaw any
	var latestRunAtRaw any
	if err := scan(&item.ID, &item.ShortID, &item.ProjectID, &item.SchedulerID, &item.AgentName, &item.Revision, &enabled, &item.TriggerCount, &item.SpecJSON, &createdAtRaw, &updatedAtRaw, &item.RunCount, &latestRunAtRaw, &item.LastError); err != nil {
		return domain.ProjectSchedulerRecord{}, fmt.Errorf("scan project scheduler page: %w", err)
	}
	if item.ID == "" {
		item.ID = item.SchedulerID
	}
	if item.ShortID == "" {
		item.ShortID = identity.ShortID(item.ID)
	}
	item.Enabled = enabled != 0
	item.CreatedAt = storedtime.ParseStoredTime(createdAtRaw)
	item.UpdatedAt = storedtime.ParseStoredTime(updatedAtRaw)
	item.LatestRunAt = storedtime.ParseStoredTime(latestRunAtRaw)
	return item, nil
}

func ScanProjectRun(scan func(dest ...any) error) (domain.ProjectRunRecord, error) {
	var item domain.ProjectRunRecord
	var schedulerRunID sql.NullString
	var sandboxCreated int
	var startedAtRaw any
	var completedAtRaw any
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(
		&item.RunID, &item.ProjectID, &item.ProjectName, &item.ProjectRevision, &item.AgentName, &item.AgentID, &item.Source, &item.SchedulerID, &schedulerRunID, &item.TriggerID, &item.Status,
		&item.SandboxID, &item.ExitCode, &item.Error, &item.ErrorStack, &item.Prompt, &item.Output, &item.ResultJSON, &item.LogsPath, &item.ArtifactsDir, &item.CleanupError, &item.CleanupPolicy, &sandboxCreated, &item.Driver, &item.ImageRef,
		&startedAtRaw, &completedAtRaw, &item.DurationMs, &createdAtRaw, &updatedAtRaw,
	); err != nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("scan project run: %w", err)
	}
	item.StartedAt = storedtime.ParseStoredUnixTimeAuto(AsInt64Time(startedAtRaw))
	item.SchedulerRunID = schedulerRunID.String
	item.SandboxCreated = sandboxCreated != 0
	item.CompletedAt = storedtime.ParseStoredUnixTimeAuto(AsInt64Time(completedAtRaw))
	item.CreatedAt = storedtime.ParseStoredTime(createdAtRaw)
	item.UpdatedAt = storedtime.ParseStoredTime(updatedAtRaw)
	return item, nil
}

func ScanProjectAgentRunState(scan func(dest ...any) error) (domain.ProjectAgentRunState, error) {
	var item domain.ProjectAgentRunState
	var runningCount int64
	var runningSchedulerCount int64
	var latestAtRaw any
	if err := scan(&item.AgentName, &runningCount, &runningSchedulerCount, &item.LatestRunID, &item.LatestStatus, &item.LatestSource, &latestAtRaw); err != nil {
		return domain.ProjectAgentRunState{}, fmt.Errorf("scan project agent run state: %w", err)
	}
	item.RunningRunCount = uint32(runningCount)
	item.RunningSchedulerRunCount = uint32(runningSchedulerCount)
	item.LatestAt = storedtime.ParseStoredTime(latestAtRaw)
	return item, nil
}

func AsInt64Time(value any) int64 {
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
		return AsInt64Time(string(typed))
	case string:
		parsed, _ := ParseInt64String(typed)
		return parsed
	default:
		return 0
	}
}

func ParseInt64String(value string) (int64, bool) {
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

func SelectProjectRunSQL() string {
	return `SELECT run_id, project_id, project_name, project_revision, agent_name, agent_id, source, scheduler_id, scheduler_run_id, trigger_id, status,
		sandbox_id, exit_code, error, error_stack, prompt, output, result_json, logs_path, artifacts_dir, cleanup_error, cleanup_policy, sandbox_created, driver, image_ref,
		started_at, completed_at, duration_ms, created_at, updated_at FROM project_run`
}
