package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"agent-compose/pkg/controlplane"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestGetProjectSecretVisibilityRequiresTrustedLocalTransport(t *testing.T) {
	store := &apiProjectRunStore{
		projects: []domain.ProjectRecord{{ID: "project-1", Name: "Project", CurrentRevision: 1}},
		revision: domain.ProjectRevisionRecord{ProjectID: "project-1", Revision: 1, SpecJSON: `{
            "variables":[{"name":"PROJECT_SECRET","value":"project-secret","secret":true}],
            "mcp_servers":[{"name":"project-mcp","env":[{"name":"TOKEN","value":"project-mcp-env","secret":true}],"headers":[{"name":"Authorization","value":"project-mcp-header","secret":true}]}],
            "octobus_servers":[{"name":"internal","url":"https://octobus.example","token":"octobus-secret"}],
            "agents":[{"name":"worker","env":[{"name":"AGENT_SECRET","value":"agent-secret","secret":true}],"mcp_servers":[{"name":"agent-mcp","env":[{"name":"TOKEN","value":"agent-mcp-env","secret":true}],"headers":[{"name":"Authorization","value":"agent-mcp-header","secret":true}]}]}]
        }`},
	}
	handler := NewProjectHandler(nil, store, nil)
	request := func() *connect.Request[agentcomposev2.GetProjectRequest] {
		req := connect.NewRequest(&agentcomposev2.GetProjectRequest{
			Project:     &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: "project-1"}},
			IncludeSpec: true,
		})
		req.Header().Set("User-Agent", "agent-compose local CLI")
		req.Header().Set("X-Agent-Compose-Trusted", "true")
		return req
	}

	remote, err := handler.GetProject(context.Background(), request())
	if err != nil {
		t.Fatalf("remote GetProject returned error: %v", err)
	}
	assertProjectSecretValues(t, remote.Msg.GetProject().GetSpec(), secretRedactedValue)

	local, err := handler.GetProject(controlplane.WithTrustedLocalRequest(context.Background()), request())
	if err != nil {
		t.Fatalf("trusted local GetProject returned error: %v", err)
	}
	assertProjectSecretValues(t, local.Msg.GetProject().GetSpec(), "")
}

func assertProjectSecretValues(t *testing.T, spec *agentcomposev2.ProjectSpec, redacted string) {
	t.Helper()
	want := []string{"project-secret", "project-mcp-env", "project-mcp-header", "octobus-secret", "agent-secret", "agent-mcp-env", "agent-mcp-header"}
	if redacted != "" {
		for index := range want {
			want[index] = redacted
		}
	}
	got := []string{
		spec.GetVariables()[0].GetValue(),
		spec.GetMcpServers()[0].GetEnv()[0].GetValue(),
		spec.GetMcpServers()[0].GetHeaders()[0].GetValue(),
		spec.GetOctobusServers()[0].GetToken(),
		spec.GetAgents()[0].GetEnv()[0].GetValue(),
		spec.GetAgents()[0].GetMcpServers()[0].GetEnv()[0].GetValue(),
		spec.GetAgents()[0].GetMcpServers()[0].GetHeaders()[0].GetValue(),
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("secret value %d = %q, want %q", index, got[index], want[index])
		}
	}
}
