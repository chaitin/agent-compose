package agentcompose

import (
	"agent-compose/pkg/bus"

	"github.com/samber/do/v2"
)

type LoaderBus = bus.LoaderBus

func NewLoaderBus(di do.Injector) (*LoaderBus, error) {
	return bus.NewLoaderBus(di)
}

func NewLoaderBusWithBuffer(size int) *LoaderBus {
	return bus.NewLoaderBusWithBuffer(size)
}
