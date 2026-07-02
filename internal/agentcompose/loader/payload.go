package loader

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

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
