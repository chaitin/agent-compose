package loaders

import (
	"encoding/json"
	"strings"
	"testing"

	domain "agent-compose/pkg/model"
)

func TestCommandAndEventHelperWorkflows(t *testing.T) {
	execReq := domain.LoaderCommandRequest{Mode: "exec", Command: "echo", Args: []string{"ok"}}
	if err := ValidateCommandRequest(execReq); err != nil {
		t.Fatalf("ValidateCommandRequest exec returned error: %v", err)
	}
	if got := CommandCellSource(execReq); got != "echo ok" {
		t.Fatalf("exec source = %q", got)
	}
	shellReq := domain.LoaderCommandRequest{Mode: "shell", Script: "echo shell"}
	if err := ValidateCommandRequest(shellReq); err != nil {
		t.Fatalf("ValidateCommandRequest shell returned error: %v", err)
	}
	if got := CommandCellSource(shellReq); got != "echo shell" {
		t.Fatalf("shell source = %q", got)
	}
	for _, req := range []domain.LoaderCommandRequest{{Mode: "exec"}, {Mode: "shell"}, {Mode: "bad"}} {
		if err := ValidateCommandRequest(req); err == nil {
			t.Fatalf("expected validation error for %#v", req)
		}
	}
	loader := domain.Loader{Summary: domain.LoaderSummary{SessionPolicy: domain.LoaderSessionPolicyReuse}}
	if CommandRequestRequiresCleanup(loader, domain.LoaderCommandRequest{}) {
		t.Fatalf("reuse policy without overrides should not require cleanup")
	}
	if !CommandRequestRequiresCleanup(loader, domain.LoaderCommandRequest{SessionPolicy: domain.LoaderSessionPolicyNew}) {
		t.Fatalf("new policy should require cleanup")
	}
	if !CommandRequestOverridesSession(domain.LoaderCommandRequest{Driver: "docker"}) ||
		!CommandRequestOverridesSession(domain.LoaderCommandRequest{SessionEnv: []domain.SessionEnvVar{{Name: "A", Value: "B"}}}) {
		t.Fatalf("expected session override detection")
	}

	published, err := NewPublishedTopicEvent("runtime.demo", `{"correlation_id":"corr","parentEventId":"parent","provider":"test","ok":true}`, TriggerEventMetadata{EventID: "trigger-event"}, "loader-1", "run-1")
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
	if _, err := NewPublishedTopicEvent("bad.topic", `{}`, TriggerEventMetadata{}, "", ""); err == nil {
		t.Fatalf("expected invalid topic error")
	}
	if _, err := NewPublishedTopicEvent("runtime.demo", `[]`, TriggerEventMetadata{}, "", ""); err == nil {
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

	loaders := []domain.Loader{
		{Summary: domain.LoaderSummary{ID: "loader-1", Enabled: true, ConcurrencyPolicy: domain.LoaderConcurrencyPolicySkip}, Triggers: []domain.LoaderTrigger{{ID: "trigger-1", Enabled: true, Kind: domain.LoaderTriggerKindEvent, Topic: "runtime.*"}}},
		{Summary: domain.LoaderSummary{ID: "loader-2", Enabled: false}, Triggers: []domain.LoaderTrigger{{ID: "disabled-loader", Enabled: true, Kind: domain.LoaderTriggerKindEvent, Topic: "runtime.demo"}}},
		{Summary: domain.LoaderSummary{ID: "loader-3", Enabled: true}, Triggers: []domain.LoaderTrigger{{ID: "disabled-trigger", Enabled: false, Kind: domain.LoaderTriggerKindEvent, Topic: "runtime.demo"}}},
	}
	targets := CollectEventTargets(loaders, "runtime.demo")
	if len(targets) != 1 || targets[0].Loader.Summary.ID != "loader-1" {
		t.Fatalf("targets = %#v", targets)
	}
	duplicated := append(targets, targets...)
	deduped := DedupeWebhookEventTargets(domain.LoaderTopicEvent{Source: domain.TopicEventSourceWebhook}, duplicated)
	if len(deduped) != 1 {
		t.Fatalf("deduped targets = %#v", deduped)
	}
	if !AnyTargetBusy(targets, map[string]int{"loader-1": 1}) {
		t.Fatalf("expected busy target")
	}
	targets[0].Loader.Summary.ConcurrencyPolicy = domain.LoaderConcurrencyPolicyParallel
	if AnyTargetBusy(targets, map[string]int{"loader-1": 1}) {
		t.Fatalf("parallel target should not be busy")
	}
}
