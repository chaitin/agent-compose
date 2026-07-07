package projects

import (
	"strings"

	domain "agent-compose/pkg/model"
)

func ProjectSchedulersFromManagedLoaders(loaders []domain.Loader) []domain.ProjectSchedulerRecord {
	schedulers := make([]domain.ProjectSchedulerRecord, 0, len(loaders))
	for _, loader := range loaders {
		summary := loader.Summary
		if strings.TrimSpace(summary.ManagedProjectID) == "" ||
			strings.TrimSpace(summary.ManagedAgentName) == "" ||
			strings.TrimSpace(summary.ManagedSchedulerID) == "" {
			continue
		}
		schedulers = append(schedulers, domain.ProjectSchedulerRecord{
			ProjectID:       summary.ManagedProjectID,
			AgentName:       summary.ManagedAgentName,
			SchedulerID:     summary.ManagedSchedulerID,
			ManagedLoaderID: summary.ID,
			Revision:        summary.ManagedRevision,
			Enabled:         summary.Enabled,
			TriggerCount:    len(loader.Triggers),
			CreatedAt:       summary.CreatedAt,
			UpdatedAt:       summary.UpdatedAt,
		})
	}
	return schedulers
}
