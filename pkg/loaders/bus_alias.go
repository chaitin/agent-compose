package loaders

import owner "agent-compose/pkg/bus"

type Bus = owner.Bus

var (
	NewBus           = owner.NewBus
	NewBusWithBuffer = owner.NewBusWithBuffer
)
