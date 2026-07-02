package agentcompose

import (
	"time"

	loaderdomain "agent-compose/internal/agentcompose/loader"
)

func (s *Service) publishLoaderTopic(topic string, payload map[string]any) {
	if s == nil || s.bus == nil {
		return
	}
	s.bus.Publish(LoaderTopicEvent{
		Topic:     topic,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})
}

func sessionTopicPayload(session *Session, source string) map[string]any {
	if session == nil {
		return nil
	}
	return loaderdomain.SessionTopicPayload(loaderdomain.SessionTopicFields{
		SessionID:     session.Summary.ID,
		Title:         session.Summary.Title,
		Driver:        session.Summary.Driver,
		VMStatus:      session.Summary.VMStatus,
		GuestImage:    session.Summary.GuestImage,
		TriggerSource: session.Summary.TriggerSource,
	}, source)
}

func cellTopicPayload(sessionID string, cell NotebookCell, source string) map[string]any {
	return loaderdomain.CellTopicPayload(loaderdomain.CellTopicFields{
		SessionID:      sessionID,
		CellID:         cell.ID,
		CellType:       cell.Type,
		Success:        cell.Success,
		ExitCode:       cell.ExitCode,
		Agent:          cell.Agent,
		AgentSessionID: cell.AgentSessionID,
		StopReason:     cell.StopReason,
	}, source)
}

func loaderCommandEventPayload(request LoaderCommandRequest, result LoaderCommandResult) map[string]any {
	return loaderdomain.CommandEventPayload(request, result)
}
