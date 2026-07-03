package events

import owner "agent-compose/pkg/events"

type (
	Bus        = owner.Bus
	Dispatcher = owner.Dispatcher
	Store      = owner.Store
)

var (
	NewDispatcher             = owner.NewDispatcher
	NormalizeTopicEventRecord = owner.NormalizeTopicEventRecord
	ScanTopicEvent            = owner.ScanTopicEvent
	ScanTopicEvents           = owner.ScanTopicEvents
	SelectTopicEventSQL       = owner.SelectTopicEventSQL
)
