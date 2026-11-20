package runs

import (
	"context"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// guestFileWriterFor returns an execution.GuestFileWriterFunc that pushes a
// file into sandbox's Pod over Exec, or nil if runtime's driver has a real
// shared filesystem with the daemon and has no need for one (docker/
// boxlite/microsandbox today). runtime is typed as
// this package's own Runtime interface (only ExecStream), so the type
// assertion below is against the concrete value it holds - the same
// pattern pkg/agentcompose/adapters.driverRuntimeAdapter uses internally
// for Stats/IsSandboxAlive - not against Runtime's declared method set.
func guestFileWriterFor(runtime Runtime, sandbox *domain.Sandbox, vmState domain.VMState) execution.GuestFileWriterFunc {
	writer, ok := runtime.(interface {
		WriteGuestFile(context.Context, *domain.Sandbox, domain.VMState, string, []byte) error
	})
	if !ok {
		return nil
	}
	return func(ctx context.Context, guestPath string, content []byte) error {
		return writer.WriteGuestFile(ctx, sandbox, vmState, guestPath, content)
	}
}

// sandboxGuestFileWriter resolves sandbox's runtime and VM state and
// returns a guest file writer for it, or nil if either can't be resolved
// or the driver has no need for one (see guestFileWriterFor). Mirrors
// pkg/agentcompose/adapters.AgentRunner.guestFileWriterFor for callers that
// only have a Controller, not an AgentRunner, at hand.
func (c *Controller) sandboxGuestFileWriter(sandbox *domain.Sandbox) execution.GuestFileWriterFunc {
	if c == nil || c.runtime == nil || c.store == nil || sandbox == nil {
		return nil
	}
	vmState, err := c.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		return nil
	}
	runtime, err := c.runtime(sandbox)
	if err != nil {
		return nil
	}
	return guestFileWriterFor(runtime, sandbox, vmState)
}

// guestDirReaderFor returns the pull-side directory capability for runtimes
// without a shared filesystem. A nil result means the guest artifact
// directory is already visible at its daemon-local path.
func guestDirReaderFor(runtime Runtime, sandbox *domain.Sandbox, vmState domain.VMState) execution.GuestDirReaderFunc {
	reader, ok := runtime.(interface {
		ReadGuestDir(context.Context, *domain.Sandbox, domain.VMState, string, string) error
	})
	if !ok {
		return nil
	}
	return func(ctx context.Context, guestDir, hostDestDir string) error {
		return reader.ReadGuestDir(ctx, sandbox, vmState, guestDir, hostDestDir)
	}
}
