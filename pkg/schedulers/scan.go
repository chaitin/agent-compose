package schedulers

import (
	"fmt"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storedtime"
)

func ScanSchedulerTrigger(scan func(dest ...any) error) (domain.SchedulerTrigger, error) {
	var item domain.SchedulerTrigger
	var enabled int
	var autoID int
	var nextFireAtRaw any
	var lastFiredAtRaw any
	if err := scan(&item.SchedulerID, &item.ID, &item.Kind, &item.Topic, &item.IntervalMs, &enabled, &autoID, &item.SpecJSON, &nextFireAtRaw, &lastFiredAtRaw); err != nil {
		return domain.SchedulerTrigger{}, fmt.Errorf("scan scheduler trigger: %w", err)
	}
	item.Enabled = enabled != 0
	item.AutoID = autoID != 0
	item.NextFireAt = storedtime.ParseStoredTime(nextFireAtRaw)
	item.LastFiredAt = storedtime.ParseStoredTime(lastFiredAtRaw)
	return item, nil
}

func ScanSchedulerRun(scan func(dest ...any) error) (domain.SchedulerRunSummary, error) {
	var item domain.SchedulerRunSummary
	var startedAtRaw any
	var completedAtRaw any
	if err := scan(&item.SchedulerID, &item.ID, &item.TriggerID, &item.TriggerKind, &item.TriggerSource, &item.Status, &startedAtRaw, &completedAtRaw, &item.DurationMs, &item.Error, &item.ResultJSON, &item.PayloadJSON, &item.SourceScriptHash, &item.ArtifactsDir); err != nil {
		return domain.SchedulerRunSummary{}, fmt.Errorf("scan scheduler run: %w", err)
	}
	item.StartedAt = storedtime.ParseStoredTime(startedAtRaw)
	item.CompletedAt = storedtime.ParseStoredTime(completedAtRaw)
	return item, nil
}

func ScanSchedulerEvent(scan func(dest ...any) error) (domain.SchedulerEvent, error) {
	var item domain.SchedulerEvent
	var createdAtRaw any
	if err := scan(&item.SchedulerID, &item.ID, &item.RunID, &item.TriggerID, &item.Type, &item.Level, &item.Message, &item.PayloadJSON, &item.LinkedSandboxID, &item.LinkedCellID, &item.LinkedAgentThreadID, &createdAtRaw); err != nil {
		return domain.SchedulerEvent{}, fmt.Errorf("scan scheduler event: %w", err)
	}
	item.CreatedAt = storedtime.ParseStoredTime(createdAtRaw)
	return item, nil
}

func ScanSchedulerBinding(scan func(dest ...any) error) (domain.SchedulerBinding, error) {
	var item domain.SchedulerBinding
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.SchedulerID, &item.TriggerID, &item.SandboxID, &item.SandboxConfigHash, &item.SchedulerConfigHash, &createdAtRaw, &updatedAtRaw); err != nil {
		return domain.SchedulerBinding{}, fmt.Errorf("scan scheduler binding: %w", err)
	}
	item.CreatedAt = storedtime.ParseStoredTime(createdAtRaw)
	item.UpdatedAt = storedtime.ParseStoredTime(updatedAtRaw)
	return item, nil
}

func SelectSchedulerTriggerSQL() string {
	return `SELECT scheduler_id, trigger_id, kind, topic, interval_ms, enabled, auto_id, spec_json, next_fire_at, last_fired_at
        FROM scheduler_trigger`
}

func SelectSchedulerRunSQL() string {
	return `SELECT scheduler_id, run_id, trigger_id, trigger_kind, trigger_source, status, started_at, completed_at, duration_ms, error, result_json, payload_json, source_script_sha256, artifacts_dir
        FROM scheduler_run`
}

func SelectSchedulerEventSQL() string {
	return `SELECT scheduler_id, event_id, scheduler_run_id, trigger_id, type, level, message, payload_json, linked_sandbox_id, linked_cell_id, linked_agent_thread_id, created_at
        FROM scheduler_event`
}

func SelectSchedulerBindingSQL() string {
	return `SELECT scheduler_id, trigger_id, sandbox_id, sandbox_config_hash, scheduler_config_hash, created_at, updated_at FROM scheduler_sandbox_binding`
}
