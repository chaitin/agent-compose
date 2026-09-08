package api

import (
	"context"
	"database/sql"
	"testing"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestGetSchedulerSupportsGlobalSchedulerID(t *testing.T) {
	store := &schedulerIDLookupStore{
		project: domain.ProjectRecord{ID: "project-1"},
		scheduler: domain.ProjectSchedulerRecord{
			ID:          "scheduler-1",
			ProjectID:   "project-1",
			AgentName:   "worker",
			SchedulerID: "scheduler-1",
			SpecJSON:    `{"enabled":true,"script":"run()"}`,
		},
	}
	handler := NewProjectHandler(nil, store)

	response, err := handler.GetScheduler(context.Background(), connect.NewRequest(&agentcomposev2.GetSchedulerRequest{SchedulerId: " scheduler-1 "}))
	if err != nil {
		t.Fatalf("GetScheduler by scheduler ID returned error: %v", err)
	}
	if got := response.Msg.GetScheduler(); got.GetSchedulerId() != "scheduler-1" || got.GetProjectId() != "project-1" || got.GetAgentName() != "worker" {
		t.Fatalf("GetScheduler by scheduler ID = %#v", got)
	}
	if response.Msg.GetSpec().GetScript() != "run()" {
		t.Fatalf("GetScheduler spec = %#v", response.Msg.GetSpec())
	}
	if store.schedulerIDLookups != 1 || store.lastSchedulerID != "scheduler-1" || store.schedulerLists != 0 {
		t.Fatalf("lookup calls=%d id=%q list calls=%d", store.schedulerIDLookups, store.lastSchedulerID, store.schedulerLists)
	}
}

func TestGetSchedulerRejectsAmbiguousLookup(t *testing.T) {
	store := &schedulerIDLookupStore{}
	handler := NewProjectHandler(nil, store)

	_, err := handler.GetScheduler(context.Background(), connect.NewRequest(&agentcomposev2.GetSchedulerRequest{
		Project:     &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: "project-1"}},
		AgentName:   "worker",
		SchedulerId: "scheduler-1",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("GetScheduler ambiguous lookup error = %v, want invalid_argument", err)
	}
	if store.schedulerIDLookups != 0 || store.schedulerLists != 0 {
		t.Fatalf("ambiguous lookup accessed store: ID lookups=%d list calls=%d", store.schedulerIDLookups, store.schedulerLists)
	}
}

func TestGetSchedulerKeepsProjectAgentLookup(t *testing.T) {
	store := &schedulerIDLookupStore{
		project: domain.ProjectRecord{ID: "project-1"},
		scheduler: domain.ProjectSchedulerRecord{
			ID:          "scheduler-1",
			ProjectID:   "project-1",
			AgentName:   "worker",
			SchedulerID: "scheduler-1",
			SpecJSON:    `{}`,
		},
	}
	handler := NewProjectHandler(nil, store)

	response, err := handler.GetScheduler(context.Background(), connect.NewRequest(&agentcomposev2.GetSchedulerRequest{
		Project:   &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: "project-1"}},
		AgentName: "worker",
	}))
	if err != nil || response.Msg.GetScheduler().GetSchedulerId() != "scheduler-1" {
		t.Fatalf("GetScheduler by project and agent response=%#v err=%v", response, err)
	}
	if store.schedulerLists != 1 || store.schedulerIDLookups != 0 {
		t.Fatalf("project-agent lookup list calls=%d ID lookups=%d", store.schedulerLists, store.schedulerIDLookups)
	}
}

type schedulerIDLookupStore struct {
	project            domain.ProjectRecord
	scheduler          domain.ProjectSchedulerRecord
	schedulerIDLookups int
	lastSchedulerID    string
	schedulerLists     int
}

func (s *schedulerIDLookupStore) GetProject(_ context.Context, projectID string) (domain.ProjectRecord, error) {
	if s.project.ID != projectID {
		return domain.ProjectRecord{}, sql.ErrNoRows
	}
	return s.project, nil
}

func (s *schedulerIDLookupStore) ListProjects(context.Context, domain.ProjectListOptions) (domain.ProjectListResult, error) {
	return domain.ProjectListResult{Projects: []domain.ProjectRecord{s.project}}, nil
}

func (s *schedulerIDLookupStore) ListProjectAgents(context.Context, string) ([]domain.ProjectAgentRecord, error) {
	return nil, nil
}

func (s *schedulerIDLookupStore) ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error) {
	s.schedulerLists++
	return []domain.ProjectSchedulerRecord{s.scheduler}, nil
}

func (s *schedulerIDLookupStore) GetProjectSchedulerByID(_ context.Context, schedulerID string) (domain.ProjectSchedulerRecord, error) {
	s.schedulerIDLookups++
	s.lastSchedulerID = schedulerID
	if s.scheduler.ID != schedulerID {
		return domain.ProjectSchedulerRecord{}, domain.ResourceError(domain.ErrNotFound, "project scheduler", schedulerID, "scheduler not found", sql.ErrNoRows)
	}
	return s.scheduler, nil
}

func (s *schedulerIDLookupStore) GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error) {
	return domain.ProjectRevisionRecord{}, nil
}
