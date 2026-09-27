package schedulers

import (
	"strings"

	"github.com/chaitin/agent-compose/pkg/events"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type EventTarget struct {
	Scheduler domain.Scheduler
	Trigger   domain.SchedulerTrigger
}

// CollectEventTargets returns the enabled event triggers that subscribe to the
// event's topic and that the delivery scope lets the event reach.
func CollectEventTargets(items []domain.Scheduler, event domain.SchedulerTopicEvent, scope domain.EventDeliveryScope) []EventTarget {
	targets := make([]EventTarget, 0)
	for _, scheduler := range items {
		if !scheduler.Summary.Enabled || !scope.Reaches(event, scheduler.Summary.ProjectID) {
			continue
		}
		for _, trigger := range scheduler.Triggers {
			if !trigger.Enabled || trigger.Kind != domain.SchedulerTriggerKindEvent || !events.TriggerTopicMatches(trigger.Topic, event.Topic) {
				continue
			}
			targets = append(targets, EventTarget{
				Scheduler: scheduler,
				Trigger:   trigger,
			})
		}
	}
	return targets
}

func DedupeWebhookEventTargets(event domain.SchedulerTopicEvent, targets []EventTarget) []EventTarget {
	if event.Source != domain.TopicEventSourceWebhook || len(targets) <= 1 {
		return targets
	}
	seen := map[string]struct{}{}
	deduped := make([]EventTarget, 0, len(targets))
	for _, target := range targets {
		schedulerID := strings.TrimSpace(target.Scheduler.Summary.ID)
		if schedulerID == "" {
			deduped = append(deduped, target)
			continue
		}
		if _, ok := seen[schedulerID]; ok {
			continue
		}
		seen[schedulerID] = struct{}{}
		deduped = append(deduped, target)
	}
	return deduped
}

func AnyTargetBusy(targets []EventTarget, running map[string]int) bool {
	for _, target := range targets {
		schedulerID := strings.TrimSpace(target.Scheduler.Summary.ID)
		if NormalizeConcurrencyPolicy(target.Scheduler.Summary.ConcurrencyPolicy) != domain.SchedulerConcurrencyPolicyParallel && running[schedulerID] > 0 {
			return true
		}
	}
	return false
}
