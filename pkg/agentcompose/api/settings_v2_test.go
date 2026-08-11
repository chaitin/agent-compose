package api

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestWorkspaceContentErrorsRemainInternal(t *testing.T) {
	if code := workspaceErrorCode(workspaceContentError(errors.New("disk failed"))); code != connect.CodeInternal {
		t.Fatalf("workspace content code = %v", code)
	}
	if code := workspaceErrorCode(domain.ErrReferenced); code != connect.CodeFailedPrecondition {
		t.Fatalf("referenced workspace code = %v", code)
	}
}

func TestSettingsGlobalEnvDistinguishesRetainAndClearSecret(t *testing.T) {
	ctx := context.Background()
	store := &settingsStoreFake{env: []domain.SandboxEnvVar{{Name: "TOKEN", Value: "stored-secret", Secret: true}}}
	handler := NewSettingsV2Handler(&appconfig.Config{DataRoot: t.TempDir()}, store)

	retained, err := handler.UpdateGlobalEnv(ctx, connect.NewRequest(&agentcomposev2.UpdateGlobalEnvRequest{Env: []*agentcomposev2.EnvVarUpdateSpec{{Name: "TOKEN", Secret: true}}}))
	if err != nil {
		t.Fatalf("retain secret: %v", err)
	}
	if store.env[0].Value != "stored-secret" || retained.Msg.GetEnv()[0].GetValue() != secretRedactedValue {
		t.Fatalf("retained env=%#v response=%#v", store.env, retained.Msg.GetEnv())
	}

	empty := ""
	cleared, err := handler.UpdateGlobalEnv(ctx, connect.NewRequest(&agentcomposev2.UpdateGlobalEnvRequest{Env: []*agentcomposev2.EnvVarUpdateSpec{{Name: "TOKEN", Value: &empty, Secret: true}}}))
	if err != nil {
		t.Fatalf("clear secret: %v", err)
	}
	if store.env[0].Value != "" || cleared.Msg.GetEnv()[0].GetValue() != secretRedactedValue {
		t.Fatalf("cleared env=%#v response=%#v", store.env, cleared.Msg.GetEnv())
	}
}

func TestSettingsGlobalEnvEmptyAndOmittedEntriesReplaceCollection(t *testing.T) {
	ctx := context.Background()
	store := &settingsStoreFake{env: []domain.SandboxEnvVar{
		{Name: "KEEP", Value: "old"},
		{Name: "DELETE", Value: "old"},
	}}
	handler := NewSettingsV2Handler(&appconfig.Config{DataRoot: t.TempDir()}, store)

	value := "new"
	if _, err := handler.UpdateGlobalEnv(ctx, connect.NewRequest(&agentcomposev2.UpdateGlobalEnvRequest{
		Env: []*agentcomposev2.EnvVarUpdateSpec{{Name: "KEEP", Value: &value}},
	})); err != nil {
		t.Fatalf("replace env: %v", err)
	}
	if len(store.env) != 1 || store.env[0].Name != "KEEP" || store.env[0].Value != "new" {
		t.Fatalf("replacement env = %#v", store.env)
	}

	if _, err := handler.UpdateGlobalEnv(ctx, connect.NewRequest(&agentcomposev2.UpdateGlobalEnvRequest{})); err != nil {
		t.Fatalf("clear env: %v", err)
	}
	if len(store.env) != 0 {
		t.Fatalf("cleared env = %#v", store.env)
	}
}

type settingsStoreFake struct {
	env         []domain.SandboxEnvVar
	credentials llms.EnvDefaultProviderCredentials
}

func (s *settingsStoreFake) ListGlobalEnv(context.Context) ([]domain.SandboxEnvVar, error) {
	return append([]domain.SandboxEnvVar(nil), s.env...), nil
}
func (s *settingsStoreFake) ReplaceGlobalEnv(_ context.Context, items []domain.SandboxEnvVar) ([]domain.SandboxEnvVar, error) {
	s.env = append([]domain.SandboxEnvVar(nil), items...)
	return s.env, nil
}
func (s *settingsStoreFake) ReplaceGlobalEnvWithProviderCredentials(ctx context.Context, items []domain.SandboxEnvVar, credentials llms.EnvDefaultProviderCredentials) ([]domain.SandboxEnvVar, error) {
	s.credentials = credentials
	return s.ReplaceGlobalEnv(ctx, items)
}
func (*settingsStoreFake) ListWorkspaceConfigs(context.Context) ([]domain.WorkspaceConfig, error) {
	return nil, nil
}
func (*settingsStoreFake) GetWorkspaceConfig(context.Context, string) (domain.WorkspaceConfig, error) {
	return domain.WorkspaceConfig{}, domain.ErrNotFound
}
func (*settingsStoreFake) CreateWorkspaceConfig(_ context.Context, item domain.WorkspaceConfig) (domain.WorkspaceConfig, error) {
	return item, nil
}
func (*settingsStoreFake) UpdateWorkspaceConfig(_ context.Context, item domain.WorkspaceConfig) (domain.WorkspaceConfig, error) {
	return item, nil
}
func (*settingsStoreFake) DeleteWorkspaceConfig(context.Context, string) error { return nil }
func (*settingsStoreFake) GetCapabilityGateway(context.Context) (domain.CapabilityGatewaySettings, error) {
	return domain.CapabilityGatewaySettings{}, nil
}
func (*settingsStoreFake) SaveCapabilityGateway(_ context.Context, item domain.CapabilityGatewaySettings) (domain.CapabilityGatewaySettings, error) {
	return item, nil
}

func TestSettingsGlobalEnvProviderCredentialsFallBackToProcessEnvAndConfig(t *testing.T) {
	t.Setenv("LLM_API_KEY", "process-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "process-token")
	handler := NewSettingsV2Handler(&appconfig.Config{LLMAPIKey: "config-key"}, &settingsStoreFake{})
	credentials := handler.resolveEnvDefaultProviderCredentials(nil)
	if credentials.OpenAIAPIKey != "process-key" {
		t.Fatalf("OpenAI fallback key = %q, want process-key", credentials.OpenAIAPIKey)
	}
	if credentials.AnthropicAPIKey != "process-token" || credentials.AnthropicAuthHeader != "Authorization" || credentials.AnthropicAuthScheme != "Bearer" {
		t.Fatalf("Anthropic fallback credentials = %#v", credentials)
	}

	t.Setenv("LLM_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	credentials = handler.resolveEnvDefaultProviderCredentials(nil)
	if credentials.OpenAIAPIKey != "config-key" {
		t.Fatalf("config fallback key = %q, want config-key", credentials.OpenAIAPIKey)
	}
}

func TestSettingsGlobalEnvEmptyCredentialFallsBack(t *testing.T) {
	t.Setenv("LLM_API_KEY", "process-key")
	handler := NewSettingsV2Handler(&appconfig.Config{LLMAPIKey: "config-key"}, &settingsStoreFake{})
	credentials := handler.resolveEnvDefaultProviderCredentials([]domain.SandboxEnvVar{{Name: "LLM_API_KEY", Value: ""}})
	if credentials.OpenAIAPIKey != "process-key" {
		t.Fatalf("empty credential fallback = %q, want process-key", credentials.OpenAIAPIKey)
	}
}

func TestSettingsGlobalEnvEmptyAnthropicCredentialFallsBackToAuthToken(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "process-token")
	handler := NewSettingsV2Handler(&appconfig.Config{}, &settingsStoreFake{})
	credentials := handler.resolveEnvDefaultProviderCredentials([]domain.SandboxEnvVar{{Name: "ANTHROPIC_API_KEY", Value: ""}})
	if credentials.AnthropicAPIKey != "process-token" || credentials.AnthropicAuthHeader != "Authorization" || credentials.AnthropicAuthScheme != "Bearer" {
		t.Fatalf("empty Anthropic credential fallback = %#v", credentials)
	}
}

func TestSettingsGlobalEnvCredentialsFallBackIndependentlyByFamily(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "process-anthropic")
	t.Setenv("LLM_API_KEY", "process-generic")
	handler := NewSettingsV2Handler(&appconfig.Config{}, &settingsStoreFake{})
	credentials := handler.resolveEnvDefaultProviderCredentials([]domain.SandboxEnvVar{{Name: "OPENAI_API_KEY", Value: "global-openai"}})
	if credentials.OpenAIAPIKey != "global-openai" {
		t.Fatalf("OpenAI credentials = %q, want global-openai", credentials.OpenAIAPIKey)
	}
	if credentials.AnthropicAPIKey != "process-anthropic" || credentials.AnthropicAuthHeader != "Authorization" || credentials.AnthropicAuthScheme != "Bearer" {
		t.Fatalf("Anthropic credentials = %#v", credentials)
	}

	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	credentials = handler.resolveEnvDefaultProviderCredentials([]domain.SandboxEnvVar{{Name: "ANTHROPIC_API_KEY", Value: "global-anthropic"}})
	if credentials.AnthropicAPIKey != "global-anthropic" || credentials.OpenAIAPIKey != "process-generic" {
		t.Fatalf("reverse family credentials = %#v", credentials)
	}
}

func TestSettingsUpdateGlobalEnvPassesEffectiveFallbackToStore(t *testing.T) {
	t.Setenv("LLM_API_KEY", "process-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "process-token")
	store := &settingsStoreFake{env: []domain.SandboxEnvVar{{Name: "LLM_API_KEY", Value: "old-key", Secret: true}}}
	handler := NewSettingsV2Handler(&appconfig.Config{LLMAPIKey: "config-key"}, store)

	if _, err := handler.UpdateGlobalEnv(context.Background(), connect.NewRequest(&agentcomposev2.UpdateGlobalEnvRequest{})); err != nil {
		t.Fatalf("clear global environment: %v", err)
	}
	if len(store.env) != 0 {
		t.Fatalf("saved environment = %#v, want empty", store.env)
	}
	if store.credentials.OpenAIAPIKey != "process-key" {
		t.Errorf("persisted OpenAI fallback = %q, want process-key", store.credentials.OpenAIAPIKey)
	}
	if store.credentials.AnthropicAPIKey != "process-token" || store.credentials.AnthropicAuthHeader != "Authorization" || store.credentials.AnthropicAuthScheme != "Bearer" {
		t.Errorf("persisted Anthropic fallback = %#v", store.credentials)
	}
}
