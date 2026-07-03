package api

import (
	"time"

	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func SessionDetailToProto(session *domain.Session) *agentcomposev1.SessionDetail {
	resp := &agentcomposev1.SessionDetail{Summary: SessionSummaryToProto(&session.Summary), WorkspaceId: session.WorkspaceID, Workspace: SessionWorkspaceToProto(session.Workspace)}
	for _, item := range session.EnvItems {
		value := item.Value
		if item.Secret && value != "" {
			value = "********"
		}
		resp.EnvItems = append(resp.EnvItems, &agentcomposev1.SessionEnvVar{Name: item.Name, Value: value, Secret: item.Secret})
	}
	return resp
}

func SessionSummaryToProto(summary *domain.SessionSummary) *agentcomposev1.SessionSummary {
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

func GlobalEnvConfigToProto(items []domain.SessionEnvVar) *agentcomposev1.GlobalEnvConfigResponse {
	resp := &agentcomposev1.GlobalEnvConfigResponse{}
	for _, item := range items {
		value := item.Value
		if item.Secret && value != "" {
			value = "********"
		}
		resp.EnvItems = append(resp.EnvItems, &agentcomposev1.SessionEnvVar{Name: item.Name, Value: value, Secret: item.Secret})
	}
	return resp
}

func SessionWorkspaceToProto(item *domain.SessionWorkspace) *agentcomposev1.SessionWorkspaceSnapshot {
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

func WorkspaceConfigToProto(item domain.WorkspaceConfig) *agentcomposev1.WorkspaceConfig {
	return &agentcomposev1.WorkspaceConfig{
		Id:         item.ID,
		Name:       item.Name,
		Type:       item.Type,
		ConfigJson: item.ConfigJSON,
		Comment:    item.Comment,
		CreatedAt:  item.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:  item.UpdatedAt.Format(time.RFC3339Nano),
	}
}

func CellToProto(cell domain.NotebookCell) *agentcomposev1.NotebookCell {
	return &agentcomposev1.NotebookCell{
		Id:             cell.ID,
		Source:         cell.Source,
		Stdout:         cell.Stdout,
		Stderr:         cell.Stderr,
		Output:         firstNonEmpty(cell.Output, cell.Stdout+cell.Stderr),
		Success:        cell.Success,
		CreatedAt:      cell.CreatedAt.Format(time.RFC3339Nano),
		Type:           CellTypeToProto(cell.Type),
		ExitCode:       int32(cell.ExitCode),
		Agent:          cell.Agent,
		AgentSessionId: cell.AgentSessionID,
		StopReason:     cell.StopReason,
		Running:        cell.Running,
	}
}

func AgentRunToProto(cell domain.NotebookCell) *agentcomposev1.AgentRun {
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

func CellTypeFromProto(cellType agentcomposev1.CellType) string {
	switch cellType {
	case agentcomposev1.CellType_CELL_TYPE_SHELL:
		return execution.CellTypeShell
	case agentcomposev1.CellType_CELL_TYPE_PYTHON:
		return execution.CellTypePython
	case agentcomposev1.CellType_CELL_TYPE_AGENT:
		return execution.CellTypeAgent
	case agentcomposev1.CellType_CELL_TYPE_JAVASCRIPT, agentcomposev1.CellType_CELL_TYPE_UNSPECIFIED:
		return execution.CellTypeJavaScript
	default:
		return execution.CellTypeJavaScript
	}
}

func WatchSessionResponseToProto(event sessions.WatchEvent) *agentcomposev1.WatchSessionResponse {
	resp := &agentcomposev1.WatchSessionResponse{
		Chunk:    event.Chunk,
		IsStderr: event.IsStderr,
		CellId:   event.CellID,
	}
	switch event.EventType {
	case sessions.WatchEventTypeSessionUpdated:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_SESSION_UPDATED
		if event.Session != nil {
			resp.Session = SessionSummaryToProto(event.Session)
		}
	case sessions.WatchEventTypeCellStarted:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_CELL_STARTED
		if event.Cell != nil {
			resp.Cell = CellToProto(*event.Cell)
			resp.CellId = event.Cell.ID
		}
	case sessions.WatchEventTypeCellOutput:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_CELL_OUTPUT
	case sessions.WatchEventTypeCellCompleted:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_CELL_COMPLETED
		if event.Cell != nil {
			resp.Cell = CellToProto(*event.Cell)
			resp.CellId = event.Cell.ID
		}
	case sessions.WatchEventTypeEventAdded:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_EVENT_ADDED
		if event.Event != nil {
			resp.Event = SessionEventToProto(*event.Event)
		}
	default:
		resp.EventType = agentcomposev1.WatchSessionEventType_WATCH_SESSION_EVENT_TYPE_UNSPECIFIED
	}
	return resp
}

func CellTypeToProto(cellType string) agentcomposev1.CellType {
	switch cellType {
	case execution.CellTypeShell:
		return agentcomposev1.CellType_CELL_TYPE_SHELL
	case execution.CellTypePython:
		return agentcomposev1.CellType_CELL_TYPE_PYTHON
	case execution.CellTypeAgent:
		return agentcomposev1.CellType_CELL_TYPE_AGENT
	case execution.CellTypeJavaScript:
		fallthrough
	default:
		return agentcomposev1.CellType_CELL_TYPE_JAVASCRIPT
	}
}

func SessionEventToProto(event domain.SessionEvent) *agentcomposev1.SessionEvent {
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
