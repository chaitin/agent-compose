package projects

import (
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
)

func ScanProject(scan func(dest ...any) error) (domain.ProjectRecord, error) {
	var item domain.ProjectRecord
	var createdAtRaw any
	var updatedAtRaw any
	var removedAtRaw any
	if err := scan(&item.ID, &item.Name, &item.SourcePath, &item.SourceJSON, &item.CurrentRevision, &item.SpecHash, &createdAtRaw, &updatedAtRaw, &removedAtRaw); err != nil {
		return domain.ProjectRecord{}, fmt.Errorf("scan project: %w", err)
	}
	item.CreatedAt = configstore.ParseStoredTime(createdAtRaw)
	item.UpdatedAt = configstore.ParseStoredTime(updatedAtRaw)
	item.RemovedAt = configstore.ParseStoredTime(removedAtRaw)
	return item, nil
}

func ScanProjectRevision(scan func(dest ...any) error) (domain.ProjectRevisionRecord, error) {
	var item domain.ProjectRevisionRecord
	var createdAtRaw any
	if err := scan(&item.ProjectID, &item.Revision, &item.SpecHash, &item.SpecJSON, &createdAtRaw); err != nil {
		return domain.ProjectRevisionRecord{}, fmt.Errorf("scan project revision: %w", err)
	}
	item.CreatedAt = configstore.ParseStoredTime(createdAtRaw)
	return item, nil
}

func ScanProjectAgent(scan func(dest ...any) error) (domain.ProjectAgentRecord, error) {
	var item domain.ProjectAgentRecord
	var schedulerEnabled int
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.ProjectID, &item.AgentName, &item.ManagedAgentID, &item.Revision, &item.Provider, &item.Model, &item.Image, &item.Driver, &schedulerEnabled, &item.SpecJSON, &createdAtRaw, &updatedAtRaw); err != nil {
		return domain.ProjectAgentRecord{}, fmt.Errorf("scan project agent: %w", err)
	}
	item.SchedulerEnabled = schedulerEnabled != 0
	item.CreatedAt = configstore.ParseStoredTime(createdAtRaw)
	item.UpdatedAt = configstore.ParseStoredTime(updatedAtRaw)
	return item, nil
}

func ScanProjectScheduler(scan func(dest ...any) error) (domain.ProjectSchedulerRecord, error) {
	var item domain.ProjectSchedulerRecord
	var enabled int
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.ProjectID, &item.SchedulerID, &item.AgentName, &item.ManagedLoaderID, &item.Revision, &enabled, &item.TriggerCount, &item.SpecJSON, &createdAtRaw, &updatedAtRaw); err != nil {
		return domain.ProjectSchedulerRecord{}, fmt.Errorf("scan project scheduler: %w", err)
	}
	item.Enabled = enabled != 0
	item.CreatedAt = configstore.ParseStoredTime(createdAtRaw)
	item.UpdatedAt = configstore.ParseStoredTime(updatedAtRaw)
	return item, nil
}

func ScanProjectRun(scan func(dest ...any) error) (domain.ProjectRunRecord, error) {
	var item domain.ProjectRunRecord
	var startedAtRaw any
	var completedAtRaw any
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(
		&item.RunID, &item.ProjectID, &item.ProjectName, &item.ProjectRevision, &item.AgentName, &item.ManagedAgentID, &item.Source, &item.SchedulerID, &item.TriggerID, &item.Status,
		&item.SessionID, &item.ExitCode, &item.Error, &item.Prompt, &item.Output, &item.ResultJSON, &item.LogsPath, &item.ArtifactsDir, &item.CleanupError, &item.Driver, &item.ImageRef,
		&startedAtRaw, &completedAtRaw, &item.DurationMs, &createdAtRaw, &updatedAtRaw,
	); err != nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("scan project run: %w", err)
	}
	item.StartedAt = configstore.ParseStoredUnixTimeAuto(AsInt64Time(startedAtRaw))
	item.CompletedAt = configstore.ParseStoredUnixTimeAuto(AsInt64Time(completedAtRaw))
	item.CreatedAt = configstore.ParseStoredTime(createdAtRaw)
	item.UpdatedAt = configstore.ParseStoredTime(updatedAtRaw)
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
	return `SELECT run_id, project_id, project_name, project_revision, agent_name, managed_agent_id, source, scheduler_id, trigger_id, status,
		session_id, exit_code, error, prompt, output, result_json, logs_path, artifacts_dir, cleanup_error, driver, image_ref,
		started_at, completed_at, duration_ms, created_at, updated_at FROM project_run`
}
