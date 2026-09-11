package api

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
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

func TestSettingsCapabilityGatewayTokensAreIndependent(t *testing.T) {
	ctx := context.Background()
	store := &settingsStoreFake{gateway: domain.CapabilityGatewaySettings{
		Addr:       "http://octobus",
		Token:      "capset-token",
		AdminToken: "admin-token",
	}}
	handler := NewSettingsV2Handler(&appconfig.Config{DataRoot: t.TempDir()}, store)

	configured, err := handler.GetCapabilityGatewayConfig(ctx, connect.NewRequest(&agentcomposev2.GetCapabilityGatewayConfigRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !configured.Msg.GetConfig().GetTokenSet() || !configured.Msg.GetConfig().GetAdminTokenSet() {
		t.Fatalf("configured tokens = %+v", configured.Msg.GetConfig())
	}

	empty := ""
	if _, err := handler.UpdateCapabilityGatewayConfig(ctx, connect.NewRequest(&agentcomposev2.UpdateCapabilityGatewayConfigRequest{
		AdminToken: &empty,
	})); err != nil {
		t.Fatal(err)
	}
	if store.gateway.Token != "capset-token" || store.gateway.AdminToken != "" {
		t.Fatalf("gateway after admin clear = %+v", store.gateway)
	}

	replacement := "replacement-capset-token"
	updated, err := handler.UpdateCapabilityGatewayConfig(ctx, connect.NewRequest(&agentcomposev2.UpdateCapabilityGatewayConfigRequest{
		Token: &replacement,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if store.gateway.Token != replacement || store.gateway.AdminToken != "" {
		t.Fatalf("gateway after capset update = %+v", store.gateway)
	}
	if !updated.Msg.GetConfig().GetTokenSet() || updated.Msg.GetConfig().GetAdminTokenSet() {
		t.Fatalf("updated token state = %+v", updated.Msg.GetConfig())
	}
}

type settingsStoreFake struct {
	env     []domain.SandboxEnvVar
	gateway domain.CapabilityGatewaySettings
}

func (s *settingsStoreFake) ListGlobalEnv(context.Context) ([]domain.SandboxEnvVar, error) {
	return append([]domain.SandboxEnvVar(nil), s.env...), nil
}
func (s *settingsStoreFake) ReplaceGlobalEnv(_ context.Context, items []domain.SandboxEnvVar) ([]domain.SandboxEnvVar, error) {
	s.env = append([]domain.SandboxEnvVar(nil), items...)
	return s.env, nil
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
func (s *settingsStoreFake) GetCapabilityGateway(context.Context) (domain.CapabilityGatewaySettings, error) {
	return s.gateway, nil
}
func (s *settingsStoreFake) SaveCapabilityGateway(_ context.Context, item domain.CapabilityGatewaySettings) (domain.CapabilityGatewaySettings, error) {
	s.gateway = item
	return item, nil
}
