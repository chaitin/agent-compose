package chat

import (
	"strings"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// runIsLive reports whether a run can still accept an attach.
func runIsLive(run *agentcomposev2.RunSummary) bool {
	switch run.GetStatus() {
	case agentcomposev2.RunStatus_RUN_STATUS_PENDING, agentcomposev2.RunStatus_RUN_STATUS_RUNNING:
		return true
	default:
		return false
	}
}

// messageFromEvent maps one durable run event onto a Message. It returns false
// for events that are not conversational, such as lifecycle transitions.
func messageFromEvent(event *agentcomposev2.RunEvent) (Message, bool) {
	kind := strings.ToUpper(strings.TrimSpace(event.GetKind().String()))
	var role Role
	switch {
	case strings.HasSuffix(kind, "USER_MESSAGE"):
		role = RoleUser
	case strings.HasSuffix(kind, "AGENT_MESSAGE"):
		role = RoleAssistant
	default:
		return Message{}, false
	}
	if strings.TrimSpace(event.GetText()) == "" {
		return Message{}, false
	}
	return Message{
		ID:   event.GetId(),
		Role: role,
		Text: event.GetText(),
		Time: event.GetCreatedAt().AsTime(),
	}, true
}
