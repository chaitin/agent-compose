package agentcompose

import (
	"context"

	"agent-compose/pkg/events"
	"agent-compose/pkg/loaders"
)

func NewEventDispatcher(rootCtx context.Context, configDB *ConfigStore, bus *loaders.Bus) *events.Dispatcher {
	return events.NewDispatcher(rootCtx, configDB, bus)
}
