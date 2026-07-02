package loaders

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	driverpkg "agent-compose/pkg/driver"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func TestLoaderCreateBindsAgentDefinitionProvider(t *testing.T) {
	ctx := context.Background()
	store := newLoaderEventTestConfigStore(t)
	agent, err := store.CreateAgentDefinition(ctx, AgentDefinition{
		ID:          "agent-loader",
		Name:        "Loader Agent",
		Enabled:     true,
		Provider:    "gemini",
		Driver:      driverpkg.RuntimeDriverDocker,
		GuestImage:  "agent-guest:latest",
		WorkspaceID: "",
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	manager := newTestLoaderManager(t, ManagerDeps{
		ConfigDB: store,
		Engine:   &QJSLoaderEngine{},
	})
	service := NewService(store, manager, NewLoaderBusWithBuffer(8))
	created, err := service.CreateLoader(ctx, connect.NewRequest(&agentcomposev1.CreateLoaderRequest{
		Name:              "Bound Loader",
		Runtime:           LoaderRuntimeScheduler,
		Script:            `scheduler.interval("tick", function(){ scheduler.log("tick"); }, 60000);`,
		AgentId:           agent.ID,
		DefaultAgent:      "codex",
		SessionPolicy:     LoaderSessionPolicyNew,
		ConcurrencyPolicy: LoaderConcurrencyPolicySkip,
		Enabled:           true,
	}))
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}
	summary := created.Msg.GetLoader().GetSummary()
	if summary.GetAgentId() != agent.ID {
		t.Fatalf("loader agent id = %q, want %q", summary.GetAgentId(), agent.ID)
	}
	if summary.GetDefaultAgent() != "gemini" {
		t.Fatalf("loader default agent = %q, want gemini", summary.GetDefaultAgent())
	}
}
