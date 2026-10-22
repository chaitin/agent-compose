package api

import (
	"context"
	"database/sql"
	"testing"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type octobusTokenStore struct {
	project domain.ProjectRecord
	tokens  map[string]string
}

func (s *octobusTokenStore) key(projectID, serverName string) string {
	return projectID + "/" + serverName
}

func (s *octobusTokenStore) GetProject(_ context.Context, projectID string) (domain.ProjectRecord, error) {
	if s.project.ID != projectID {
		return domain.ProjectRecord{}, sql.ErrNoRows
	}
	return s.project, nil
}

func (s *octobusTokenStore) ListProjects(context.Context, domain.ProjectListOptions) (domain.ProjectListResult, error) {
	return domain.ProjectListResult{Projects: []domain.ProjectRecord{s.project}}, nil
}

func (s *octobusTokenStore) ListProjectAgents(context.Context, string) ([]domain.ProjectAgentRecord, error) {
	return nil, nil
}

func (s *octobusTokenStore) ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error) {
	return nil, nil
}

func (s *octobusTokenStore) GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error) {
	return domain.ProjectRevisionRecord{}, nil
}

func (s *octobusTokenStore) GetProjectOctoBusServerToken(_ context.Context, projectID, serverName string) (string, error) {
	return s.tokens[s.key(projectID, serverName)], nil
}

func (s *octobusTokenStore) SaveProjectOctoBusServerToken(_ context.Context, projectID, serverName, token string) error {
	s.tokens[s.key(projectID, serverName)] = token
	return nil
}

func strPtr(s string) *string { return &s }

func TestUpdateAndGetProjectOctoBusServerToken(t *testing.T) {
	store := &octobusTokenStore{project: domain.ProjectRecord{ID: "project-1"}, tokens: map[string]string{}}
	handler := NewProjectHandler(nil, store)
	ctx := context.Background()
	project := &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: "project-1"}}

	update, err := handler.UpdateProjectOctoBusServerToken(ctx, connect.NewRequest(&agentcomposev2.UpdateProjectOctoBusServerTokenRequest{
		Project: project, ServerName: "internal", Token: strPtr("new-token"),
	}))
	if err != nil {
		t.Fatalf("UpdateProjectOctoBusServerToken returned error: %v", err)
	}
	if !update.Msg.GetTokenSet() {
		t.Fatalf("update token_set = false, want true")
	}
	if got := store.tokens["project-1/internal"]; got != "new-token" {
		t.Fatalf("stored token = %q, want %q", got, "new-token")
	}

	got, err := handler.GetProjectOctoBusServerToken(ctx, connect.NewRequest(&agentcomposev2.GetProjectOctoBusServerTokenRequest{
		Project: project, ServerName: "internal",
	}))
	if err != nil {
		t.Fatalf("GetProjectOctoBusServerToken returned error: %v", err)
	}
	if !got.Msg.GetTokenSet() {
		t.Fatalf("get token_set = false, want true")
	}

	// Field patch: absent token is a no-op, present empty clears.
	if _, err := handler.UpdateProjectOctoBusServerToken(ctx, connect.NewRequest(&agentcomposev2.UpdateProjectOctoBusServerTokenRequest{
		Project: project, ServerName: "internal",
	})); err != nil {
		t.Fatalf("absent-token update returned error: %v", err)
	}
	if got := store.tokens["project-1/internal"]; got != "new-token" {
		t.Fatalf("absent-token update changed stored token to %q", got)
	}
	cleared, err := handler.UpdateProjectOctoBusServerToken(ctx, connect.NewRequest(&agentcomposev2.UpdateProjectOctoBusServerTokenRequest{
		Project: project, ServerName: "internal", Token: strPtr(""),
	}))
	if err != nil {
		t.Fatalf("clear-token update returned error: %v", err)
	}
	if cleared.Msg.GetTokenSet() {
		t.Fatalf("clear-token update token_set = true, want false")
	}
	if got := store.tokens["project-1/internal"]; got != "" {
		t.Fatalf("clear-token update stored %q, want empty", got)
	}
}

func TestProjectOctoBusServerTokenValidation(t *testing.T) {
	store := &octobusTokenStore{project: domain.ProjectRecord{ID: "project-1"}, tokens: map[string]string{}}
	handler := NewProjectHandler(nil, store)
	ctx := context.Background()

	for _, test := range []struct {
		name    string
		project *agentcomposev2.ProjectRef
		server  string
		code    connect.Code
	}{
		{name: "missing project", project: nil, server: "internal", code: connect.CodeInvalidArgument},
		{name: "unknown project", project: &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: "missing"}}, server: "internal", code: connect.CodeNotFound},
		{name: "empty server name", project: &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: "project-1"}}, server: "", code: connect.CodeInvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := handler.GetProjectOctoBusServerToken(ctx, connect.NewRequest(&agentcomposev2.GetProjectOctoBusServerTokenRequest{
				Project: test.project, ServerName: test.server,
			}))
			if connect.CodeOf(err) != test.code {
				t.Fatalf("GetProjectOctoBusServerToken error = %v, want %v", connect.CodeOf(err), test.code)
			}
		})
	}
}
