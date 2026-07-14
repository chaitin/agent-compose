package adapters

import (
	"context"
	"fmt"
)

type HostPortStore interface {
	AllocateHostPort() (int, error)
}

type StorePortAllocator struct {
	Store HostPortStore
}

func (a StorePortAllocator) AllocateHostPort(context.Context) (int, error) {
	if a.Store == nil {
		return 0, fmt.Errorf("host port store is required")
	}
	return a.Store.AllocateHostPort()
}
