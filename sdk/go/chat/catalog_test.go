package chat

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func projectAgent(name string, enabled bool, availability agentcomposev2.ProjectAgentAvailability) *agentcomposev2.ProjectAgent {
	return &agentcomposev2.ProjectAgent{
		AgentName:    name,
		DisplayName:  name + " display",
		Description:  name + " description",
		Enabled:      enabled,
		Availability: availability,
	}
}

// A product cannot call Agent(projectID, agentName) without knowing both, and
// the SDK used to offer no way to learn either. This is that half.
func TestProjectsListsEveryAgentAConversationCanBeHeldWith(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.listProjects = func(*agentcomposev2.ListProjectsRequest) (*agentcomposev2.ListProjectsResponse, error) {
		return &agentcomposev2.ListProjectsResponse{Projects: []*agentcomposev2.ProjectSummary{
			{ProjectId: "p1", Name: "first"},
			{ProjectId: "p2", Name: "second"},
		}, Total: 2}, nil
	}
	daemon.getProject = func(request *agentcomposev2.GetProjectRequest) (*agentcomposev2.GetProjectResponse, error) {
		agents := map[string][]*agentcomposev2.ProjectAgent{
			"p1": {projectAgent("reviewer", true, agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_AVAILABLE)},
			"p2": {projectAgent("writer", true, agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_AVAILABLE)},
		}
		id := request.GetProject().GetProjectId()
		return &agentcomposev2.GetProjectResponse{Project: &agentcomposev2.Project{
			Summary: &agentcomposev2.ProjectSummary{ProjectId: id},
			Agents:  agents[id],
		}}, nil
	}

	projects, err := daemon.client(t).Projects(context.Background())
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("projects = %d, want 2", len(projects))
	}
	if projects[0].ID != "p1" || projects[0].Name != "first" {
		t.Errorf("first project = %+v, want p1/first", projects[0])
	}
	if len(projects[0].Agents) != 1 || projects[0].Agents[0].Name != "reviewer" {
		t.Fatalf("first project agents = %+v, want one named reviewer", projects[0].Agents)
	}
	agent := projects[0].Agents[0]
	if !agent.Available || agent.Unavailable != "" {
		t.Errorf("agent = %+v, want available", agent)
	}
	if agent.DisplayName != "reviewer display" || agent.Description != "reviewer description" {
		t.Errorf("agent presentation = %+v, want the daemon's own display fields", agent)
	}
}

// Availability is an enum, and matching it as text is a trap: "unavailable"
// contains "available". An agent the daemon calls unavailable must not be
// reported as one a conversation can start with.
func TestProjectsReportsWhyAnAgentCannotBeUsed(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.listProjects = func(*agentcomposev2.ListProjectsRequest) (*agentcomposev2.ListProjectsResponse, error) {
		return &agentcomposev2.ListProjectsResponse{Projects: []*agentcomposev2.ProjectSummary{{ProjectId: "p1"}}}, nil
	}
	daemon.getProject = func(*agentcomposev2.GetProjectRequest) (*agentcomposev2.GetProjectResponse, error) {
		return &agentcomposev2.GetProjectResponse{Project: &agentcomposev2.Project{Agents: []*agentcomposev2.ProjectAgent{
			projectAgent("off", false, agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_AVAILABLE),
			projectAgent("gone", true, agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_UNAVAILABLE),
			projectAgent("broken", true, agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_VALIDATION_FAILED),
			projectAgent("quiet", true, agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_UNSPECIFIED),
		}}}, nil
	}

	projects, err := daemon.client(t).Projects(context.Background())
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	want := map[string]bool{"off": false, "gone": false, "broken": false, "quiet": true}
	for _, agent := range projects[0].Agents {
		if agent.Available != want[agent.Name] {
			t.Errorf("%s available = %v, want %v", agent.Name, agent.Available, want[agent.Name])
		}
		if !agent.Available && agent.Unavailable == "" {
			t.Errorf("%s is unavailable but says nothing about why", agent.Name)
		}
		if agent.Available && agent.Unavailable != "" {
			t.Errorf("%s is available but carries a reason %q", agent.Name, agent.Unavailable)
		}
	}
}

// Projects come and go. One that is gone by the time its agents are read must
// not take the whole chooser down with it.
func TestProjectsSkipsAProjectThatDisappearsMidListing(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.listProjects = func(*agentcomposev2.ListProjectsRequest) (*agentcomposev2.ListProjectsResponse, error) {
		return &agentcomposev2.ListProjectsResponse{Projects: []*agentcomposev2.ProjectSummary{
			{ProjectId: "gone"}, {ProjectId: "here", Name: "here"},
		}}, nil
	}
	daemon.getProject = func(request *agentcomposev2.GetProjectRequest) (*agentcomposev2.GetProjectResponse, error) {
		if request.GetProject().GetProjectId() == "gone" {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no such project"))
		}
		return &agentcomposev2.GetProjectResponse{Project: &agentcomposev2.Project{
			Agents: []*agentcomposev2.ProjectAgent{
				projectAgent("reviewer", true, agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_AVAILABLE),
			},
		}}, nil
	}

	projects, err := daemon.client(t).Projects(context.Background())
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != "here" {
		t.Fatalf("projects = %+v, want only the one that still exists", projects)
	}
}

// A daemon that is failing for some other reason is not a missing project,
// and must not be quietly dropped from the catalog.
func TestProjectsReportsAFailureThatIsNotAMissingProject(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.listProjects = func(*agentcomposev2.ListProjectsRequest) (*agentcomposev2.ListProjectsResponse, error) {
		return &agentcomposev2.ListProjectsResponse{Projects: []*agentcomposev2.ProjectSummary{{ProjectId: "p1"}}}, nil
	}
	daemon.getProject = func(*agentcomposev2.GetProjectRequest) (*agentcomposev2.GetProjectResponse, error) {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the store is down"))
	}

	if _, err := daemon.client(t).Projects(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Projects error = %v, want ErrUnavailable", err)
	}
}
