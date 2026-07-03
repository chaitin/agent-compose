package loaders

import (
	"fmt"
	"strings"

	"agent-compose/pkg/capabilities"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
)

func ScanLoaderSummary(scan func(dest ...any) error) (domain.LoaderSummary, error) {
	var item domain.LoaderSummary
	var enabled int
	var capsetIDsRaw string
	var createdAtRaw any
	var updatedAtRaw any
	var latestRunAtRaw any
	if err := scan(
		&item.ID,
		&item.Name,
		&item.Description,
		&item.Runtime,
		&item.WorkspaceID,
		&item.AgentID,
		&item.Driver,
		&item.GuestImage,
		&item.DefaultAgent,
		&item.SessionPolicy,
		&item.ConcurrencyPolicy,
		&capsetIDsRaw,
		&item.ManagedProjectID,
		&item.ManagedRevision,
		&item.ManagedAgentName,
		&item.ManagedSchedulerID,
		&enabled,
		&item.LastError,
		&createdAtRaw,
		&updatedAtRaw,
		&item.TriggerCount,
		&item.RunCount,
		&item.EventCount,
		&latestRunAtRaw,
	); err != nil {
		return domain.LoaderSummary{}, fmt.Errorf("scan loader summary: %w", err)
	}
	item.CapsetIDs = capabilities.DecodeCapsetIDs(capsetIDsRaw)
	item.ManagedProjectID = strings.TrimSpace(item.ManagedProjectID)
	item.ManagedAgentName = strings.TrimSpace(item.ManagedAgentName)
	item.ManagedSchedulerID = strings.TrimSpace(item.ManagedSchedulerID)
	item.Enabled = enabled != 0
	item.CreatedAt = configstore.ParseStoredTime(createdAtRaw)
	item.UpdatedAt = configstore.ParseStoredTime(updatedAtRaw)
	item.LatestRunAt = configstore.ParseStoredTime(latestRunAtRaw)
	return item, nil
}

func ScanLoader(scan func(dest ...any) error) (domain.Loader, error) {
	var item domain.Loader
	var enabled int
	var envJSON string
	var capsetIDsRaw string
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(
		&item.Summary.ID,
		&item.Summary.Name,
		&item.Summary.Description,
		&item.Summary.Runtime,
		&item.Script,
		&item.Summary.WorkspaceID,
		&item.Summary.AgentID,
		&item.Summary.Driver,
		&item.Summary.GuestImage,
		&item.Summary.DefaultAgent,
		&item.Summary.SessionPolicy,
		&item.Summary.ConcurrencyPolicy,
		&capsetIDsRaw,
		&envJSON,
		&item.Summary.ManagedProjectID,
		&item.Summary.ManagedRevision,
		&item.Summary.ManagedAgentName,
		&item.Summary.ManagedSchedulerID,
		&enabled,
		&item.Summary.LastError,
		&createdAtRaw,
		&updatedAtRaw,
	); err != nil {
		return domain.Loader{}, fmt.Errorf("scan loader: %w", err)
	}
	item.Summary.CapsetIDs = capabilities.DecodeCapsetIDs(capsetIDsRaw)
	item.Summary.ManagedProjectID = strings.TrimSpace(item.Summary.ManagedProjectID)
	item.Summary.ManagedAgentName = strings.TrimSpace(item.Summary.ManagedAgentName)
	item.Summary.ManagedSchedulerID = strings.TrimSpace(item.Summary.ManagedSchedulerID)
	item.Summary.Enabled = enabled != 0
	item.Summary.CreatedAt = configstore.ParseStoredTime(createdAtRaw)
	item.Summary.UpdatedAt = configstore.ParseStoredTime(updatedAtRaw)
	envItems, err := DecodeEnvItems(envJSON)
	if err != nil {
		return domain.Loader{}, err
	}
	item.EnvItems = envItems
	return item, nil
}

func ScanLoaderTrigger(scan func(dest ...any) error) (domain.LoaderTrigger, error) {
	var item domain.LoaderTrigger
	var enabled int
	var autoID int
	var nextFireAtRaw any
	var lastFiredAtRaw any
	if err := scan(&item.LoaderID, &item.ID, &item.Kind, &item.Topic, &item.IntervalMs, &enabled, &autoID, &item.SpecJSON, &nextFireAtRaw, &lastFiredAtRaw); err != nil {
		return domain.LoaderTrigger{}, fmt.Errorf("scan loader trigger: %w", err)
	}
	item.Enabled = enabled != 0
	item.AutoID = autoID != 0
	item.NextFireAt = configstore.ParseStoredLoaderTriggerTime(nextFireAtRaw)
	item.LastFiredAt = configstore.ParseStoredLoaderTriggerTime(lastFiredAtRaw)
	return item, nil
}

func ScanLoaderRun(scan func(dest ...any) error) (domain.LoaderRunSummary, error) {
	var item domain.LoaderRunSummary
	var startedAtRaw any
	var completedAtRaw any
	if err := scan(&item.LoaderID, &item.ID, &item.TriggerID, &item.TriggerKind, &item.TriggerSource, &item.Status, &startedAtRaw, &completedAtRaw, &item.DurationMs, &item.Error, &item.ResultJSON, &item.PayloadJSON, &item.SourceScriptHash, &item.ArtifactsDir); err != nil {
		return domain.LoaderRunSummary{}, fmt.Errorf("scan loader run: %w", err)
	}
	item.StartedAt = configstore.ParseStoredTime(startedAtRaw)
	item.CompletedAt = configstore.ParseStoredTime(completedAtRaw)
	return item, nil
}

func ScanLoaderEvent(scan func(dest ...any) error) (domain.LoaderEvent, error) {
	var item domain.LoaderEvent
	var createdAtRaw any
	if err := scan(&item.LoaderID, &item.ID, &item.RunID, &item.TriggerID, &item.Type, &item.Level, &item.Message, &item.PayloadJSON, &item.LinkedSessionID, &item.LinkedCellID, &item.LinkedAgentSessionID, &createdAtRaw); err != nil {
		return domain.LoaderEvent{}, fmt.Errorf("scan loader event: %w", err)
	}
	item.CreatedAt = configstore.ParseStoredTime(createdAtRaw)
	return item, nil
}

func ScanLoaderBinding(scan func(dest ...any) error) (domain.LoaderBinding, error) {
	var item domain.LoaderBinding
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.LoaderID, &item.SessionID, &createdAtRaw, &updatedAtRaw); err != nil {
		return domain.LoaderBinding{}, fmt.Errorf("scan loader binding: %w", err)
	}
	item.CreatedAt = configstore.ParseStoredTime(createdAtRaw)
	item.UpdatedAt = configstore.ParseStoredTime(updatedAtRaw)
	return item, nil
}

func SelectLoaderSummarySQL() string {
	return `SELECT
        l.id,
        l.name,
        l.description,
        l.runtime,
        l.workspace_id,
        l.agent_id,
        l.driver,
        l.guest_image,
        l.default_agent,
        l.session_policy,
        l.concurrency_policy,
        l.capset_ids,
        l.managed_project_id,
        l.managed_project_revision,
        l.managed_agent_name,
        l.managed_scheduler_id,
        l.enabled,
        l.last_error,
        l.created_at,
        l.updated_at,
        (SELECT COUNT(*) FROM loader_trigger t WHERE t.loader_id = l.id),
        (SELECT COUNT(*) FROM loader_run r WHERE r.loader_id = l.id),
        (SELECT COUNT(*) FROM loader_event e WHERE e.loader_id = l.id),
        (SELECT MAX(r.started_at) FROM loader_run r WHERE r.loader_id = l.id)
        FROM loader l`
}

func SelectLoaderSQL() string {
	return `SELECT
        id, name, description, runtime, script, workspace_id, agent_id, driver, guest_image, default_agent, session_policy, concurrency_policy, capset_ids, env_json,
        managed_project_id, managed_project_revision, managed_agent_name, managed_scheduler_id, enabled, last_error, created_at, updated_at
        FROM loader`
}

func SelectLoaderTriggerSQL() string {
	return `SELECT loader_id, trigger_id, kind, topic, interval_ms, enabled, auto_id, spec_json, next_fire_at, last_fired_at
        FROM loader_trigger`
}

func SelectLoaderRunSQL() string {
	return `SELECT loader_id, run_id, trigger_id, trigger_kind, trigger_source, status, started_at, completed_at, duration_ms, error, result_json, payload_json, source_script_sha256, artifacts_dir
        FROM loader_run`
}

func SelectLoaderEventSQL() string {
	return `SELECT loader_id, event_id, run_id, trigger_id, type, level, message, payload_json, linked_session_id, linked_cell_id, linked_agent_session_id, created_at
        FROM loader_event`
}

func SelectLoaderBindingSQL() string {
	return `SELECT loader_id, session_id, created_at, updated_at FROM loader_binding`
}
