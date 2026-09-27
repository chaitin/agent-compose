package schedulers

import (
	"encoding/json"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestCommandAndEventHelperWorkflows(t *testing.T) {
	execReq := domain.SchedulerCommandRequest{Mode: "exec", Command: "echo", Args: []string{"ok"}}
	if err := ValidateCommandRequest(execReq); err != nil {
		t.Fatalf("ValidateCommandRequest exec returned error: %v", err)
	}
	if got := CommandCellSource(execReq); got != "echo ok" {
		t.Fatalf("exec source = %q", got)
	}
	shellReq := domain.SchedulerCommandRequest{Mode: "shell", Script: "echo shell"}
	if err := ValidateCommandRequest(shellReq); err != nil {
		t.Fatalf("ValidateCommandRequest shell returned error: %v", err)
	}
	if got := CommandCellSource(shellReq); got != "echo shell" {
		t.Fatalf("shell source = %q", got)
	}
	for _, req := range []domain.SchedulerCommandRequest{{Mode: "exec"}, {Mode: "shell"}, {Mode: "bad"}} {
		if err := ValidateCommandRequest(req); err == nil {
			t.Fatalf("expected validation error for %#v", req)
		}
	}
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{SandboxPolicy: domain.SchedulerSandboxPolicyReuse}}
	if CommandRequestRequiresCleanup(scheduler, domain.SchedulerCommandRequest{}) {
		t.Fatalf("reuse policy without overrides should not require cleanup")
	}
	if !CommandRequestRequiresCleanup(scheduler, domain.SchedulerCommandRequest{SandboxPolicy: domain.SchedulerSandboxPolicyNew}) {
		t.Fatalf("new policy should require cleanup")
	}
	if !CommandRequestOverridesSandbox(domain.SchedulerCommandRequest{Driver: "docker"}) ||
		!CommandRequestOverridesSandbox(domain.SchedulerCommandRequest{SandboxEnv: []domain.SandboxEnvVar{{Name: "A", Value: "B"}}}) {
		t.Fatalf("expected sandbox override detection")
	}

	published, err := NewPublishedTopicEvent(PublishTopicEventRequest{
		Topic:       "runtime.demo",
		PayloadJSON: `{"correlation_id":"corr","parentEventId":"parent","provider":"test","ok":true}`,
		Trigger:     TriggerEventMetadata{EventID: "trigger-event"},
		SchedulerID: "scheduler-1",
		RunID:       "run-1",
	})
	if err != nil {
		t.Fatalf("NewPublishedTopicEvent returned error: %v", err)
	}
	if published.Record.Topic != "runtime.demo" || published.Record.CorrelationID != "corr" || published.Record.ParentEventID != "parent" || published.Record.PublisherRunID != "run-1" {
		t.Fatalf("published record = %#v", published.Record)
	}
	updated, err := UpdatePublishedTopicEventSequence(published, 42)
	if err != nil {
		t.Fatalf("UpdatePublishedTopicEventSequence returned error: %v", err)
	}
	if updated.Sequence != 42 || !strings.Contains(updated.PayloadJSON, `"sequence":42`) {
		t.Fatalf("updated event = %#v", updated)
	}
	record, err := UpdatePublishedTopicEventSequence(PublishedTopicEvent{Record: domain.TopicEventRecord{ID: "no-envelope"}}, 7)
	if err != nil || record.Sequence != 7 {
		t.Fatalf("nil envelope update record=%#v err=%v", record, err)
	}
	if _, err := NewPublishedTopicEvent(PublishTopicEventRequest{Topic: "bad.topic", PayloadJSON: `{}`}); err == nil {
		t.Fatalf("expected invalid topic error")
	}
	if _, err := NewPublishedTopicEvent(PublishTopicEventRequest{Topic: "runtime.demo", PayloadJSON: `[]`}); err == nil {
		t.Fatalf("expected non-object payload error")
	}
	if !IsJSONObject(`{"ok":true}`) || IsJSONObject(`[]`) {
		t.Fatalf("IsJSONObject failed")
	}
	if err := ValidatePublishTopic("workflow.ready"); err != nil {
		t.Fatalf("workflow topic should be valid: %v", err)
	}
	if err := ValidatePublishTopic("external.ready"); err != nil {
		t.Fatalf("external topic should be valid: %v", err)
	}
	metaPayload, _ := json.Marshal(map[string]any{"payload": map[string]any{"eventId": "evt", "correlationId": "corr", "sequence": json.Number("12")}})
	meta := ParseTriggerEventMetadata(string(metaPayload))
	if meta.EventID != "evt" || meta.CorrelationID != "corr" || meta.Sequence != 12 {
		t.Fatalf("metadata = %#v", meta)
	}
	if ParseTriggerEventMetadata("{bad").EventID != "" || Int64FromMap(map[string]any{"n": int64(5)}, "n") != 5 {
		t.Fatalf("metadata helpers failed")
	}

	schedulers := []domain.Scheduler{
		{Summary: domain.SchedulerSummary{ID: "scheduler-1", Enabled: true, ConcurrencyPolicy: domain.SchedulerConcurrencyPolicySkip}, Triggers: []domain.SchedulerTrigger{{ID: "trigger-1", Enabled: true, Kind: domain.SchedulerTriggerKindEvent, Topic: "runtime.*"}}},
		{Summary: domain.SchedulerSummary{ID: "scheduler-2", Enabled: false}, Triggers: []domain.SchedulerTrigger{{ID: "disabled-scheduler", Enabled: true, Kind: domain.SchedulerTriggerKindEvent, Topic: "runtime.demo"}}},
		{Summary: domain.SchedulerSummary{ID: "scheduler-3", Enabled: true}, Triggers: []domain.SchedulerTrigger{{ID: "disabled-trigger", Enabled: false, Kind: domain.SchedulerTriggerKindEvent, Topic: "runtime.demo"}}},
	}
	targets := CollectEventTargets(schedulers, domain.SchedulerTopicEvent{Topic: "runtime.demo"}, domain.EventDeliveryScopeProject)
	if len(targets) != 1 || targets[0].Scheduler.Summary.ID != "scheduler-1" {
		t.Fatalf("targets = %#v", targets)
	}
	duplicated := append(targets, targets...)
	deduped := DedupeWebhookEventTargets(domain.SchedulerTopicEvent{Source: domain.TopicEventSourceWebhook}, duplicated)
	if len(deduped) != 1 {
		t.Fatalf("deduped targets = %#v", deduped)
	}
	if !AnyTargetBusy(targets, map[string]int{"scheduler-1": 1}) {
		t.Fatalf("expected busy target")
	}
	targets[0].Scheduler.Summary.ConcurrencyPolicy = domain.SchedulerConcurrencyPolicyParallel
	if AnyTargetBusy(targets, map[string]int{"scheduler-1": 1}) {
		t.Fatalf("parallel target should not be busy")
	}
}
