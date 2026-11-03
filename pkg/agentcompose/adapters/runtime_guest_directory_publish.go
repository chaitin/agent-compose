package adapters

import (
	"context"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func (r guestFileRuntimeAdapter) PublishGuestDirectory(ctx context.Context, sandbox *domain.Sandbox, vmState domain.VMState, publication driverpkg.GuestDirectoryPublication) error {
	return guestRuntimeCallErr[driverGuestDirectoryPublisher](r, ctx, guestRuntimeTarget{
		session: sandbox, vmState: vmState,
		unsupported: "runtime does not support guest directory publication",
	}, func(capability driverGuestDirectoryPublisher, driverSandbox *driverpkg.Sandbox, state driverpkg.VMState) error {
		return capability.PublishGuestDirectory(ctx, driverSandbox, state, publication)
	})
}

func (r guestFileRuntimeAdapter) EnsureGuestSymlink(ctx context.Context, sandbox *domain.Sandbox, vmState domain.VMState, projection driverpkg.GuestSymlinkProjection) error {
	return guestRuntimeCallErr[driverGuestSymlinkProjector](r, ctx, guestRuntimeTarget{
		session: sandbox, vmState: vmState,
		unsupported: "runtime does not support guest symlink projection",
	}, func(capability driverGuestSymlinkProjector, driverSandbox *driverpkg.Sandbox, state driverpkg.VMState) error {
		return capability.EnsureGuestSymlink(ctx, driverSandbox, state, projection)
	})
}
