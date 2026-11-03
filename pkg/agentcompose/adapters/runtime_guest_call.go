package adapters

import (
	"context"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// Optional driver-level guest-file capabilities. They are named so each method
// states the exact driver signature it needs, and so the helper below can be
// instantiated without repeating an anonymous interface type.
type (
	driverGuestFileReader interface {
		ReadGuestFile(context.Context, *driverpkg.Sandbox, driverpkg.VMState, string) ([]byte, error)
	}
	driverGuestDirReader interface {
		ReadGuestDir(context.Context, *driverpkg.Sandbox, driverpkg.VMState, string, string) error
	}
	driverGuestFileWriter interface {
		WriteGuestFile(context.Context, *driverpkg.Sandbox, driverpkg.VMState, string, []byte) error
	}
	driverGuestDirWriter interface {
		WriteGuestDir(context.Context, *driverpkg.Sandbox, driverpkg.VMState, string, string) error
	}
	driverGuestDirectoryPublisher interface {
		PublishGuestDirectory(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.GuestDirectoryPublication) error
	}
	driverGuestSymlinkProjector interface {
		EnsureGuestSymlink(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.GuestSymlinkProjection) error
	}
)

// guestRuntimeTarget is the request-scoped input common to every guest-file
// capability.
type guestRuntimeTarget struct {
	session     *domain.Sandbox
	vmState     domain.VMState
	unsupported string
}

// guestRuntimeCall resolves the driver-boundary sandbox and invokes one optional
// guest-file capability on the wrapped runtime.
//
// Every path must go through driverSandbox: the guest-file methods can
// materialize the runtime implicitly, so handing the driver a sandbox without
// its declared network policy would create a Pod or container before the
// enforcement gate saw the declaration. An unsupported runtime is reported the
// same way from every entry point.
func guestRuntimeCall[Capability any, T any](
	r guestFileRuntimeAdapter,
	ctx context.Context,
	target guestRuntimeTarget,
	call func(Capability, *driverpkg.Sandbox, driverpkg.VMState) (T, error),
) (T, error) {
	var zero T
	capability, ok := r.runtime.(Capability)
	if !ok {
		return zero, domain.ClassifyError(domain.ErrUnsupported, target.unsupported, nil)
	}
	driverSandbox, _, err := r.driverSandbox(ctx, target.session)
	if err != nil {
		return zero, err
	}
	return call(capability, driverSandbox, execution.ToDriverVMState(target.vmState))
}

// guestRuntimeCallErr is guestRuntimeCall for capabilities that only report an
// error.
func guestRuntimeCallErr[Capability any](
	r guestFileRuntimeAdapter,
	ctx context.Context,
	target guestRuntimeTarget,
	call func(Capability, *driverpkg.Sandbox, driverpkg.VMState) error,
) error {
	_, err := guestRuntimeCall[Capability, struct{}](r, ctx, target,
		func(capability Capability, sandbox *driverpkg.Sandbox, state driverpkg.VMState) (struct{}, error) {
			return struct{}{}, call(capability, sandbox, state)
		})
	return err
}
