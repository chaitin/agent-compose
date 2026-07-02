//go:build !boxlitecgo

package boxlitedriver

import (
	appconfig "agent-compose/pkg/config"
	. "agent-compose/pkg/driver/types"
	"context"
	"fmt"
)

type stubBoxRuntime struct{}

func NewRuntime(_ *appconfig.Config) (BoxRuntime, error) {
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
