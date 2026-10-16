package adapters

import (
	"context"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// PreflightSandboxRuntime reports whether the runtime a sandbox would use can be
// used at all, before the caller creates a sandbox record or runtime state. The
// run path asks it before creating the record; the start path asks the runtime
// adapter for the same capability before creating runtime state.
//
// A driver whose runtime has no way to judge its own usability is reported as
// usable: the capability only ever adds a check, it never blocks a driver that
// cannot answer it.
func (d *SandboxDriver) PreflightSandboxRuntime(ctx context.Context, session *domain.Sandbox, vmState domain.VMState) error {
	if session == nil || d.Runtimes == nil {
		return nil
	}
	driver := strings.TrimSpace(vmState.Driver)
	if driver == "" {
		driver = strings.TrimSpace(session.Summary.Driver)
	}
	runtime, err := d.Runtimes.ForDriver(driver)
	if err != nil {
		return err
	}
	preflight, ok := runtime.(SandboxRuntimePreflight)
	if !ok {
		return nil
	}
	return preflight.PreflightSandboxRuntime(ctx, session, vmState)
}
