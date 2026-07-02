package runtime

import (
	sessiondomain "agent-compose/internal/agentcompose/session"
	appconfig "agent-compose/internal/config"
	driverpkg "agent-compose/internal/driver"
	"context"
	"fmt"
)

type SessionVMInfo = sessiondomain.VMInfo

type BoxRuntime interface {
	EnsureSession(context.Context, *sessiondomain.Session, sessiondomain.VMState, sessiondomain.ProxyState) (SessionVMInfo, error)
	StopSession(context.Context, *sessiondomain.Session, sessiondomain.VMState) (bool, error)
	Exec(context.Context, *sessiondomain.Session, sessiondomain.VMState, sessiondomain.ExecSpec) (sessiondomain.ExecResult, error)
	ExecStream(context.Context, *sessiondomain.Session, sessiondomain.VMState, sessiondomain.ExecSpec, sessiondomain.ExecStreamWriter) (sessiondomain.ExecResult, error)
}

type SessionAliveRuntime interface {
	IsSessionAlive(context.Context, *sessiondomain.Session, sessiondomain.VMState) (bool, error)
}

type Provider interface {
	ForDriver(string) (BoxRuntime, error)
	ForSession(*sessiondomain.Session) (BoxRuntime, error)
}

type provider struct {
	config   *appconfig.Config
	runtimes map[string]BoxRuntime
}

type driverRuntimeAdapter struct {
	runtime driverpkg.BoxRuntime
}

func NewProvider(config *appconfig.Config) (Provider, error) {
	boxliteRuntime, err := driverpkg.NewBoxliteRuntime(config)
	if err != nil {
		return nil, err
	}
	dockerRuntime, err := driverpkg.NewDockerRuntime(config)
	if err != nil {
		return nil, err
	}
	microsandboxRuntime, err := driverpkg.NewMicrosandboxRuntime(config)
	if err != nil {
		return nil, err
	}
	return &provider{
		config: config,
		runtimes: map[string]BoxRuntime{
			driverpkg.RuntimeDriverBoxlite:      driverRuntimeAdapter{runtime: boxliteRuntime},
			driverpkg.RuntimeDriverDocker:       driverRuntimeAdapter{runtime: dockerRuntime},
			driverpkg.RuntimeDriverMicrosandbox: driverRuntimeAdapter{runtime: microsandboxRuntime},
		},
	}, nil
}

func (p *provider) ForDriver(driver string) (BoxRuntime, error) {
	driver = driverpkg.ResolveRuntimeDriver(driver)
	if err := driverpkg.ValidateRuntimeDriver(driver); err != nil {
		return nil, err
	}
	runtime, ok := p.runtimes[driver]
	if !ok {
		return nil, fmt.Errorf("agent-compose runtime %q is not configured", driver)
	}
	return runtime, nil
}

func (p *provider) ForSession(session *sessiondomain.Session) (BoxRuntime, error) {
	if session == nil {
		return nil, fmt.Errorf("session is required")
	}
	driver, err := driverpkg.ResolveSessionRuntimeDriver(session.Summary.Driver, p.config.RuntimeDriver)
	if err != nil {
		return nil, err
	}
	return p.ForDriver(driver)
}

func (r driverRuntimeAdapter) EnsureSession(ctx context.Context, session *sessiondomain.Session, vmState sessiondomain.VMState, proxyState sessiondomain.ProxyState) (SessionVMInfo, error) {
	info, err := r.runtime.EnsureSession(ctx, ToDriverSession(session), ToDriverVMState(vmState), ToDriverProxyState(proxyState))
	if err != nil {
		return SessionVMInfo{}, err
	}
	return FromDriverSessionVMInfo(info), nil
}

func (r driverRuntimeAdapter) StopSession(ctx context.Context, session *sessiondomain.Session, vmState sessiondomain.VMState) (bool, error) {
	return r.runtime.StopSession(ctx, ToDriverSession(session), ToDriverVMState(vmState))
}

func (r driverRuntimeAdapter) Exec(ctx context.Context, session *sessiondomain.Session, vmState sessiondomain.VMState, spec sessiondomain.ExecSpec) (sessiondomain.ExecResult, error) {
	result, err := r.runtime.Exec(ctx, ToDriverSession(session), ToDriverVMState(vmState), ToDriverExecSpec(spec))
	return FromDriverExecResult(result), err
}

func (r driverRuntimeAdapter) ExecStream(ctx context.Context, session *sessiondomain.Session, vmState sessiondomain.VMState, spec sessiondomain.ExecSpec, stream sessiondomain.ExecStreamWriter) (sessiondomain.ExecResult, error) {
	driverStream := func(chunk driverpkg.ExecChunk) {
		if stream != nil {
			stream(sessiondomain.ExecChunk{Text: chunk.Text, IsStderr: chunk.IsStderr})
		}
	}
	result, err := r.runtime.ExecStream(ctx, ToDriverSession(session), ToDriverVMState(vmState), ToDriverExecSpec(spec), driverStream)
	return FromDriverExecResult(result), err
}

func (r driverRuntimeAdapter) IsSessionAlive(ctx context.Context, session *sessiondomain.Session, vmState sessiondomain.VMState) (bool, error) {
	aliveRuntime, ok := r.runtime.(interface {
		IsSessionAlive(context.Context, *driverpkg.Session, driverpkg.VMState) (bool, error)
	})
	if !ok {
		return false, fmt.Errorf("runtime does not support session liveness checks")
	}
	return aliveRuntime.IsSessionAlive(ctx, ToDriverSession(session), ToDriverVMState(vmState))
}
