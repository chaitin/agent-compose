package dashboard

import owner "agent-compose/pkg/dashboard"

const (
	OverviewPageSize = owner.OverviewPageSize
)

type (
	Aggregator     = owner.Aggregator
	Event          = owner.Event
	Hub            = owner.Hub
	LoaderRunStore = owner.LoaderRunStore
	SessionStore   = owner.SessionStore
)

var (
	CloneOverview     = owner.CloneOverview
	IsAttentionStatus = owner.IsAttentionStatus
	IsRunningStatus   = owner.IsRunningStatus
	NewAggregator     = owner.NewAggregator
	NewHub            = owner.NewHub
)
