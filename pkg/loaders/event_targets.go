package loaders

import (
	"strings"

	domain "agent-compose/pkg/model"
)

type EventTarget struct {
	Loader  domain.Loader
	Trigger domain.LoaderTrigger
}

func CollectEventTargets(items []domain.Loader, topic string) []EventTarget {
	targets := make([]EventTarget, 0)
	for _, loader := range items {
		if !loader.Summary.Enabled {
			continue
		}
		for _, trigger := range loader.Triggers {
			if !trigger.Enabled || trigger.Kind != domain.LoaderTriggerKindEvent || !domain.LoaderTriggerTopicMatches(trigger.Topic, topic) {
				continue
			}
			targets = append(targets, EventTarget{
				Loader:  loader,
				Trigger: trigger,
			})
		}
	}
	return targets
}

func DedupeWebhookEventTargets(event domain.LoaderTopicEvent, targets []EventTarget) []EventTarget {
	if event.Source != domain.TopicEventSourceWebhook || len(targets) <= 1 {
		return targets
	}
	seen := map[string]struct{}{}
	deduped := make([]EventTarget, 0, len(targets))
	for _, target := range targets {
		loaderID := strings.TrimSpace(target.Loader.Summary.ID)
		if loaderID == "" {
			deduped = append(deduped, target)
			continue
		}
		if _, ok := seen[loaderID]; ok {
			continue
		}
		seen[loaderID] = struct{}{}
		deduped = append(deduped, target)
	}
	return deduped
}

func AnyTargetBusy(targets []EventTarget, running map[string]int) bool {
	for _, target := range targets {
		loaderID := strings.TrimSpace(target.Loader.Summary.ID)
		if domain.NormalizeLoaderConcurrencyPolicy(target.Loader.Summary.ConcurrencyPolicy) != domain.LoaderConcurrencyPolicyParallel && running[loaderID] > 0 {
			return true
		}
	}
	return false
}
