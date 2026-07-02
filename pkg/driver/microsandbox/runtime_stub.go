//go:build !cgo

package microsandboxdriver

import (
	appconfig "agent-compose/pkg/config"
	. "agent-compose/pkg/driver/types"
	"context"
	"fmt"
)

type microsandboxRuntime struct{}

func NewRuntime(_ *appconfig.Config) (BoxRuntime, error) {
	return &microsandboxRuntime{}, nil
}

func (r *microsandboxRuntime) EnsureSession(context.Context, *Session, VMState, ProxyState) (SessionVMInfo, error) {
	return SessionVMInfo{}, fmt.Errorf("agent-compose was built without cgo support; microsandbox runtime is unavailable")
}

func (r *microsandboxRuntime) StopSession(context.Context, *Session, VMState) (bool, error) {
	return false, fmt.Errorf("agent-compose was built without cgo support; microsandbox runtime is unavailable")
}

func (r *microsandboxRuntime) Exec(context.Context, *Session, VMState, ExecSpec) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("agent-compose was built without cgo support; microsandbox runtime is unavailable")
}

func (r *microsandboxRuntime) ExecStream(context.Context, *Session, VMState, ExecSpec, ExecStreamWriter) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("agent-compose was built without cgo support; microsandbox runtime is unavailable")
}

func (r *microsandboxRuntime) IsSessionAlive(context.Context, *Session, VMState) (bool, error) {
	return false, nil
}
