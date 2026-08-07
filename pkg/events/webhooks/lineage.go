package webhooks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	domain "agent-compose/pkg/model"
)

var (
	errWebhookParentNotFound            = errors.New("parent event does not exist")
	errWebhookParentCorrelationMismatch = errors.New("parent event correlation does not match")
)

// webhookLineageRequest preserves the caller's delivery identity before an
// event ID is allocated. An empty CorrelationID means the request omitted a
// correlation and has no parent, so a newly created event will use its own ID.
type webhookLineageRequest struct {
	ParentEventID string
	CorrelationID string
}

func (h routeHandler) resolveWebhookLineage(ctx context.Context, request *http.Request, body map[string]any) (webhookLineageRequest, error) {
	lineage := webhookLineageRequest{
		ParentEventID: strings.TrimSpace(ExtractParentEventID(request)),
		CorrelationID: strings.TrimSpace(ExtractCorrelationID(request, body)),
	}
	if lineage.ParentEventID == "" {
		return lineage, nil
	}
	parent, err := h.store().GetEvent(ctx, lineage.ParentEventID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return webhookLineageRequest{}, errWebhookParentNotFound
		}
		return webhookLineageRequest{}, fmt.Errorf("get parent event: %w", err)
	}
	parentCorrelationID := strings.TrimSpace(parent.CorrelationID)
	if parentCorrelationID == "" {
		parentCorrelationID = strings.TrimSpace(parent.ID)
	}
	if lineage.CorrelationID == "" {
		lineage.CorrelationID = parentCorrelationID
	} else if lineage.CorrelationID != parentCorrelationID {
		return webhookLineageRequest{}, errWebhookParentCorrelationMismatch
	}
	return lineage, nil
}

func webhookDeliveryMatches(existing domain.TopicEventRecord, compactBody string, lineage webhookLineageRequest) bool {
	if ExistingBodyHash(existing.PayloadJSON) != domain.TopicEventPayloadSHA256(compactBody) {
		return false
	}
	if strings.TrimSpace(existing.ParentEventID) != lineage.ParentEventID {
		return false
	}
	if lineage.CorrelationID != "" {
		return strings.TrimSpace(existing.CorrelationID) == lineage.CorrelationID
	}
	return lineage.ParentEventID == "" && strings.TrimSpace(existing.CorrelationID) == strings.TrimSpace(existing.ID)
}
