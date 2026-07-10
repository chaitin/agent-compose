//go:build !cgo

package driver

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"fmt"
)

type microsandboxRuntime struct{}

func newMicrosandboxRuntime(_ *appconfig.Config) (SandboxRuntime, error) {
	return &microsandboxRuntime{}, nil
}

func (r *microsandboxRuntime) EnsureSandbox(context.Context, *Sandbox, VMState, ProxyState) (SandboxVMInfo, error) {
	return SandboxVMInfo{}, fmt.Errorf("agent-compose was built without cgo support; microsandbox runtime is unavailable")
}

func (r *microsandboxRuntime) StopSandbox(context.Context, *Sandbox, VMState) (bool, error) {
	return false, fmt.Errorf("agent-compose was built without cgo support; microsandbox runtime is unavailable")
}

func (r *microsandboxRuntime) RemoveSandbox(context.Context, *Sandbox, VMState) error {
	return fmt.Errorf("agent-compose was built without cgo support; microsandbox runtime is unavailable")
}

func (r *microsandboxRuntime) Exec(context.Context, *Sandbox, VMState, ExecSpec) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("agent-compose was built without cgo support; microsandbox runtime is unavailable")
}

func (r *microsandboxRuntime) ExecStream(context.Context, *Sandbox, VMState, ExecSpec, ExecStreamWriter) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("agent-compose was built without cgo support; microsandbox runtime is unavailable")
}

func (r *microsandboxRuntime) IsSandboxAlive(context.Context, *Sandbox, VMState) (bool, error) {
	return false, nil
}
