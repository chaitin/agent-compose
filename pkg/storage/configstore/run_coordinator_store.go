package configstore

import (
	"context"

	"agent-compose/pkg/runs"
)

func (s *ConfigStore) GetManagedAgentDefinition(ctx context.Context, id string) (runs.ManagedAgentDefinition, error) {
	agent, err := s.GetAgentDefinition(ctx, id)
	if err != nil {
		return runs.ManagedAgentDefinition{}, err
	}
	return runs.ManagedAgentDefinition{
		ID:               agent.ID,
		Enabled:          agent.Enabled,
		DeletedAt:        agent.DeletedAt,
		Driver:           agent.Driver,
		GuestImage:       agent.GuestImage,
		ManagedProjectID: agent.ManagedProjectID,
		ManagedAgentName: agent.ManagedAgentName,
	}, nil
}
