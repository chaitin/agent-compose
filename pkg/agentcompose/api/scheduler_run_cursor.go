package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	domain "agent-compose/pkg/model"
)

type schedulerRunPageCursor struct {
	ProjectID       string    `json:"project_id"`
	ProjectRevision int64     `json:"project_revision"`
	AgentName       string    `json:"agent_name,omitempty"`
	TriggerID       string    `json:"trigger_id,omitempty"`
	Status          string    `json:"status,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	LoaderID        string    `json:"loader_id"`
	RunID           string    `json:"run_id"`
}

func encodeSchedulerRunCursor(projectID string, projectRevision int64, agentName, triggerID, status string, run domain.LoaderRunSummary) string {
	payload, _ := json.Marshal(schedulerRunPageCursor{
		ProjectID:       strings.TrimSpace(projectID),
		ProjectRevision: projectRevision,
		AgentName:       strings.TrimSpace(agentName),
		TriggerID:       strings.TrimSpace(triggerID),
		Status:          strings.TrimSpace(status),
		StartedAt:       run.StartedAt.UTC(),
		LoaderID:        strings.TrimSpace(run.LoaderID),
		RunID:           strings.TrimSpace(run.ID),
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeSchedulerRunCursor(token, projectID string, projectRevision int64, agentName, triggerID, status string) (schedulerRunPageCursor, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	triggerID = strings.TrimSpace(triggerID)
	status = strings.TrimSpace(status)
	if strings.TrimSpace(token) == "" {
		return schedulerRunPageCursor{ProjectID: projectID, ProjectRevision: projectRevision, AgentName: agentName, TriggerID: triggerID, Status: status}, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return schedulerRunPageCursor{}, fmt.Errorf("invalid cursor")
	}
	var cursor schedulerRunPageCursor
	if json.Unmarshal(payload, &cursor) != nil ||
		cursor.ProjectID != projectID || cursor.ProjectRevision != projectRevision || cursor.AgentName != agentName || cursor.TriggerID != triggerID || cursor.Status != status ||
		cursor.StartedAt.IsZero() || strings.TrimSpace(cursor.LoaderID) == "" || strings.TrimSpace(cursor.RunID) == "" {
		return schedulerRunPageCursor{}, fmt.Errorf("invalid cursor")
	}
	return cursor, nil
}
