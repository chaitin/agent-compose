package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/volumes"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type VolumeManager interface {
	List(context.Context, domain.VolumeListOptions) ([]domain.VolumeRecord, error)
	Ensure(context.Context, domain.VolumeRecord) (domain.VolumeRecord, bool, error)
	Inspect(context.Context, string) (domain.VolumeRecord, error)
	Remove(context.Context, string, bool) error
	Prune(context.Context, domain.VolumeListOptions, bool) (volumes.PruneResult, error)
}

type VolumeHandler struct {
	manager VolumeManager
}

func NewVolumeHandler(manager VolumeManager) *VolumeHandler {
	return &VolumeHandler{manager: manager}
}

func (h *VolumeHandler) ListVolumes(ctx context.Context, req *connect.Request[agentcomposev2.ListVolumesRequest]) (*connect.Response[agentcomposev2.ListVolumesResponse], error) {
	if h == nil || h.manager == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("volume manager is unavailable"))
	}
	items, err := h.manager.List(ctx, domain.VolumeListOptions{
		Query:     strings.TrimSpace(req.Msg.GetQuery()),
		Driver:    strings.TrimSpace(req.Msg.GetDriver()),
		ProjectID: strings.TrimSpace(req.Msg.GetProjectId()),
	})
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.ListVolumesResponse{Volumes: VolumesToProto(items)}), nil
}

func (h *VolumeHandler) CreateVolume(ctx context.Context, req *connect.Request[agentcomposev2.CreateVolumeRequest]) (*connect.Response[agentcomposev2.CreateVolumeResponse], error) {
	if h == nil || h.manager == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("volume manager is unavailable"))
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("volume name is required"))
	}
	item, created, err := h.manager.Ensure(ctx, domain.VolumeRecord{
		Name:    name,
		Driver:  strings.TrimSpace(req.Msg.GetDriver()),
		Labels:  cloneVolumeStringMap(req.Msg.GetLabels()),
		Options: cloneVolumeStringMap(req.Msg.GetOptions()),
	})
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.CreateVolumeResponse{Volume: VolumeToProto(item), Created: created}), nil
}

func (h *VolumeHandler) InspectVolume(ctx context.Context, req *connect.Request[agentcomposev2.InspectVolumeRequest]) (*connect.Response[agentcomposev2.InspectVolumeResponse], error) {
	if h == nil || h.manager == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("volume manager is unavailable"))
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("volume name is required"))
	}
	item, err := h.manager.Inspect(ctx, name)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.InspectVolumeResponse{Volume: VolumeToProto(item)}), nil
}

func (h *VolumeHandler) RemoveVolume(ctx context.Context, req *connect.Request[agentcomposev2.RemoveVolumeRequest]) (*connect.Response[agentcomposev2.RemoveVolumeResponse], error) {
	if h == nil || h.manager == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("volume manager is unavailable"))
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("volume name is required"))
	}
	if err := h.manager.Remove(ctx, name, req.Msg.GetForce()); err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.RemoveVolumeResponse{Name: name, Removed: true}), nil
}

func (h *VolumeHandler) PruneVolumes(ctx context.Context, req *connect.Request[agentcomposev2.PruneVolumesRequest]) (*connect.Response[agentcomposev2.PruneVolumesResponse], error) {
	if h == nil || h.manager == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("volume manager is unavailable"))
	}
	result, err := h.manager.Prune(ctx, domain.VolumeListOptions{
		Query:     strings.TrimSpace(req.Msg.GetQuery()),
		Driver:    strings.TrimSpace(req.Msg.GetDriver()),
		ProjectID: strings.TrimSpace(req.Msg.GetProjectId()),
	}, req.Msg.GetForce())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.PruneVolumesResponse{
		DryRun:  result.DryRun,
		Matched: VolumesToProto(result.Matched),
		Removed: VolumesToProto(result.Removed),
		Skipped: VolumesToProto(result.Skipped),
	}), nil
}

func VolumesToProto(items []domain.VolumeRecord) []*agentcomposev2.Volume {
	out := make([]*agentcomposev2.Volume, 0, len(items))
	for _, item := range items {
		out = append(out, VolumeToProto(item))
	}
	return out
}

func VolumeToProto(item domain.VolumeRecord) *agentcomposev2.Volume {
	return &agentcomposev2.Volume{
		Name:      item.Name,
		Driver:    item.Driver,
		Path:      item.Path,
		Labels:    cloneVolumeStringMap(item.Labels),
		Options:   cloneVolumeStringMap(item.Options),
		ProjectId: item.ProjectID,
		CreatedAt: formatVolumeTime(item.CreatedAt),
		UpdatedAt: formatVolumeTime(item.UpdatedAt),
	}
}

func formatVolumeTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func cloneVolumeStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	return out
}
