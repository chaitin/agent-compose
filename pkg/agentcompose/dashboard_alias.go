package agentcompose

import (
	"context"
	"time"

	"github.com/samber/do/v2"

	"agent-compose/pkg/dashboard"
)

type DashboardOverviewAggregator = dashboard.DashboardOverviewAggregator
type DashboardOverviewHub = dashboard.DashboardOverviewHub
type DashboardOverviewEvent = dashboard.DashboardOverviewEvent
type DashboardService = dashboard.Service

func NewDashboardOverviewAggregator(di do.Injector) (*DashboardOverviewAggregator, error) {
	return dashboard.NewDashboardOverviewAggregator(di)
}

func NewDashboardOverviewHub(di do.Injector) (*DashboardOverviewHub, error) {
	return dashboard.NewDashboardOverviewHub(di)
}

func newDashboardOverviewAggregator(store *Store, configDB *ConfigStore) *DashboardOverviewAggregator {
	return dashboard.NewAggregator(store, configDB)
}

func newDashboardOverviewHub(ctx context.Context, aggregator *DashboardOverviewAggregator, debounce time.Duration) *DashboardOverviewHub {
	return dashboard.NewHub(ctx, aggregator, debounce)
}
