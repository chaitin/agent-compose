//go:build !boxlitecgo

package driver

import (
	appconfig "agent-compose/internal/config"
	"context"
	"fmt"
)

type stubBoxRuntime struct{}

func newBoxRuntime(_ *appconfig.Config) (BoxRuntime, error) {
	return &stubBoxRuntime{}, nil
}

func (s *stubBoxRuntime) EnsureSession(context.Context, *Session, VMState, ProxyState) (SessionVMInfo, error) {
	return SessionVMInfo{}, fmt.Errorf("agent-compose was built without BoxLite cgo support; rebuild with -tags boxlitecgo after preparing libboxlite")
}

func (s *stubBoxRuntime) StopSession(context.Context, *Session, VMState) (bool, error) {
	return false, fmt.Errorf("agent-compose was built without BoxLite cgo support; rebuild with -tags boxlitecgo after preparing libboxlite")
}

func (s *stubBoxRuntime) Exec(context.Context, *Session, VMState, ExecSpec) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("agent-compose was built without BoxLite cgo support; rebuild with -tags boxlitecgo after preparing libboxlite")
}

func (s *stubBoxRuntime) ExecStream(context.Context, *Session, VMState, ExecSpec, ExecStreamWriter) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("agent-compose was built without BoxLite cgo support; rebuild with -tags boxlitecgo after preparing libboxlite")
}
