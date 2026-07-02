package agentcompose

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	modelpkg "agent-compose/pkg/model"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func TestAgentSessionMessageUsesDefinitionProvider(t *testing.T) {
	ctx := context.Background()
	service, runtime, _ := newTestServiceAPIHarness(t)
	created, err := service.CreateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.CreateAgentDefinitionRequest{
		Name:         "Claude Runner",
		Enabled:      true,
		Provider:     "open-code",
		Model:        "unused-model-field",
		SystemPrompt: "system body",
		EnvItems: []*agentcomposev1.SessionEnvVar{
			{Name: "OPENCODE_MODEL", Value: "anthropic/claude-sonnet-4-5"},
		},
	}))
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	sessionResp, err := service.CreateAgentSession(ctx, connect.NewRequest(&agentcomposev1.CreateAgentSessionRequest{
		AgentId: created.Msg.GetAgent().GetAgentId(),
	}))
	if err != nil {
		t.Fatalf("CreateAgentSession returned error: %v", err)
	}
	sessionID := sessionResp.Msg.GetSession().GetSummary().GetSessionId()
	_, err = service.SendAgentMessage(ctx, connect.NewRequest(&agentcomposev1.SendAgentMessageRequest{
		SessionId: sessionID,
		Agent:     "codex",
		Message:   "summarize",
	}))
	if err != nil {
		t.Fatalf("SendAgentMessage returned error: %v", err)
	}
	if len(runtime.providers) != 1 || runtime.providers[0] != "opencode" {
		t.Fatalf("runtime providers = %v, want [opencode]", runtime.providers)
	}
	if len(runtime.agentSpecs) != 1 {
		t.Fatalf("runtime agent specs = %d, want 1", len(runtime.agentSpecs))
	}
	command := strings.Join(runtime.agentSpecs[0].Args, " ")
	for _, want := range []string{"--provider 'opencode'", "--model 'anthropic/claude-sonnet-4-5'"} {
		if !strings.Contains(command, want) {
			t.Fatalf("agent command %q does not contain %q", command, want)
		}
	}
	if strings.Contains(command, "--system-prompt-file") {
		t.Fatalf("agent command %q contains deprecated --system-prompt-file flag", command)
	}
}

func TestAgentSessionMessageUsesDefinitionProviderEnvForFacade(t *testing.T) {
	ctx := context.Background()
	service, runtime, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	created, err := service.CreateAgentDefinition(ctx, connect.NewRequest(&agentcomposev1.CreateAgentDefinitionRequest{
		Name:     "Claude Env Runner",
		Enabled:  true,
		Provider: "claude",
		EnvItems: []*agentcomposev1.SessionEnvVar{
			{Name: "ANTHROPIC_API_KEY", Value: "agent-anthropic-key", Secret: true},
			{Name: "ANTHROPIC_BASE_URL", Value: "https://anthropic.example.invalid"},
			{Name: "ANTHROPIC_MODEL", Value: "claude-test"},
		},
	}))
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	sessionResp, err := service.CreateAgentSession(ctx, connect.NewRequest(&agentcomposev1.CreateAgentSessionRequest{
		AgentId: created.Msg.GetAgent().GetAgentId(),
	}))
	if err != nil {
		t.Fatalf("CreateAgentSession returned error: %v", err)
	}
	sessionID := sessionResp.Msg.GetSession().GetSummary().GetSessionId()
	createdSession, err := service.store.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if env := modelpkg.SessionEnvMap(createdSession.EnvItems); env["ANTHROPIC_API_KEY"] != "" {
		t.Fatalf("ANTHROPIC_API_KEY persisted in session env: %#v", createdSession.EnvItems)
	}
	if len(createdSession.ProviderEnvItems) != 0 {
		t.Fatalf("ProviderEnvItems unexpectedly set before execution: %#v", createdSession.ProviderEnvItems)
	}
	storedAgent, err := service.configDB.GetAgentDefinition(ctx, created.Msg.GetAgent().GetAgentId())
	if err != nil {
		t.Fatalf("GetAgentDefinition returned error: %v", err)
	}
	if env := modelpkg.SessionEnvMap(storedAgent.EnvItems); env["ANTHROPIC_API_KEY"] != "agent-anthropic-key" {
		t.Fatalf("stored agent env missing key: %#v", storedAgent.EnvItems)
	}
	agentConfig := service.resolveSessionAgentConfig(ctx, createdSession, "codex")
	if env := modelpkg.SessionEnvMap(agentConfig.EnvItems); env["ANTHROPIC_API_KEY"] != "agent-anthropic-key" || agentConfig.Provider != "claude" {
		t.Fatalf("resolved agent config = %#v env=%#v", agentConfig, agentConfig.EnvItems)
	}
	_, err = service.SendAgentMessage(ctx, connect.NewRequest(&agentcomposev1.SendAgentMessageRequest{
		SessionId: sessionID,
		Agent:     "codex",
		Message:   "hello",
	}))
	if err != nil {
		t.Fatalf("SendAgentMessage returned error: %v", err)
	}
	if len(createdSession.ProviderEnvItems) != 0 {
		t.Fatalf("SendAgentMessage mutated source session ProviderEnvItems: %#v", createdSession.ProviderEnvItems)
	}
	if len(runtime.agentSpecs) != 1 {
		t.Fatalf("runtime agent specs = %d, want 1", len(runtime.agentSpecs))
	}
	env := runtime.agentSpecs[0].Env
	token := env["AGENT_COMPOSE_SESSION_TOKEN"]
	if token == "" {
		t.Fatalf("agent exec env missing facade token: providers=%v env=%#v", runtime.providers, env)
	}
	if env["ANTHROPIC_API_KEY"] != token {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want facade token", env["ANTHROPIC_API_KEY"])
	}
	if env["ANTHROPIC_BASE_URL"] != "http://agent-compose.test/api/runtime/sessions/"+sessionID+"/llm/anthropic" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", env["ANTHROPIC_BASE_URL"])
	}
	if env["ANTHROPIC_MODEL"] != "claude-test" {
		t.Fatalf("ANTHROPIC_MODEL = %q, want claude-test", env["ANTHROPIC_MODEL"])
	}
}
