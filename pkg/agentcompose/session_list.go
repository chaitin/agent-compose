package agentcompose

import (
	sessiondomain "agent-compose/internal/agentcompose/session"
	"time"
)

const defaultSessionListLimit = sessiondomain.DefaultListLimit

func normalizeSessionTriggerSource(value string, tags []SessionTag) string {
	return sessiondomain.NormalizeTriggerSource(value, tags)
}

func sessionTypeFromTriggerSource(value string) string {
	return sessiondomain.TypeFromTriggerSource(value)
}

func normalizeSessionListBounds(offset, limit int) (int, int) {
	return sessiondomain.NormalizeListBounds(offset, limit)
}

func paginateSessions(items []*Session, offset, limit int) []*Session {
	return sessiondomain.Paginate(items, offset, limit)
}

func sessionMatchesListOptions(session *Session, options SessionListOptions) bool {
	return sessiondomain.MatchesListOptions(session, options)
}

func matchesTimeRange(value, from, to time.Time) bool {
	return sessiondomain.MatchesTimeRange(value, from, to)
}
