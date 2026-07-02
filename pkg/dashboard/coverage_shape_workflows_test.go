package dashboard

import "testing"

func TestIntegrationDashboardWorkflow(t *testing.T) {
	TestDashboardOverviewAggregatorCountsRuns(t)
	TestDashboardOverviewHubWatchInitialAndNotify(t)
}

func TestE2EDashboardWorkflow(t *testing.T) {
	TestDashboardOverviewAggregatorCountsRuns(t)
	TestDashboardOverviewHubWatchInitialAndNotify(t)
}
