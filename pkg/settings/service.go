package settings

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/model"
	"agent-compose/pkg/storage"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

type WorkspaceConfigHandler interface {
	ListWorkspaceConfigs(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.ListWorkspaceConfigsResponse], error)
	CreateWorkspaceConfig(context.Context, *connect.Request[agentcomposev1.CreateWorkspaceConfigRequest]) (*connect.Response[agentcomposev1.WorkspaceConfigResponse], error)
	UpdateWorkspaceConfig(context.Context, *connect.Request[agentcomposev1.UpdateWorkspaceConfigRequest]) (*connect.Response[agentcomposev1.WorkspaceConfigResponse], error)
	DeleteWorkspaceConfig(context.Context, *connect.Request[agentcomposev1.WorkspaceConfigIDRequest]) (*connect.Response[emptypb.Empty], error)
}

type CapabilityGatewayHandler interface {
	GetCapabilityGatewayConfig(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.CapabilityGatewayConfig], error)
	UpdateCapabilityGatewayConfig(context.Context, *connect.Request[agentcomposev1.UpdateCapabilityGatewayConfigRequest]) (*connect.Response[agentcomposev1.CapabilityGatewayConfig], error)
}

type Service struct {
	configDB   *storage.ConfigStore
	workspaces WorkspaceConfigHandler
	gateway    CapabilityGatewayHandler
}

func NewService(configDB *storage.ConfigStore, workspaces WorkspaceConfigHandler, gateway CapabilityGatewayHandler) *Service {
	return &Service{configDB: configDB, workspaces: workspaces, gateway: gateway}
}

func (s *Service) GetGlobalEnvConfig(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.GlobalEnvConfigResponse], error) {
	_ = req
	items, err := s.configDB.ListGlobalEnv(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(ToProtoGlobalEnvConfig(items)), nil
}

func (s *Service) UpdateGlobalEnvConfig(ctx context.Context, req *connect.Request[agentcomposev1.UpdateGlobalEnvConfigRequest]) (*connect.Response[agentcomposev1.GlobalEnvConfigResponse], error) {
	items := make([]model.SessionEnvVar, 0, len(req.Msg.GetEnvItems()))
	for _, item := range req.Msg.GetEnvItems() {
		items = append(items, model.SessionEnvVar{Name: item.GetName(), Value: item.GetValue(), Secret: item.GetSecret()})
	}
	items = normalizeEnvItems(items)
	items, err := s.preserveUnchangedGlobalEnvSecrets(ctx, items)
	if err != nil {
		return nil, err
	}
	saved, err := s.configDB.ReplaceGlobalEnv(ctx, items)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(ToProtoGlobalEnvConfig(saved)), nil
}

func (s *Service) preserveUnchangedGlobalEnvSecrets(ctx context.Context, items []model.SessionEnvVar) ([]model.SessionEnvVar, error) {
	existingItems, err := s.configDB.ListGlobalEnv(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	existingByName := make(map[string]model.SessionEnvVar, len(existingItems))
	for _, item := range existingItems {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		existingByName[name] = item
	}
	for index, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" || !item.Secret || strings.TrimSpace(item.Value) != "" {
			continue
		}
		existing, ok := existingByName[name]
		if !ok || !existing.Secret || existing.Value == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("secret env %s requires a value", name))
		}
		items[index].Value = existing.Value
	}
	return items, nil
}

func (s *Service) ListWorkspaceConfigs(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.ListWorkspaceConfigsResponse], error) {
	return s.workspaces.ListWorkspaceConfigs(ctx, req)
}

func (s *Service) CreateWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.CreateWorkspaceConfigRequest]) (*connect.Response[agentcomposev1.WorkspaceConfigResponse], error) {
	return s.workspaces.CreateWorkspaceConfig(ctx, req)
}

func (s *Service) UpdateWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.UpdateWorkspaceConfigRequest]) (*connect.Response[agentcomposev1.WorkspaceConfigResponse], error) {
	return s.workspaces.UpdateWorkspaceConfig(ctx, req)
}

func (s *Service) DeleteWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.WorkspaceConfigIDRequest]) (*connect.Response[emptypb.Empty], error) {
	return s.workspaces.DeleteWorkspaceConfig(ctx, req)
}

func (s *Service) GetCapabilityGatewayConfig(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.CapabilityGatewayConfig], error) {
	return s.gateway.GetCapabilityGatewayConfig(ctx, req)
}

func (s *Service) UpdateCapabilityGatewayConfig(ctx context.Context, req *connect.Request[agentcomposev1.UpdateCapabilityGatewayConfigRequest]) (*connect.Response[agentcomposev1.CapabilityGatewayConfig], error) {
	return s.gateway.UpdateCapabilityGatewayConfig(ctx, req)
}

func ToProtoGlobalEnvConfig(items []model.SessionEnvVar) *agentcomposev1.GlobalEnvConfigResponse {
	resp := &agentcomposev1.GlobalEnvConfigResponse{}
	for _, item := range items {
		value := item.Value
		if item.Secret && value != "" {
			value = "********"
		}
		resp.EnvItems = append(resp.EnvItems, &agentcomposev1.SessionEnvVar{Name: item.Name, Value: value, Secret: item.Secret})
	}
	return resp
}

func normalizeEnvItems(items []model.SessionEnvVar) []model.SessionEnvVar {
	merged := make(map[string]model.SessionEnvVar, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		merged[name] = model.SessionEnvVar{Name: name, Value: item.Value, Secret: item.Secret}
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]model.SessionEnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}
