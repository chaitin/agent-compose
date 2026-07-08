package webhooks

import (
	"encoding/json"
	"strings"
	"time"

	domain "agent-compose/pkg/model"
)

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

func EventSessionsResponseFor(item domain.TopicEventRecord, links []domain.EventSessionTraceItem) EventSessionsResponse {
	resp := EventSessionsResponse{
		EventID:       item.ID,
		CorrelationID: item.CorrelationID,
		Sessions:      make([]EventSessionJSON, 0, len(links)),
	}
	for _, link := range links {
		resp.Sessions = append(resp.Sessions, EventSessionJSON{
			SessionID:     link.SessionID,
			Relation:      link.Relation,
			LoaderID:      link.LoaderID,
			RunID:         link.RunID,
			TriggerID:     link.TriggerID,
			LoaderEventID: link.LoaderEventID,
			EventID:       link.EventID,
			CreatedAt:     link.CreatedAt.UTC().Format(time.RFC3339Nano),
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
			EventID:   delivery.EventID,
			LoaderID:  delivery.LoaderID,
			RunID:     delivery.RunID,
			TriggerID: delivery.TriggerID,
			Status:    delivery.Status,
			Error:     delivery.Error,
			CreatedAt: delivery.CreatedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt: delivery.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return resp
}
