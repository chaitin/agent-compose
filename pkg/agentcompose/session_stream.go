package agentcompose

import (
	sessiondomain "agent-compose/internal/agentcompose/session"

	"github.com/samber/do/v2"
)

const sessionStreamBufferSize = sessiondomain.StreamBufferSize

type sessionWatchEventType = sessiondomain.WatchEventType

const (
	sessionWatchEventTypeUnspecified    = sessiondomain.WatchEventTypeUnspecified
	sessionWatchEventTypeSessionUpdated = sessiondomain.WatchEventTypeSessionUpdated
	sessionWatchEventTypeCellStarted    = sessiondomain.WatchEventTypeCellStarted
	sessionWatchEventTypeCellOutput     = sessiondomain.WatchEventTypeCellOutput
	sessionWatchEventTypeCellCompleted  = sessiondomain.WatchEventTypeCellCompleted
	sessionWatchEventTypeEventAdded     = sessiondomain.WatchEventTypeEventAdded
)

type sessionWatchEvent = sessiondomain.WatchEvent
type SessionStreamBroker = sessiondomain.StreamBroker

func NewSessionStreamBroker(do.Injector) (*SessionStreamBroker, error) {
	return sessiondomain.NewStreamBroker(), nil
}

func cloneSessionSummary(summary *SessionSummary) *SessionSummary {
	return sessiondomain.CloneSummary(summary)
}

func cloneNotebookCell(cell *NotebookCell) *NotebookCell {
	return sessiondomain.CloneNotebookCell(cell)
}

func cloneSessionEvent(event *SessionEvent) *SessionEvent {
	return sessiondomain.CloneEvent(event)
}
