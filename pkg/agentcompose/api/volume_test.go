package api

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/volumes"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestVolumeHandlerWorkflows(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	manager := &fakeVolumeManager{
		items: []domain.VolumeRecord{{
			ID:        "vol-cache",
			Name:      "cache",
			Driver:    domain.VolumeDriverLocal,
			Path:      "/data/volumes/local/vol-cache/data",
			Labels:    map[string]string{"purpose": "cache"},
			CreatedAt: now,
			UpdatedAt: now,
		}},
	}
	handler := NewVolumeHandler(manager)

	listResp, err := handler.ListVolumes(ctx, connect.NewRequest(&agentcomposev2.ListVolumesRequest{Query: "cac", Driver: "local"}))
	if err != nil {
		t.Fatalf("ListVolumes returned error: %v", err)
	}
	if len(listResp.Msg.GetVolumes()) != 1 || listResp.Msg.GetVolumes()[0].GetName() != "cache" || manager.listOptions.Query != "cac" {
		t.Fatalf("list response=%#v options=%#v", listResp.Msg, manager.listOptions)
	}

	createResp, err := handler.CreateVolume(ctx, connect.NewRequest(&agentcomposev2.CreateVolumeRequest{
		Name:   "cache",
		Driver: "local",
		Labels: map[string]string{" purpose ": " cache "},
	}))
	if err != nil {
		t.Fatalf("CreateVolume returned error: %v", err)
	}
	if !createResp.Msg.GetCreated() || createResp.Msg.GetVolume().GetName() != "cache" || manager.ensureItem.Labels["purpose"] != "cache" {
		t.Fatalf("create response=%#v ensure=%#v", createResp.Msg, manager.ensureItem)
	}

	inspectResp, err := handler.InspectVolume(ctx, connect.NewRequest(&agentcomposev2.InspectVolumeRequest{Name: "cache"}))
	if err != nil {
		t.Fatalf("InspectVolume returned error: %v", err)
	}
	if inspectResp.Msg.GetVolume().GetVolumeId() != "vol-cache" || inspectResp.Msg.GetVolume().GetCreatedAt() == "" {
		t.Fatalf("inspect response = %#v", inspectResp.Msg)
	}

	removeResp, err := handler.RemoveVolume(ctx, connect.NewRequest(&agentcomposev2.RemoveVolumeRequest{Name: "cache", Force: true}))
	if err != nil {
		t.Fatalf("RemoveVolume returned error: %v", err)
	}
	if !removeResp.Msg.GetRemoved() || manager.removeName != "cache" || !manager.removeForce {
		t.Fatalf("remove response=%#v manager=%#v", removeResp.Msg, manager)
	}

	pruneResp, err := handler.PruneVolumes(ctx, connect.NewRequest(&agentcomposev2.PruneVolumesRequest{Force: true}))
	if err != nil {
		t.Fatalf("PruneVolumes returned error: %v", err)
	}
	if pruneResp.Msg.GetDryRun() || len(pruneResp.Msg.GetRemoved()) != 1 {
		t.Fatalf("prune response = %#v", pruneResp.Msg)
	}
}

type fakeVolumeManager struct {
	items       []domain.VolumeRecord
	listOptions domain.VolumeListOptions
	ensureItem  domain.VolumeRecord
	removeName  string
	removeForce bool
}

func (m *fakeVolumeManager) List(_ context.Context, options domain.VolumeListOptions) ([]domain.VolumeRecord, error) {
	m.listOptions = options
	return m.items, nil
}

func (m *fakeVolumeManager) Ensure(_ context.Context, item domain.VolumeRecord) (domain.VolumeRecord, bool, error) {
	m.ensureItem = item
	if len(m.items) > 0 {
		created := m.items[0]
		created.Labels = item.Labels
		return created, true, nil
	}
	return item, true, nil
}

func (m *fakeVolumeManager) Inspect(context.Context, string) (domain.VolumeRecord, error) {
	return m.items[0], nil
}

func (m *fakeVolumeManager) Remove(_ context.Context, name string, force bool) error {
	m.removeName = name
	m.removeForce = force
	return nil
}

func (m *fakeVolumeManager) Prune(context.Context, domain.VolumeListOptions, bool) (volumes.PruneResult, error) {
	return volumes.PruneResult{Removed: m.items}, nil
}
