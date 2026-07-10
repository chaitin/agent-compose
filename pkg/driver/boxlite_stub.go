//go:build !boxlitecgo

package driver

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"fmt"
)

type stubSandboxRuntime struct{}

func newSandboxRuntime(_ *appconfig.Config) (SandboxRuntime, error) {
	return &stubSandboxRuntime{}, nil
}

func (s *stubSandboxRuntime) EnsureSandbox(context.Context, *Sandbox, VMState, ProxyState) (SandboxVMInfo, error) {
	return SandboxVMInfo{}, fmt.Errorf("agent-compose was built without BoxLite cgo support; rebuild with -tags boxlitecgo after preparing libboxlite")
}

func (s *stubSandboxRuntime) StopSandbox(context.Context, *Sandbox, VMState) (bool, error) {
	return false, fmt.Errorf("agent-compose was built without BoxLite cgo support; rebuild with -tags boxlitecgo after preparing libboxlite")
}

func (s *stubSandboxRuntime) RemoveSandbox(context.Context, *Sandbox, VMState) error {
	return fmt.Errorf("agent-compose was built without BoxLite cgo support; rebuild with -tags boxlitecgo after preparing libboxlite")
}

func (s *stubSandboxRuntime) Exec(context.Context, *Sandbox, VMState, ExecSpec) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("agent-compose was built without BoxLite cgo support; rebuild with -tags boxlitecgo after preparing libboxlite")
}

func (s *stubSandboxRuntime) ExecStream(context.Context, *Sandbox, VMState, ExecSpec, ExecStreamWriter) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("agent-compose was built without BoxLite cgo support; rebuild with -tags boxlitecgo after preparing libboxlite")
}

func (s *stubSandboxRuntime) InteractionCapabilities() RuntimeInteractionCapabilities {
	return RuntimeInteractionCapabilities{}
}

func (s *stubSandboxRuntime) OpenInteraction(ctx context.Context, session *Sandbox, vmState VMState, spec RuntimeStartSpec) (RuntimeInteraction, error) {
	return UnsupportedRuntimeInteraction(RuntimeDriverBoxlite, s.InteractionCapabilities(), spec)
}
