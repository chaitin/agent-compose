package agents

import (
	"fmt"
	"testing"
	"time"

	"agent-compose/pkg/model"
)

// TestAgentRunSummariesScansAllSessions guards against the run-summary scan
// being truncated to a recent page: an agent's running session must be found
// even when many newer non-agent sessions exist.
func TestAgentRunSummariesScansAllSessions(t *testing.T) {
	testAgentRunSummariesScansAllSessions(t)
}

func testAgentRunSummariesScansAllSessions(t *testing.T) {
	t.Helper()
	base := time.Now().UTC()
	sessions := make([]*Session, 0, 61)
	for i := 0; i < 60; i++ {
		sessions = append(sessions, &Session{Summary: model.SessionSummary{
			ID:        fmt.Sprintf("other-%d", i),
			VMStatus:  model.VMStatusStopped,
			UpdatedAt: base.Add(time.Duration(i) * time.Minute),
		}})
	}
	sessions = append(sessions, &Session{Summary: model.SessionSummary{
		ID:        "agent-session",
		Title:     "Agent Run",
		VMStatus:  model.VMStatusRunning,
		UpdatedAt: base.Add(-time.Hour),
		Tags: []model.SessionTag{
			{Name: agentSessionTagSource, Value: agentSessionTagSourceVal},
			{Name: agentSessionTagID, Value: "agent-x"},
		},
	}})
	current, latest := agentRunSummaries("agent-x", sessions)
	if current.RunningSessionCount != 1 {
		t.Fatalf("running session count = %d, want 1", current.RunningSessionCount)
	}
	if latest == nil || latest.RunID != "agent-session" || latest.Status != model.VMStatusRunning {
		t.Fatalf("latest run summary = %+v", latest)
	}
}
