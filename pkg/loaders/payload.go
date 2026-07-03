package loaders

import (
	"strings"

	domain "agent-compose/pkg/model"
)

func SessionTopicPayload(session *domain.Session, source string) map[string]any {
	if session == nil {
		return nil
	}
	return map[string]any{
		"sessionId":     session.Summary.ID,
		"title":         session.Summary.Title,
		"driver":        session.Summary.Driver,
		"vmStatus":      session.Summary.VMStatus,
		"guestImage":    session.Summary.GuestImage,
		"triggerSource": session.Summary.TriggerSource,
		"source":        source,
	}
}

func CellTopicPayload(sessionID string, cell domain.NotebookCell, source string) map[string]any {
	return map[string]any{
		"sessionId":      sessionID,
		"cellId":         cell.ID,
		"cellType":       cell.Type,
		"success":        cell.Success,
		"exitCode":       cell.ExitCode,
		"agent":          cell.Agent,
		"agentSessionId": cell.AgentSessionID,
		"stopReason":     cell.StopReason,
		"source":         source,
	}
}

func CommandEventPayload(request domain.LoaderCommandRequest, result domain.LoaderCommandResult) map[string]any {
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
