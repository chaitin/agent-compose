package webhooks

import (
	"encoding/json"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

const idempotencyPayloadMismatchMessage = "idempotency key conflicts with existing payload"

func acceptedResponseFor(item domain.TopicEventRecord) AcceptedResponse {
	return AcceptedResponse{
		Accepted:      true,
		Topic:         item.Topic,
		EventID:       item.ID,
		Sequence:      item.Sequence,
		CorrelationID: item.CorrelationID,
	}
}

func idempotencyConflictResponseFor(item domain.TopicEventRecord) idempotencyConflictResponse {
	return idempotencyConflictResponse{
		Code:          idempotencyPayloadMismatchCode,
		Error:         idempotencyPayloadMismatchMessage,
		ExistingEvent: acceptedResponseFor(item),
	}
}

func SourceToJSON(source domain.WebhookSource) SourceJSON {
	return SourceJSON{
		ID:                 source.ID,
		Name:               source.Name,
		Enabled:            source.Enabled,
		Provider:           source.Provider,
		TopicPrefix:        source.TopicPrefix,
		HasToken:           strings.TrimSpace(source.TokenHash) != "",
		TokenHeader:        source.TokenHeader,
		SignatureType:      source.SignatureType,
		HasSignatureSecret: strings.TrimSpace(source.SignatureSecret) != "",
		BodyLimitBytes:     source.BodyLimitBytes,
		CreatedAt:          source.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:          source.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func TopicEventToJSON(item domain.TopicEventRecord) TopicEventJSON {
	payload := make(map[string]any)
	_ = json.Unmarshal([]byte(item.PayloadJSON), &payload)
	out := TopicEventJSON{
		EventID:        item.ID,
		Sequence:       item.Sequence,
		Topic:          item.Topic,
		Source:         item.Source,
		Provider:       item.Provider,
		Intent:         item.Intent,
		CorrelationID:  item.CorrelationID,
		IdempotencyKey: item.IdempotencyKey,
		DeliveryID:     item.DeliveryID,
		DispatchStatus: item.DispatchStatus,
		ParentEventID:  item.ParentEventID,
		PublisherType:  item.PublisherType,
		PublisherID:    item.PublisherID,
		PublisherRunID: item.PublisherRunID,
		CreatedAt:      item.CreatedAt.UTC().Format(time.RFC3339Nano),
		Payload:        payload,
	}
	if !item.DispatchedAt.IsZero() {
		out.DispatchedAt = item.DispatchedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func eventSummaryToJSON(item domain.EventSummary) EventSummaryJSON {
	out := EventSummaryJSON{
		EventID:        item.ID,
		Sequence:       item.Sequence,
		Topic:          item.Topic,
		Source:         item.Source,
		Provider:       item.Provider,
		Intent:         item.Intent,
		CorrelationID:  item.CorrelationID,
		DeliveryID:     item.DeliveryID,
		DispatchStatus: item.DispatchStatus,
		ParentEventID:  item.ParentEventID,
		PublisherType:  item.PublisherType,
		PublisherID:    item.PublisherID,
		PublisherRunID: item.PublisherRunID,
		CreatedAt:      formatEventTime(item.CreatedAt),
	}
	if !item.DispatchedAt.IsZero() {
		out.DispatchedAt = formatEventTime(item.DispatchedAt)
	}
	return out
}

func eventTraceResponseFor(view eventTraceView) EventTraceResponse {
	resp := EventTraceResponse{
		SandboxSummariesIncomplete: view.SandboxSummaryError != nil,
		Event:                      eventSummaryToJSON(view.Trace.Event),
		Runs:                       make([]EventRunTraceJSON, 0, len(view.Trace.Runs)),
		Sandboxes:                  make([]EventTraceSandboxJSON, 0, len(view.Trace.SandboxLinks)),
		DescendantsTruncated:       view.Trace.DescendantsTruncated,
	}
	schedulers := make(map[string]domain.EventSchedulerSummary)
	for _, trace := range view.Trace.Runs {
		item := EventRunTraceJSON{
			Delivery: eventDeliveryToJSON(trace.Delivery),
			Events:   make([]EventSchedulerEventJSON, 0, len(trace.Events)),
		}
		if trace.Scheduler != nil {
			schedulers[trace.Scheduler.ID] = *trace.Scheduler
			item.Scheduler = &EventSchedulerJSON{
				SchedulerID: trace.Scheduler.ID,
				ProjectID:   trace.Scheduler.ProjectID,
				AgentName:   trace.Scheduler.AgentName,
				Name:        trace.Scheduler.Name,
			}
		}
		if trace.Run != nil {
			item.Run = &EventSchedulerRunJSON{
				RunID:       trace.Run.ID,
				Status:      trace.Run.Status,
				StartedAt:   formatEventTime(trace.Run.StartedAt),
				CompletedAt: formatEventTime(trace.Run.CompletedAt),
				DurationMs:  trace.Run.DurationMs,
				Error:       trace.Run.Error,
			}
		}
		for _, event := range trace.Events {
			item.Events = append(item.Events, EventSchedulerEventJSON{
				EventID:         event.ID,
				Type:            event.Type,
				Level:           event.Level,
				Message:         event.Message,
				LinkedSandboxID: event.LinkedSandboxID,
				CreatedAt:       formatEventTime(event.CreatedAt),
			})
		}
		resp.Runs = append(resp.Runs, item)
	}
	for _, link := range view.Trace.SandboxLinks {
		item := EventTraceSandboxJSON{EventSandboxJSON: eventSandboxToJSON(link)}
		if summary, ok := view.Sandboxes[link.SandboxID]; ok {
			item.Sandbox = &EventSandboxSummaryJSON{
				SandboxID: summary.ID,
				Title:     summary.Title,
				Status:    summary.VMStatus,
				CreatedAt: formatEventTime(summary.CreatedAt),
				UpdatedAt: formatEventTime(summary.UpdatedAt),
			}
			if scheduler, ok := schedulers[link.SchedulerID]; ok {
				item.Sandbox.ProjectID = scheduler.ProjectID
				item.Sandbox.AgentName = scheduler.AgentName
			}
		}
		resp.Sandboxes = append(resp.Sandboxes, item)
	}
	return resp
}

func eventDeliveryToJSON(delivery domain.EventDelivery) EventRunJSON {
	return EventRunJSON{
		EventID:     delivery.EventID,
		SchedulerID: delivery.SchedulerID,
		RunID:       delivery.RunID,
		TriggerID:   delivery.TriggerID,
		Status:      delivery.Status,
		Error:       delivery.Error,
		CreatedAt:   formatEventTime(delivery.CreatedAt),
		UpdatedAt:   formatEventTime(delivery.UpdatedAt),
	}
}

func eventSandboxToJSON(link domain.EventSandboxTraceItem) EventSandboxJSON {
	return EventSandboxJSON{
		SandboxID:        link.SandboxID,
		Relation:         link.Relation,
		SchedulerID:      link.SchedulerID,
		RunID:            link.RunID,
		TriggerID:        link.TriggerID,
		SchedulerEventID: link.SchedulerEventID,
		EventID:          link.EventID,
		CreatedAt:        formatEventTime(link.CreatedAt),
	}
}

func formatEventTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func EventSandboxesResponseFor(item domain.TopicEventRecord, links []domain.EventSandboxTraceItem) EventSandboxesResponse {
	resp := EventSandboxesResponse{
		EventID:       item.ID,
		CorrelationID: item.CorrelationID,
		Sandboxes:     make([]EventSandboxJSON, 0, len(links)),
	}
	for _, link := range links {
		resp.Sandboxes = append(resp.Sandboxes, EventSandboxJSON{
			SandboxID:        link.SandboxID,
			Relation:         link.Relation,
			SchedulerID:      link.SchedulerID,
			RunID:            link.RunID,
			TriggerID:        link.TriggerID,
			SchedulerEventID: link.SchedulerEventID,
			EventID:          link.EventID,
			CreatedAt:        link.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return resp
}

func EventRunsResponseFor(item domain.TopicEventRecord, deliveries []domain.EventDelivery) EventRunsResponse {
	resp := EventRunsResponse{
		EventID:       item.ID,
		CorrelationID: item.CorrelationID,
		Runs:          make([]EventRunJSON, 0, len(deliveries)),
	}
	for _, delivery := range deliveries {
		resp.Runs = append(resp.Runs, EventRunJSON{
			EventID:     delivery.EventID,
			SchedulerID: delivery.SchedulerID,
			RunID:       delivery.RunID,
			TriggerID:   delivery.TriggerID,
			Status:      delivery.Status,
			Error:       delivery.Error,
			CreatedAt:   delivery.CreatedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt:   delivery.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return resp
}
