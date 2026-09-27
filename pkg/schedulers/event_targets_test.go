package schedulers

import (
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func eventSubscriber(id, projectID string) domain.Scheduler {
	return domain.Scheduler{
		Summary:  domain.SchedulerSummary{ID: id, ProjectID: projectID, Enabled: true},
		Triggers: []domain.SchedulerTrigger{{ID: id + "-trigger", Enabled: true, Kind: domain.SchedulerTriggerKindEvent, Topic: "workflow.x.ready"}},
	}
}

func targetSchedulerIDs(targets []EventTarget) []string {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, target.Scheduler.Summary.ID)
	}
	return ids
}

func TestCollectEventTargetsHonorsDeliveryScope(t *testing.T) {
	subscribers := []domain.Scheduler{eventSubscriber("a", "project-a"), eventSubscriber("b", "project-b")}
	published := domain.SchedulerTopicEvent{Topic: "workflow.x.ready", Source: domain.TopicEventSourceScheduler, PublisherProjectID: "project-a"}

	if got := targetSchedulerIDs(CollectEventTargets(subscribers, published, domain.EventDeliveryScopeProject)); len(got) != 1 || got[0] != "a" {
		t.Fatalf("project scope targets = %v, want [a]", got)
	}
	if got := targetSchedulerIDs(CollectEventTargets(subscribers, published, domain.EventDeliveryScopeDaemon)); len(got) != 2 {
		t.Fatalf("daemon scope targets = %v, want [a b]", got)
	}
	webhook := domain.SchedulerTopicEvent{Topic: "workflow.x.ready", Source: domain.TopicEventSourceWebhook}
	if got := targetSchedulerIDs(CollectEventTargets(subscribers, webhook, domain.EventDeliveryScopeProject)); len(got) != 2 {
		t.Fatalf("webhook targets = %v, want [a b]", got)
	}
}

func TestNewControllerDefaultsToProjectDeliveryScope(t *testing.T) {
	if got := NewController(ControllerDependencies{}).EventDeliveryScope(); got != domain.EventDeliveryScopeProject {
		t.Fatalf("default scope = %q, want project", got)
	}
	if got := NewController(ControllerDependencies{EventDeliveryScope: domain.EventDeliveryScopeDaemon}).EventDeliveryScope(); got != domain.EventDeliveryScopeDaemon {
		t.Fatalf("configured scope = %q, want daemon", got)
	}
}
