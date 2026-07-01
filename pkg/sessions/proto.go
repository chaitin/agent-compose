package sessions

import (
	"fmt"
	"strings"
	"time"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func sessionListOptionsFromProto(req *agentcomposev1.ListSessionsRequest) (SessionListOptions, error) {
	if req == nil {
		return SessionListOptions{}, nil
	}
	createdFrom, err := parseOptionalRFC3339(req.GetCreatedFrom(), "created_from")
	if err != nil {
		return SessionListOptions{}, err
	}
	createdTo, err := parseOptionalRFC3339(req.GetCreatedTo(), "created_to")
	if err != nil {
		return SessionListOptions{}, err
	}
	updatedFrom, err := parseOptionalRFC3339(req.GetUpdatedFrom(), "updated_from")
	if err != nil {
		return SessionListOptions{}, err
	}
	updatedTo, err := parseOptionalRFC3339(req.GetUpdatedTo(), "updated_to")
	if err != nil {
		return SessionListOptions{}, err
	}
	return SessionListOptions{
		SessionType:        req.GetSessionType(),
		TriggerSourceQuery: req.GetTriggerSourceQuery(),
		TitleQuery:         req.GetTitleQuery(),
		WorkspaceQuery:     req.GetWorkspaceQuery(),
		Driver:             req.GetDriver(),
		VMStatus:           req.GetVmStatus(),
		CreatedFrom:        createdFrom,
		CreatedTo:          createdTo,
		UpdatedFrom:        updatedFrom,
		UpdatedTo:          updatedTo,
		Offset:             int(req.GetOffset()),
		Limit:              int(req.GetLimit()),
	}, nil
}

func SessionListOptionsFromProto(req *agentcomposev1.ListSessionsRequest) (SessionListOptions, error) {
	return sessionListOptionsFromProto(req)
}

func parseOptionalRFC3339(raw, field string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s: %w", field, err)
	}
	return value.UTC(), nil
}

func ParseOptionalRFC3339(raw, field string) (time.Time, error) {
	return parseOptionalRFC3339(raw, field)
}

func toProtoSessionDetail(session *Session) *agentcomposev1.SessionDetail {
	resp := &agentcomposev1.SessionDetail{Summary: toProtoSessionSummary(&session.Summary), WorkspaceId: session.WorkspaceID, Workspace: toProtoSessionWorkspace(session.Workspace)}
	for _, item := range session.EnvItems {
		value := item.Value
		if item.Secret && value != "" {
			value = "********"
		}
		resp.EnvItems = append(resp.EnvItems, &agentcomposev1.SessionEnvVar{Name: item.Name, Value: value, Secret: item.Secret})
	}
	return resp
}

func toProtoSessionSummary(summary *SessionSummary) *agentcomposev1.SessionSummary {
	resp := &agentcomposev1.SessionSummary{
		SessionId:     summary.ID,
		Title:         summary.Title,
		TriggerSource: summary.TriggerSource,
		Driver:        summary.Driver,
		VmStatus:      summary.VMStatus,
		GuestImage:    summary.GuestImage,
		WorkspacePath: summary.WorkspacePath,
		ProxyPath:     summary.ProxyPath,
		CreatedAt:     summary.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:     summary.UpdatedAt.Format(time.RFC3339Nano),
		CellCount:     uint32(summary.CellCount),
		EventCount:    uint32(summary.EventCount),
	}
	for _, tag := range summary.Tags {
		resp.Tags = append(resp.Tags, &agentcomposev1.SessionTag{Name: tag.Name, Value: tag.Value})
	}
	return resp
}

func toProtoSessionWorkspace(item *SessionWorkspace) *agentcomposev1.SessionWorkspaceSnapshot {
	if item == nil {
		return nil
	}
	return &agentcomposev1.SessionWorkspaceSnapshot{
		Id:         item.ID,
		Name:       item.Name,
		Type:       item.Type,
		ConfigJson: item.ConfigJSON,
	}
}

func toProtoCell(cell NotebookCell) *agentcomposev1.NotebookCell {
	return &agentcomposev1.NotebookCell{
		Id:             cell.ID,
		Source:         cell.Source,
		Stdout:         cell.Stdout,
		Stderr:         cell.Stderr,
		Output:         firstNonEmpty(cell.Output, cell.Stdout+cell.Stderr),
		Success:        cell.Success,
		CreatedAt:      cell.CreatedAt.Format(time.RFC3339Nano),
		Type:           toProtoCellType(cell.Type),
		ExitCode:       int32(cell.ExitCode),
		Agent:          cell.Agent,
		AgentSessionId: cell.AgentSessionID,
		StopReason:     cell.StopReason,
		Running:        cell.Running,
	}
}

func toProtoAgentRun(cell NotebookCell) *agentcomposev1.AgentRun {
	return &agentcomposev1.AgentRun{
		Id:             cell.ID,
		Agent:          cell.Agent,
		Message:        cell.Source,
		Output:         firstNonEmpty(cell.Output, cell.Stdout+cell.Stderr),
		ExitCode:       int32(cell.ExitCode),
		Success:        cell.Success,
		CreatedAt:      cell.CreatedAt.Format(time.RFC3339Nano),
		AgentSessionId: cell.AgentSessionID,
		StopReason:     cell.StopReason,
		Running:        cell.Running,
	}
}

func fromProtoCellType(cellType agentcomposev1.CellType) string {
	switch cellType {
	case agentcomposev1.CellType_CELL_TYPE_SHELL:
		return CellTypeShell
	case agentcomposev1.CellType_CELL_TYPE_PYTHON:
		return CellTypePython
	case agentcomposev1.CellType_CELL_TYPE_AGENT:
		return CellTypeAgent
	case agentcomposev1.CellType_CELL_TYPE_JAVASCRIPT, agentcomposev1.CellType_CELL_TYPE_UNSPECIFIED:
		return CellTypeJavaScript
	default:
		return CellTypeJavaScript
	}
}

func toProtoWatchSessionResponse(event sessionWatchEvent) *agentcomposev1.WatchSessionResponse {
	resp := &agentcomposev1.WatchSessionResponse{
		Chunk:    event.Chunk,
		IsStderr: event.IsStderr,
		CellId:   event.CellID,
	}
	switch event.EventType {
	case sessionWatchEventTypeSessionUpdated:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_SESSION_UPDATED
		if event.Session != nil {
			resp.Session = toProtoSessionSummary(event.Session)
		}
	case sessionWatchEventTypeCellStarted:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_CELL_STARTED
		if event.Cell != nil {
			resp.Cell = toProtoCell(*event.Cell)
			resp.CellId = event.Cell.ID
		}
	case sessionWatchEventTypeCellOutput:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_CELL_OUTPUT
	case sessionWatchEventTypeCellCompleted:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_CELL_COMPLETED
		if event.Cell != nil {
			resp.Cell = toProtoCell(*event.Cell)
			resp.CellId = event.Cell.ID
		}
	case sessionWatchEventTypeEventAdded:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_EVENT_ADDED
		if event.Event != nil {
			resp.Event = toProtoEvent(*event.Event)
		}
	default:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_UNSPECIFIED
	}
	return resp
}

func toProtoCellType(cellType string) agentcomposev1.CellType {
	switch cellType {
	case CellTypeShell:
		return agentcomposev1.CellType_CELL_TYPE_SHELL
	case CellTypePython:
		return agentcomposev1.CellType_CELL_TYPE_PYTHON
	case CellTypeAgent:
		return agentcomposev1.CellType_CELL_TYPE_AGENT
	case CellTypeJavaScript:
		fallthrough
	default:
		return agentcomposev1.CellType_CELL_TYPE_JAVASCRIPT
	}
}

func toProtoEvent(event SessionEvent) *agentcomposev1.SessionEvent {
	return &agentcomposev1.SessionEvent{
		Id:        event.ID,
		Type:      event.Type,
		Level:     event.Level,
		Message:   event.Message,
		CreatedAt: event.CreatedAt.Format(time.RFC3339Nano),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
