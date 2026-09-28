package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type schedulerRunIdentityStore struct {
	schedulerIDLookupStore
	headers   []domain.TrustedHeader
	projectID string
}

func (s *schedulerRunIdentityStore) ProjectApplyTrustedHeaders(_ context.Context, projectID string) ([]domain.TrustedHeader, error) {
	s.projectID = projectID
	return s.headers, nil
}

func TestGetSchedulerReportsRunTrustedHeaders(t *testing.T) {
	store := &schedulerRunIdentityStore{
		schedulerIDLookupStore: schedulerIDLookupStore{
			project: domain.ProjectRecord{ID: "project-1"},
			scheduler: domain.ProjectSchedulerRecord{
				ID:          "scheduler-1",
				ProjectID:   "project-1",
				AgentName:   "worker",
				SchedulerID: "scheduler-1",
				SpecJSON:    `{}`,
			},
		},
		headers: []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "alice"}},
	}
	handler := NewProjectHandler(nil, store)

	response, err := handler.GetScheduler(context.Background(), connect.NewRequest(&agentcomposev2.GetSchedulerRequest{SchedulerId: "scheduler-1"}))
	if err != nil {
		t.Fatalf("GetScheduler returned error: %v", err)
	}
	got := response.Msg.GetRunTrustedHeaders()
	if store.projectID != "project-1" || len(got) != 1 || got[0].GetName() != "x-mpi-user-id" || got[0].GetValue() != "alice" {
		t.Fatalf("run trusted headers = %v for project %q", got, store.projectID)
	}
}
