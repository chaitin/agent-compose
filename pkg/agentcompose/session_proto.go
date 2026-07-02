package agentcompose

import (
	sessiondomain "agent-compose/internal/agentcompose/session"
	"time"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func sessionListOptionsFromProto(req *agentcomposev1.ListSessionsRequest) (SessionListOptions, error) {
	return sessiondomain.ListOptionsFromProto(req)
}

func parseOptionalRFC3339(raw, field string) (time.Time, error) {
	return sessiondomain.ParseOptionalRFC3339(raw, field)
}
