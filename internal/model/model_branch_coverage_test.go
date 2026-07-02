package model

import (
	driverpkg "agent-compose/pkg/driver"
	"testing"
	"time"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func TestModelSessionConfigAndBusBranchCoverage(t *testing.T) {
	now := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)
	session := &Session{
		Summary: SessionSummary{
			ID:            "session-branch",
			Title:         "Branch Session",
			TriggerSource: "script:loader-1",
			Driver:        driverpkg.RuntimeDriverDocker,
			VMStatus:      VMStatusRunning,
			WorkspacePath: "/workspaces/branch",
			CreatedAt:     now,
			UpdatedAt:     now.Add(time.Minute),
		},
		WorkspaceID: "workspace-1",
		Workspace:   &SessionWorkspace{ID: "workspace-1", Name: "Workspace One", Type: "file"},
	}
	if !sessionMatchesListOptions(session, SessionListOptions{
		SessionType:        SessionTypeScript,
		TriggerSourceQuery: "loader",
		TitleQuery:         "branch",
		WorkspaceQuery:     "workspace one",
		Driver:             driverpkg.RuntimeDriverDocker,
		VMStatus:           VMStatusRunning,
		CreatedFrom:        now.Add(-time.Second),
		CreatedTo:          now.Add(time.Second),
		UpdatedFrom:        now,
		UpdatedTo:          now.Add(2 * time.Minute),
	}) {
		t.Fatalf("session should match full list options")
	}
	for _, options := range []SessionListOptions{
		{SessionType: SessionTypeManual},
		{TriggerSourceQuery: "missing"},
		{TitleQuery: "missing"},
		{WorkspaceQuery: "missing"},
		{Driver: driverpkg.RuntimeDriverBoxlite},
		{VMStatus: VMStatusStopped},
		{CreatedFrom: now.Add(time.Second)},
		{CreatedTo: now.Add(-time.Second)},
		{UpdatedFrom: now.Add(2 * time.Minute)},
		{UpdatedTo: now.Add(-time.Second)},
	} {
		if sessionMatchesListOptions(session, options) {
			t.Fatalf("session unexpectedly matched options %#v", options)
		}
	}
	if sessionMatchesListOptions(nil, SessionListOptions{}) {
		t.Fatalf("nil session matched list options")
	}
	if got := normalizeSessionTriggerSource("", []SessionTag{{Name: "origin", Value: "loader"}, {Name: "loader_id", Value: "loader-9"}}); got != "script:loader-9" {
		t.Fatalf("normalizeSessionTriggerSource tags = %q", got)
	}
	if got := paginateSessions([]*Session{session}, 5, 10); got != nil {
		t.Fatalf("paginateSessions beyond end = %#v", got)
	}
	offset, limit := normalizeSessionListBounds(-1, 0)
	if offset != 0 || limit != defaultSessionListLimit {
		t.Fatalf("normalizeSessionListBounds = %d/%d", offset, limit)
	}

	parsed, err := sessionListOptionsFromProto(&agentcomposev1.ListSessionsRequest{
		SessionType:        SessionTypeScript,
		TriggerSourceQuery: "script",
		TitleQuery:         "title",
		WorkspaceQuery:     "workspace",
		Driver:             driverpkg.RuntimeDriverDocker,
		VmStatus:           VMStatusRunning,
		CreatedFrom:        now.Format(time.RFC3339),
		CreatedTo:          now.Add(time.Hour).Format(time.RFC3339),
		UpdatedFrom:        now.Format(time.RFC3339),
		UpdatedTo:          now.Add(time.Hour).Format(time.RFC3339),
		Offset:             3,
		Limit:              7,
	})
	if err != nil {
		t.Fatalf("sessionListOptionsFromProto returned error: %v", err)
	}
	if parsed.Offset != 3 || parsed.Limit != 7 || parsed.CreatedFrom.IsZero() || parsed.UpdatedTo.IsZero() {
		t.Fatalf("parsed session options = %#v", parsed)
	}
	if _, err := sessionListOptionsFromProto(&agentcomposev1.ListSessionsRequest{CreatedFrom: "bad"}); err == nil {
		t.Fatalf("invalid created_from returned nil error")
	}
	if value, err := parseOptionalRFC3339(" ", "field"); err != nil || !value.IsZero() {
		t.Fatalf("parseOptionalRFC3339 blank = %s/%v", value, err)
	}

	if got := sessionEnvMap([]SessionEnvVar{{Name: " A ", Value: "1"}, {Name: " ", Value: "skip"}}); got["A"] != "1" || len(got) != 1 {
		t.Fatalf("sessionEnvMap = %#v", got)
	}
	if sessionEnvMap(nil) != nil {
		t.Fatalf("sessionEnvMap nil did not return nil")
	}
	mergedEnv := mergeEnvItems([]SessionEnvVar{{Name: "A", Value: "global"}}, []SessionEnvVar{{Name: "A", Value: "session"}, {Name: "B", Value: "session"}})
	if len(mergedEnv) != 2 || mergedEnv[0].Value != "session" || mergedEnv[1].Name != "B" {
		t.Fatalf("mergeEnvItems = %#v", mergedEnv)
	}
	if mergeEnvItems(nil, nil) != nil {
		t.Fatalf("mergeEnvItems nil did not return nil")
	}
}
