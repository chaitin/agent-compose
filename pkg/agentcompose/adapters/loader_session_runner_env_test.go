package adapters

import (
	"context"
	"testing"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func TestLoaderSandboxRunnerEnvironmentPrecedence(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	if _, err := bridge.configDB.ReplaceGlobalEnv(ctx, []domain.SandboxEnvVar{
		{Name: "GLOBAL_ONLY", Value: "global"},
		{Name: "SHARED", Value: "global"},
	}); err != nil {
		t.Fatalf("replace global env: %v", err)
	}
	agent, err := bridge.configDB.CreateAgentDefinition(ctx, domain.AgentDefinition{
		ID:         "loader-env-agent",
		Name:       "loader-env-agent",
		Enabled:    true,
		Provider:   "codex",
		Driver:     driverpkg.RuntimeDriverDocker,
		GuestImage: "guest:latest",
		EnvItems: []domain.SandboxEnvVar{
			{Name: "AGENT_ONLY", Value: "agent"},
			{Name: "AGENT_VS_LOADER", Value: "agent"},
			{Name: "SHARED", Value: "agent"},
		},
	})
	if err != nil {
		t.Fatalf("create agent definition: %v", err)
	}

	runner := NewLoaderSandboxRunner(bridge.config, bridge.store, bridge.configDB, bridge.workspaceEnsurer, driver, nil, nil, bridge.streams, nil, nil, bridge.agentExecutor)
	loader := domain.Loader{
		Summary: domain.LoaderSummary{
			ID:            "loader-env-precedence",
			Name:          "Loader env precedence",
			AgentID:       agent.ID,
			Driver:        driverpkg.RuntimeDriverDocker,
			GuestImage:    "guest:latest",
			SandboxPolicy: domain.LoaderSandboxPolicyNew,
		},
		EnvItems: []domain.SandboxEnvVar{
			{Name: "AGENT_VS_LOADER", Value: "loader", Secret: true},
			{Name: "LOADER_ONLY", Value: "loader"},
			{Name: "LOADER_VS_REQUEST", Value: "loader"},
			{Name: "SHARED", Value: "loader"},
		},
	}
	request := domain.LoaderAgentRequest{SandboxEnv: []domain.SandboxEnvVar{
		{Name: "LOADER_VS_REQUEST", Value: "request", Secret: true},
		{Name: "REQUEST_ONLY", Value: "request"},
		{Name: "SHARED", Value: "request", Secret: true},
	}}

	sandbox, _, err := runner.Ensure(ctx, loader, request, false)
	if err != nil {
		t.Fatalf("ensure loader sandbox: %v", err)
	}
	got := loaderRunnerEnvItemsByName(domain.MergeEnvItems(sandbox.EnvItems, sandbox.ProviderEnvItems))
	for name, want := range map[string]domain.SandboxEnvVar{
		"GLOBAL_ONLY":       {Name: "GLOBAL_ONLY", Value: "global"},
		"AGENT_ONLY":        {Name: "AGENT_ONLY", Value: "agent"},
		"AGENT_VS_LOADER":   {Name: "AGENT_VS_LOADER", Value: "loader", Secret: true},
		"LOADER_ONLY":       {Name: "LOADER_ONLY", Value: "loader"},
		"LOADER_VS_REQUEST": {Name: "LOADER_VS_REQUEST", Value: "request", Secret: true},
		"REQUEST_ONLY":      {Name: "REQUEST_ONLY", Value: "request"},
		"SHARED":            {Name: "SHARED", Value: "request", Secret: true},
	} {
		if got[name] != want {
			t.Fatalf("effective env %s = %#v, want %#v", name, got[name], want)
		}
	}
	if _, ok := loaderRunnerEnvItemsByName(sandbox.ProviderEnvItems)["GLOBAL_ONLY"]; ok {
		t.Fatal("global-only env was recorded as a sandbox provider override")
	}
}

func loaderRunnerEnvItemsByName(items []domain.SandboxEnvVar) map[string]domain.SandboxEnvVar {
	byName := make(map[string]domain.SandboxEnvVar, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		byName[item.Name] = item
	}
	return byName
}
