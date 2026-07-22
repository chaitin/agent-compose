package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	domain "agent-compose/pkg/model"
)

type projectSchedulerEventCursor struct {
	ProjectID       string    `json:"project_id"`
	ProjectRevision int64     `json:"project_revision"`
	AgentName       string    `json:"agent_name,omitempty"`
	TriggerID       string    `json:"trigger_id,omitempty"`
	RunID           string    `json:"run_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	LoaderID        string    `json:"loader_id"`
	EventID         string    `json:"event_id"`
}

func encodeProjectSchedulerEventCursor(projectID string, projectRevision int64, agentName, triggerID, runID string, event domain.LoaderEvent) string {
	payload, _ := json.Marshal(projectSchedulerEventCursor{
		ProjectID:       strings.TrimSpace(projectID),
		ProjectRevision: projectRevision,
		AgentName:       strings.TrimSpace(agentName),
		TriggerID:       strings.TrimSpace(triggerID),
		RunID:           strings.TrimSpace(runID),
		CreatedAt:       event.CreatedAt.UTC(),
		LoaderID:        strings.TrimSpace(event.LoaderID),
		EventID:         strings.TrimSpace(event.ID),
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeProjectSchedulerEventCursor(token, projectID string, projectRevision int64, agentName, triggerID, runID string) (projectSchedulerEventCursor, error) {
	expected := projectSchedulerEventCursor{
		ProjectID:       strings.TrimSpace(projectID),
		ProjectRevision: projectRevision,
		AgentName:       strings.TrimSpace(agentName),
		TriggerID:       strings.TrimSpace(triggerID),
		RunID:           strings.TrimSpace(runID),
	}
	if strings.TrimSpace(token) == "" {
		return expected, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return projectSchedulerEventCursor{}, fmt.Errorf("invalid cursor")
	}
	var cursor projectSchedulerEventCursor
	if json.Unmarshal(payload, &cursor) != nil || cursor.ProjectID != expected.ProjectID || cursor.ProjectRevision != expected.ProjectRevision || cursor.AgentName != expected.AgentName ||
		cursor.TriggerID != expected.TriggerID || cursor.RunID != expected.RunID || cursor.CreatedAt.IsZero() ||
		strings.TrimSpace(cursor.LoaderID) == "" || strings.TrimSpace(cursor.EventID) == "" {
		return projectSchedulerEventCursor{}, fmt.Errorf("invalid cursor")
	}
	return cursor, nil
}
