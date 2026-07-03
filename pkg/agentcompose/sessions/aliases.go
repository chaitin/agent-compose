package sessions

import owner "agent-compose/pkg/sessions"

const (
	WatchEventTypeCellCompleted  = owner.WatchEventTypeCellCompleted
	WatchEventTypeCellOutput     = owner.WatchEventTypeCellOutput
	WatchEventTypeCellStarted    = owner.WatchEventTypeCellStarted
	WatchEventTypeEventAdded     = owner.WatchEventTypeEventAdded
	WatchEventTypeSessionUpdated = owner.WatchEventTypeSessionUpdated
	WatchEventTypeUnspecified    = owner.WatchEventTypeUnspecified
)

type (
	StreamBroker   = owner.StreamBroker
	WatchEvent     = owner.WatchEvent
	WatchEventType = owner.WatchEventType
)

var (
	ApplySessionStartInfo  = owner.ApplySessionStartInfo
	NewStreamBroker        = owner.NewStreamBroker
	NewStreamBrokerForTest = owner.NewStreamBrokerForTest
)
