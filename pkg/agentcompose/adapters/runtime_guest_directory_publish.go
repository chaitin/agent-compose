package adapters

import (
	"context"
	"fmt"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func (r guestFileRuntimeAdapter) PublishGuestDirectory(ctx context.Context, sandbox *domain.Sandbox, vmState domain.VMState, publication driverpkg.GuestDirectoryPublication) error {
	publisher, ok := r.runtime.(interface {
		PublishGuestDirectory(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.GuestDirectoryPublication) error
	})
	if !ok {
		return fmt.Errorf("runtime does not support guest directory publication")
	}
	return publisher.PublishGuestDirectory(ctx, execution.ToDriverSandbox(sandbox), execution.ToDriverVMState(vmState), publication)
}

func (r guestFileRuntimeAdapter) EnsureGuestSymlink(ctx context.Context, sandbox *domain.Sandbox, vmState domain.VMState, projection driverpkg.GuestSymlinkProjection) error {
	linker, ok := r.runtime.(interface {
		EnsureGuestSymlink(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.GuestSymlinkProjection) error
	})
	if !ok {
		return fmt.Errorf("runtime does not support guest symlink projection")
	}
	return linker.EnsureGuestSymlink(ctx, execution.ToDriverSandbox(sandbox), execution.ToDriverVMState(vmState), projection)
}
