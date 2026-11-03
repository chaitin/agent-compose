package adapters

import (
	"context"
	"errors"
	"testing"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// A runtime that does not expose the optional guest-file capabilities must be
// reported the same way from every entry point. Before these calls shared one
// helper, the publish and symlink paths returned a plain error while the
// read/write paths classified ErrUnsupported, so the same condition mapped to
// different Connect codes at the RPC boundary.
func TestGuestFileCapabilitiesReportUnsupportedConsistently(t *testing.T) {
	adapter := guestFileRuntimeAdapter{driverRuntimeAdapter{runtime: &capturingNetworkRuntime{}}}
	ctx := context.Background()
	var session *domain.Sandbox
	var vmState domain.VMState

	calls := []struct {
		name string
		call func() error
	}{
		{name: "ReadGuestFile", call: func() error {
			_, err := adapter.ReadGuestFile(ctx, session, vmState, "/tmp/x")
			return err
		}},
		{name: "ReadGuestDir", call: func() error {
			return adapter.ReadGuestDir(ctx, session, vmState, "/tmp/x", "/tmp/y")
		}},
		{name: "WriteGuestFile", call: func() error {
			return adapter.WriteGuestFile(ctx, session, vmState, "/tmp/x", []byte("x"))
		}},
		{name: "WriteGuestDir", call: func() error {
			return adapter.WriteGuestDir(ctx, session, vmState, "/tmp/x", "/tmp/y")
		}},
		{name: "PublishGuestDirectory", call: func() error {
			return adapter.PublishGuestDirectory(ctx, session, vmState, driverpkg.GuestDirectoryPublication{})
		}},
		{name: "EnsureGuestSymlink", call: func() error {
			return adapter.EnsureGuestSymlink(ctx, session, vmState, driverpkg.GuestSymlinkProjection{})
		}},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			err := call.call()
			if !errors.Is(err, domain.ErrUnsupported) {
				t.Fatalf("error = %v, want domain ErrUnsupported", err)
			}
		})
	}
}
