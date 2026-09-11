package chat

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// catalogPageSize bounds one page of a project listing.
const catalogPageSize = 200

// Project is one project and the agents it offers, as a chat product needs
// them: enough to let someone pick a counterpart, and nothing about how the
// project is configured or deployed.
type Project struct {
	// ID is what [Client.Agent] takes as its project.
	ID string
	// Name is the project's own name, for display.
	Name string
	// Agents are the counterparts this project offers, in the order the daemon
	// reports them.
	Agents []AgentInfo
}

// AgentInfo describes one agent a conversation can be held with.
type AgentInfo struct {
	// Name is what [Client.Agent] takes as its agent.
	Name string
	// DisplayName and Description are for presentation, and may be empty.
	DisplayName string
	Description string
	// Available reports whether a conversation can be started with this agent
	// right now. An agent that is disabled, or whose image or model does not
	// resolve, is listed but not available: a product usually shows it greyed
	// out rather than hiding it, so the person can see it exists.
	Available bool
	// Unavailable, when Available is false, says why in the daemon's own
	// terms. It is empty when the agent is available.
	Unavailable string
}

// Projects lists the projects and agents this daemon can hold conversations
// with.
//
// [Client.Agent] takes a project ID and an agent name, and until now the SDK
// gave no way to learn either: a product had to be told them, or go around the
// SDK to the daemon. This is that missing half.
//
// Agent detail lives on the project, not on the summary a listing returns, so
// this makes one call per project. A project that disappears between the
// listing and its expansion is skipped rather than failing the whole catalog:
// projects come and go, and a chooser missing one entry is more useful than a
// chooser that will not open.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	var summaries []*agentcomposev2.ProjectSummary
	for offset := uint32(0); ; {
		listed, err := c.transport.projects.ListProjects(ctx, connect.NewRequest(&agentcomposev2.ListProjectsRequest{
			Offset: offset, Limit: catalogPageSize,
		}))
		if err != nil {
			return nil, fromConnect("Projects", err)
		}
		page := listed.Msg.GetProjects()
		summaries = append(summaries, page...)
		if len(page) == 0 || offset+uint32(len(page)) >= listed.Msg.GetTotal() {
			break
		}
		offset += uint32(len(page))
	}
	projects := make([]Project, 0, len(summaries))
	for _, summary := range summaries {
		project, err := c.project(ctx, summary.GetProjectId())
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		project.Name = summary.GetName()
		projects = append(projects, project)
	}
	return projects, nil
}

func (c *Client) project(ctx context.Context, projectID string) (Project, error) {
	detail, err := c.transport.projects.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project: &agentcomposev2.ProjectRef{
			Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: projectID},
		},
	}))
	if err != nil {
		return Project{}, fromConnect("Projects", err)
	}
	found := detail.Msg.GetProject()
	project := Project{
		ID:     projectID,
		Name:   found.GetSummary().GetName(),
		Agents: make([]AgentInfo, 0, len(found.GetAgents())),
	}
	for _, agent := range found.GetAgents() {
		name := strings.TrimSpace(agent.GetAgentName())
		if name == "" {
			continue
		}
		project.Agents = append(project.Agents, agentInfoOf(agent))
	}
	return project, nil
}

// agentInfoOf decides whether an agent can be conversed with. Availability is
// an enum rather than a string on purpose: matching text for "available" also
// matches "unavailable", which is the sort of mistake every caller would
// otherwise have to avoid on its own.
func agentInfoOf(agent *agentcomposev2.ProjectAgent) AgentInfo {
	info := AgentInfo{
		Name:        strings.TrimSpace(agent.GetAgentName()),
		DisplayName: strings.TrimSpace(agent.GetDisplayName()),
		Description: strings.TrimSpace(agent.GetDescription()),
	}
	switch {
	case !agent.GetEnabled():
		info.Unavailable = "the agent is disabled"
	case agent.GetAvailability() == agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_UNAVAILABLE:
		info.Unavailable = "the agent is unavailable"
	case agent.GetAvailability() == agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_VALIDATION_FAILED:
		info.Unavailable = "the agent's configuration did not validate"
	default:
		// UNSPECIFIED included: a daemon that does not report availability is
		// not saying the agent is broken.
		info.Available = true
	}
	return info
}

// isNotFound reports whether err is the daemon saying something is not there.
func isNotFound(err error) bool {
	var failure *Error
	return errors.As(err, &failure) && strings.EqualFold(strings.TrimSpace(failure.Code), "not_found")
}
