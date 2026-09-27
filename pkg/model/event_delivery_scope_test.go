package model

import "testing"

func TestParseEventDeliveryScope(t *testing.T) {
	tests := []struct {
		raw     string
		want    EventDeliveryScope
		wantErr bool
	}{
		{raw: "", want: EventDeliveryScopeProject},
		{raw: "project", want: EventDeliveryScopeProject},
		{raw: " Daemon ", want: EventDeliveryScopeDaemon},
		{raw: "namespace", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseEventDeliveryScope(tt.raw)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Fatalf("ParseEventDeliveryScope(%q) = %q, %v; want %q, error %v", tt.raw, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestEventDeliveryScopeReaches(t *testing.T) {
	fromA := SchedulerTopicEvent{Topic: "workflow.x.ready", Source: TopicEventSourceScheduler, PublisherProjectID: "project-a"}
	system := SchedulerTopicEvent{Topic: "agent-compose.session.created", PublisherProjectID: "project-a"}
	webhook := SchedulerTopicEvent{Topic: "webhook.github.push", Source: TopicEventSourceWebhook}
	tests := []struct {
		name       string
		scope      EventDeliveryScope
		event      SchedulerTopicEvent
		subscriber string
		want       bool
	}{
		{name: "project scope reaches own project", scope: EventDeliveryScopeProject, event: fromA, subscriber: "project-a", want: true},
		{name: "project scope stops at other project", scope: EventDeliveryScopeProject, event: fromA, subscriber: "project-b", want: false},
		{name: "project scope stops system topic at other project", scope: EventDeliveryScopeProject, event: system, subscriber: "project-b", want: false},
		{name: "project scope keeps project events from unmanaged schedulers", scope: EventDeliveryScopeProject, event: fromA, subscriber: "", want: false},
		{name: "daemon scope reaches other project", scope: EventDeliveryScopeDaemon, event: fromA, subscriber: "project-b", want: true},
		{name: "webhook reaches every project", scope: EventDeliveryScopeProject, event: webhook, subscriber: "project-b", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.scope.Reaches(tt.event, tt.subscriber); got != tt.want {
				t.Fatalf("Reaches = %v, want %v", got, tt.want)
			}
		})
	}
}
