package model

import (
	"fmt"
	"strings"
)

// EventDeliveryScope bounds which Engine Projects receive an event that a
// Project published. The daemon operator sets it; Project content cannot
// widen it, so a Project can never declare itself daemon-wide.
type EventDeliveryScope string

const (
	// EventDeliveryScopeProject delivers an event only to subscribers in the
	// publisher's own Project. It is the default.
	EventDeliveryScopeProject EventDeliveryScope = "project"
	// EventDeliveryScopeDaemon delivers an event to matching subscribers in
	// every Project on the daemon.
	EventDeliveryScopeDaemon EventDeliveryScope = "daemon"
)

// ParseEventDeliveryScope parses a configured scope. An empty value selects
// the Project scope; an unknown value is an error rather than a silent default.
func ParseEventDeliveryScope(raw string) (EventDeliveryScope, error) {
	switch scope := EventDeliveryScope(strings.ToLower(strings.TrimSpace(raw))); scope {
	case "":
		return EventDeliveryScopeProject, nil
	case EventDeliveryScopeProject, EventDeliveryScopeDaemon:
		return scope, nil
	default:
		return "", fmt.Errorf("unsupported event delivery scope %q: use %q or %q", raw, EventDeliveryScopeProject, EventDeliveryScopeDaemon)
	}
}

// Reaches reports whether an event may be delivered to a subscriber in
// subscriberProjectID. Webhook events enter through operator-configured
// webhook sources rather than from a Project, so no Project scope applies to
// them.
func (s EventDeliveryScope) Reaches(event SchedulerTopicEvent, subscriberProjectID string) bool {
	if s == EventDeliveryScopeDaemon || event.Source == TopicEventSourceWebhook {
		return true
	}
	return strings.TrimSpace(event.PublisherProjectID) == strings.TrimSpace(subscriberProjectID)
}
