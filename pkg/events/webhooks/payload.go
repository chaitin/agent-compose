package webhooks

import (
	"encoding/json"

	domain "agent-compose/pkg/model"
)

func ExistingBodyHash(payloadJSON string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return ""
	}
	body, ok := payload["body"]
	if !ok {
		return ""
	}
	compact, err := domain.MarshalJSONCompact(body)
	if err != nil {
		return ""
	}
	return domain.TopicEventPayloadSHA256(compact)
}
