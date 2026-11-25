package schedulers

import (
	"fmt"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storedtime"
)

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
