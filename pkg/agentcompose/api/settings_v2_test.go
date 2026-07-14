package api

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	appconfig "agent-compose/pkg/config"
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

type settingsStoreFake struct{ env []domain.SandboxEnvVar }

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
func (*settingsStoreFake) GetCapabilityGateway(context.Context) (domain.CapabilityGatewaySettings, error) {
	return domain.CapabilityGatewaySettings{}, nil
}
func (*settingsStoreFake) SaveCapabilityGateway(_ context.Context, item domain.CapabilityGatewaySettings) (domain.CapabilityGatewaySettings, error) {
	return item, nil
}
