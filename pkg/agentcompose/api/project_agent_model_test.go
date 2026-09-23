package api

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type projectAgentModelResolverStub struct {
	resolutions map[string]llms.AgentModelResolution
	err         error
}

func (s projectAgentModelResolverStub) ResolveProjectAgentModels(context.Context, domain.ProjectRecord, []domain.ProjectAgentRecord) (map[string]llms.AgentModelResolution, error) {
	return s.resolutions, s.err
}

func TestNewProjectHandlerWithAgentModelsSetsResolver(t *testing.T) {
	resolver := projectAgentModelResolverStub{}
	handler := NewProjectHandlerWithAgentModels(ProjectHandlerDeps{AgentModels: resolver})

	if _, ok := handler.agentModels.(projectAgentModelResolverStub); !ok {
		t.Fatalf("agent model resolver = %#v", handler.agentModels)
	}
}

func TestEnrichProjectAgentModelsMapsResolvedModelAndSource(t *testing.T) {
	handler := &ProjectHandler{agentModels: projectAgentModelResolverStub{resolutions: map[string]llms.AgentModelResolution{
		"coder": {Model: "dev/gpt-5.5", Source: llms.AgentModelSourceDaemonDefault},
		"main":  {Model: "gpt-explicit", Source: llms.AgentModelSourceProject},
	}}}
	project := &agentcomposev2.Project{Agents: []*agentcomposev2.ProjectAgent{{AgentName: "coder"}, {AgentName: "main", Model: "gpt-explicit"}}}
	if err := handler.enrichProjectAgentModels(context.Background(), domain.ProjectRecord{ID: "project"}, nil, project); err != nil {
		t.Fatal(err)
	}
	if coder := project.GetAgents()[0]; coder.GetResolvedModel() != "dev/gpt-5.5" || coder.GetModelSource() != agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_DAEMON_DEFAULT {
		t.Fatalf("coder = %#v", coder)
	}
	if main := project.GetAgents()[1]; main.GetModel() != "gpt-explicit" || main.GetResolvedModel() != "gpt-explicit" || main.GetModelSource() != agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_PROJECT {
		t.Fatalf("main = %#v", main)
	}
}

func TestEnrichProjectAgentModelsReturnsInternalResolverError(t *testing.T) {
	wantErr := errors.New("resolution failed")
	handler := &ProjectHandler{agentModels: projectAgentModelResolverStub{err: wantErr}}
	err := handler.enrichProjectAgentModels(context.Background(), domain.ProjectRecord{ID: "project"}, nil, &agentcomposev2.Project{})
	if connect.CodeOf(err) != connect.CodeInternal || !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want internal wrapping %v", err, wantErr)
	}
}

func TestAgentModelSourceToProtoMapsEverySource(t *testing.T) {
	tests := []struct {
		source llms.AgentModelSource
		want   agentcomposev2.AgentModelSource
	}{
		{source: llms.AgentModelSourceProject, want: agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_PROJECT},
		{source: llms.AgentModelSourceAgentEnv, want: agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_AGENT_ENV},
		{source: llms.AgentModelSourceDaemonDefault, want: agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_DAEMON_DEFAULT},
		{source: llms.AgentModelSourceProviderDefault, want: agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_PROVIDER_DEFAULT},
		{source: llms.AgentModelSourceUnresolved, want: agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_UNRESOLVED},
		{source: llms.AgentModelSource(""), want: agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_UNSPECIFIED},
		{source: llms.AgentModelSource("unknown"), want: agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_UNSPECIFIED},
	}
	for _, test := range tests {
		t.Run(string(test.source), func(t *testing.T) {
			if got := agentModelSourceToProto(test.source); got != test.want {
				t.Fatalf("agentModelSourceToProto(%q) = %v, want %v", test.source, got, test.want)
			}
		})
	}
}
