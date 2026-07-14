package adapters

import (
	"context"
	"fmt"
)

type HostPortStore interface {
	AllocateHostPortForSandbox(string) (int, error)
}

type StorePortAllocator struct {
	Store HostPortStore
}

func (a StorePortAllocator) AllocateHostPort(_ context.Context, sandboxID string) (int, error) {
	if a.Store == nil {
		return 0, fmt.Errorf("host port store is required")
	}
	return a.Store.AllocateHostPortForSandbox(sandboxID)
}
