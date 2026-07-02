package loader

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	eventdomain "agent-compose/internal/agentcompose/events"
)

type TriggerEventMetadata struct {
	EventID       string
	Sequence      int64
	CorrelationID string
}

type SessionTopicFields struct {
	SessionID     string
	Title         string
	Driver        string
	VMStatus      string
	GuestImage    string
	TriggerSource string
}

type CellTopicFields struct {
	SessionID      string
	CellID         string
	CellType       string
	Success        bool
	ExitCode       int
	Agent          string
	AgentSessionID string
	StopReason     string
}

func SessionTopicPayload(fields SessionTopicFields, source string) map[string]any {
	return map[string]any{
		"sessionId":     fields.SessionID,
		"title":         fields.Title,
		"driver":        fields.Driver,
		"vmStatus":      fields.VMStatus,
		"guestImage":    fields.GuestImage,
		"triggerSource": fields.TriggerSource,
		"source":        source,
	}
}

func CellTopicPayload(fields CellTopicFields, source string) map[string]any {
	return map[string]any{
		"sessionId":      fields.SessionID,
		"cellId":         fields.CellID,
		"cellType":       fields.CellType,
		"success":        fields.Success,
		"exitCode":       fields.ExitCode,
		"agent":          fields.Agent,
		"agentSessionId": fields.AgentSessionID,
		"stopReason":     fields.StopReason,
		"source":         source,
	}
}

func CommandEventPayload(request CommandRequest, result CommandResult) map[string]any {
	payload := map[string]any{
		"mode":            strings.TrimSpace(request.Mode),
		"command":         strings.TrimSpace(request.Command),
		"args":            append([]string(nil), request.Args...),
		"cwd":             strings.TrimSpace(request.Cwd),
		"exitCode":        result.ExitCode,
		"success":         result.Success,
		"stdoutTruncated": result.StdoutTruncated,
		"stderrTruncated": result.StderrTruncated,
		"sessionId":       result.SessionID,
		"cellId":          result.CellID,
	}
	if payload["mode"] == "shell" {
		payload["command"] = ""
	}
	return payload
}

func NormalizeJSONDocument(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(raw)); err != nil {
		return "", fmt.Errorf("normalize json document: %w", err)
	}
	return compact.String(), nil
}

func ParseTriggerEventMetadata(payloadJSON string) TriggerEventMetadata {
	payloadJSON = strings.TrimSpace(payloadJSON)
	if payloadJSON == "" {
		return TriggerEventMetadata{}
	}
	var envelope struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &envelope); err != nil {
		return TriggerEventMetadata{}
	}
	return TriggerEventMetadata{
		EventID:       StringFromMap(envelope.Payload, "eventId"),
		CorrelationID: StringFromMap(envelope.Payload, "correlationId"),
		Sequence:      Int64FromMap(envelope.Payload, "sequence"),
	}
}

func ValidatePublishTopic(topic string) error {
	if err := eventdomain.ValidateTopicEventName(topic); err != nil {
		return err
	}
	if strings.HasPrefix(topic, "runtime.") || strings.HasPrefix(topic, "workflow.") || strings.HasPrefix(topic, "external.") {
		return nil
	}
	return fmt.Errorf("loader event topic must use runtime.*, workflow.*, or external.* prefix")
}

func JSONObjectDocument(payloadJSON string) bool {
	var payload map[string]any
	return json.Unmarshal([]byte(payloadJSON), &payload) == nil && payload != nil
}

func StringFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func Int64FromMap(values map[string]any, key string) int64 {
	if values == nil {
		return 0
	}
	switch value := values[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}

func SessionRPCLinkedSessionID(method, requestJSON, responseJSON string) string {
	if value := SessionIDFromJSON(responseJSON); value != "" {
		return value
	}
	if strings.TrimSpace(method) == "ListSessions" {
		return ""
	}
	return SessionIDFromJSON(requestJSON)
}

func SessionIDFromJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	if value, ok := payload["sessionId"].(string); ok {
		return strings.TrimSpace(value)
	}
	sessionValue, ok := payload["session"].(map[string]any)
	if !ok {
		return ""
	}
	summaryValue, ok := sessionValue["summary"].(map[string]any)
	if !ok {
		return ""
	}
	if value, ok := summaryValue["sessionId"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}
