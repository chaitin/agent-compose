package dashboard

import (
	"strings"
	"time"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const OverviewPageSize = 20

type RunSource struct {
	Status string
}

type OverviewInput struct {
	Sessions []RunSource
	Runs     []RunSource
	Now      time.Time
}

func BuildOverview(input OverviewInput) *agentcomposev1.DashboardOverview {
	overview := &agentcomposev1.DashboardOverview{
		Runs:      &agentcomposev1.RunOverview{},
		UpdatedAt: input.Now.UTC().Format(time.RFC3339Nano),
	}
	overview.Runs.RecentCount = uint32(len(input.Sessions) + len(input.Runs))
	for _, session := range input.Sessions {
		addStatus(overview.Runs, session.Status)
	}
	for _, run := range input.Runs {
		addStatus(overview.Runs, run.Status)
	}
	return overview
}

func IsRunningStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "PENDING", "RUNNING":
		return true
	default:
		return false
	}
}

func IsAttentionStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "FAILED", "SKIPPED", "CANCELED", "CANCELLED":
		return true
	default:
		return false
	}
}

func CloneOverview(item *agentcomposev1.DashboardOverview) *agentcomposev1.DashboardOverview {
	if item == nil {
		return nil
	}
	clone := &agentcomposev1.DashboardOverview{UpdatedAt: item.GetUpdatedAt()}
	if item.GetRuns() != nil {
		clone.Runs = &agentcomposev1.RunOverview{
			RunningCount:   item.GetRuns().GetRunningCount(),
			RecentCount:    item.GetRuns().GetRecentCount(),
			AttentionCount: item.GetRuns().GetAttentionCount(),
		}
	}
	return clone
}

func addStatus(overview *agentcomposev1.RunOverview, status string) {
	if IsRunningStatus(status) {
		overview.RunningCount++
	}
	if IsAttentionStatus(status) {
		overview.AttentionCount++
	}
}
