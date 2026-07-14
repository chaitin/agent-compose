package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type SettingsV2Handler struct {
	store      ConfigStore
	workspaces *workspaceSettings
}

func NewSettingsV2Handler(config *appconfig.Config, store ConfigStore) *SettingsV2Handler {
	return &SettingsV2Handler{store: store, workspaces: newWorkspaceSettings(config, store)}
}

func (h *SettingsV2Handler) GetGlobalEnv(ctx context.Context, _ *connect.Request[agentcomposev2.GetGlobalEnvRequest]) (*connect.Response[agentcomposev2.GetGlobalEnvResponse], error) {
	items, err := h.store.ListGlobalEnv(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev2.GetGlobalEnvResponse{Env: envToV2(items)}), nil
}
func (h *SettingsV2Handler) UpdateGlobalEnv(ctx context.Context, req *connect.Request[agentcomposev2.UpdateGlobalEnvRequest]) (*connect.Response[agentcomposev2.UpdateGlobalEnvResponse], error) {
	items, err := h.globalEnvUpdates(ctx, req.Msg.GetEnv())
	if err != nil {
		return nil, err
	}
	saved, err := h.store.ReplaceGlobalEnv(ctx, items)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev2.UpdateGlobalEnvResponse{Env: envToV2(saved)}), nil
}

func (h *SettingsV2Handler) globalEnvUpdates(ctx context.Context, updates []*agentcomposev2.EnvVarUpdateSpec) ([]domain.SandboxEnvVar, error) {
	existing, err := h.store.ListGlobalEnv(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	existingByName := make(map[string]domain.SandboxEnvVar, len(existing))
	for _, item := range existing {
		existingByName[strings.TrimSpace(item.Name)] = item
	}
	items := make([]domain.SandboxEnvVar, 0, len(updates))
	for _, update := range updates {
		name := strings.TrimSpace(update.GetName())
		item := domain.SandboxEnvVar{Name: name, Secret: update.GetSecret()}
		if update.Value != nil {
			item.Value = update.GetValue()
		} else if item.Secret {
			current, ok := existingByName[name]
			if !ok || !current.Secret || current.Value == "" {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("secret env %s requires a value", name))
			}
			item.Value = current.Value
		}
		items = append(items, item)
	}
	return domain.NormalizeEnvItems(items), nil
}
func (h *SettingsV2Handler) GetCapabilityGatewayConfig(ctx context.Context, _ *connect.Request[agentcomposev2.GetCapabilityGatewayConfigRequest]) (*connect.Response[agentcomposev2.GetCapabilityGatewayConfigResponse], error) {
	settings, err := h.store.GetCapabilityGateway(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev2.GetCapabilityGatewayConfigResponse{Config: &agentcomposev2.CapabilityGatewayConfig{Addr: settings.Addr, TokenSet: strings.TrimSpace(settings.Token) != ""}}), nil
}
func (h *SettingsV2Handler) UpdateCapabilityGatewayConfig(ctx context.Context, req *connect.Request[agentcomposev2.UpdateCapabilityGatewayConfigRequest]) (*connect.Response[agentcomposev2.UpdateCapabilityGatewayConfigResponse], error) {
	current, err := h.store.GetCapabilityGateway(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if req.Msg.Addr != nil {
		current.Addr = req.Msg.GetAddr()
	}
	if req.Msg.Token != nil {
		current.Token = req.Msg.GetToken()
	}
	saved, err := h.store.SaveCapabilityGateway(ctx, current)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev2.UpdateCapabilityGatewayConfigResponse{Config: &agentcomposev2.CapabilityGatewayConfig{Addr: saved.Addr, TokenSet: strings.TrimSpace(saved.Token) != ""}}), nil
}
func (h *SettingsV2Handler) ListWorkspacePresets(ctx context.Context, _ *connect.Request[agentcomposev2.ListWorkspacePresetsRequest]) (*connect.Response[agentcomposev2.ListWorkspacePresetsResponse], error) {
	items, err := h.store.ListWorkspaceConfigs(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev2.ListWorkspacePresetsResponse{}
	for _, item := range items {
		resp.Presets = append(resp.Presets, workspacePresetToV2(item))
	}
	return connect.NewResponse(resp), nil
}
func (h *SettingsV2Handler) CreateWorkspacePreset(ctx context.Context, req *connect.Request[agentcomposev2.CreateWorkspacePresetRequest]) (*connect.Response[agentcomposev2.WorkspacePresetResponse], error) {
	item, err := h.workspaces.createWorkspaceConfig(ctx, domain.WorkspaceConfig{Name: req.Msg.GetName(), Type: req.Msg.GetType(), ConfigJSON: req.Msg.GetConfigJson(), Comment: req.Msg.GetComment()})
	if err != nil {
		return nil, connect.NewError(workspaceErrorCode(err), err)
	}
	return connect.NewResponse(&agentcomposev2.WorkspacePresetResponse{Preset: workspacePresetToV2(item)}), nil
}
func (h *SettingsV2Handler) UpdateWorkspacePreset(ctx context.Context, req *connect.Request[agentcomposev2.UpdateWorkspacePresetRequest]) (*connect.Response[agentcomposev2.WorkspacePresetResponse], error) {
	item, err := h.workspaces.updateWorkspaceConfig(ctx, domain.WorkspaceConfig{ID: req.Msg.GetPresetId(), Name: req.Msg.GetName(), Type: req.Msg.GetType(), ConfigJSON: req.Msg.GetConfigJson(), Comment: req.Msg.GetComment()})
	if err != nil {
		return nil, connect.NewError(workspaceErrorCode(err), err)
	}
	return connect.NewResponse(&agentcomposev2.WorkspacePresetResponse{Preset: workspacePresetToV2(item)}), nil
}
func (h *SettingsV2Handler) DeleteWorkspacePreset(ctx context.Context, req *connect.Request[agentcomposev2.DeleteWorkspacePresetRequest]) (*connect.Response[agentcomposev2.DeleteWorkspacePresetResponse], error) {
	err := h.workspaces.deleteWorkspaceConfig(ctx, req.Msg.GetPresetId())
	if err != nil {
		return nil, connect.NewError(workspaceErrorCode(err), err)
	}
	return connect.NewResponse(&agentcomposev2.DeleteWorkspacePresetResponse{}), nil
}

func workspaceErrorCode(err error) connect.Code {
	if errors.Is(err, errWorkspaceContent) {
		return connect.CodeInternal
	}
	if errors.Is(err, domain.ErrReferenced) {
		return connect.CodeFailedPrecondition
	}
	return connect.CodeInvalidArgument
}

func envToV2(items []domain.SandboxEnvVar) []*agentcomposev2.EnvVarSpec {
	result := make([]*agentcomposev2.EnvVarSpec, 0, len(items))
	for _, item := range items {
		value := item.Value
		if item.Secret {
			value = secretRedactedValue
		}
		result = append(result, &agentcomposev2.EnvVarSpec{Name: item.Name, Value: value, Secret: item.Secret})
	}
	return result
}
func workspacePresetToV2(item domain.WorkspaceConfig) *agentcomposev2.WorkspacePreset {
	return &agentcomposev2.WorkspacePreset{Id: item.ID, Name: item.Name, Type: item.Type, ConfigJson: item.ConfigJSON, Comment: item.Comment, CreatedAt: timestamppb.New(item.CreatedAt), UpdatedAt: timestamppb.New(item.UpdatedAt)}
}
