package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"context"
	"fmt"

	"github.com/samber/do/v2"
)

type BoxRuntime interface {
	EnsureSession(context.Context, *Session, VMState, ProxyState) (domain.SessionVMInfo, error)
	StopSession(context.Context, *Session, VMState) (bool, error)
	Exec(context.Context, *Session, VMState, ExecSpec) (ExecResult, error)
	ExecStream(context.Context, *Session, VMState, ExecSpec, ExecStreamWriter) (ExecResult, error)
}

type sessionAliveRuntime interface {
	IsSessionAlive(context.Context, *Session, VMState) (bool, error)
}

type RuntimeProvider interface {
	ForDriver(string) (BoxRuntime, error)
	ForSession(*Session) (BoxRuntime, error)
}

type runtimeProvider struct {
	config   *appconfig.Config
	runtimes map[string]BoxRuntime
}

type driverRuntimeAdapter struct {
	runtime driverpkg.BoxRuntime
}

func NewRuntimeProvider(di do.Injector) (RuntimeProvider, error) {
	config := do.MustInvoke[*appconfig.Config](di)
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
	return &runtimeProvider{
		config: config,
		runtimes: map[string]BoxRuntime{
			driverpkg.RuntimeDriverBoxlite:      driverRuntimeAdapter{runtime: boxliteRuntime},
			driverpkg.RuntimeDriverDocker:       driverRuntimeAdapter{runtime: dockerRuntime},
			driverpkg.RuntimeDriverMicrosandbox: driverRuntimeAdapter{runtime: microsandboxRuntime},
		},
	}, nil
}

func (p *runtimeProvider) ForDriver(driver string) (BoxRuntime, error) {
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

func (p *runtimeProvider) ForSession(session *Session) (BoxRuntime, error) {
	if session == nil {
		return nil, fmt.Errorf("session is required")
	}
	driver, err := driverpkg.ResolveSessionRuntimeDriver(session.Summary.Driver, p.config.RuntimeDriver)
	if err != nil {
		return nil, err
	}
	return p.ForDriver(driver)
}

func (r driverRuntimeAdapter) EnsureSession(ctx context.Context, session *Session, vmState VMState, proxyState ProxyState) (domain.SessionVMInfo, error) {
	info, err := r.runtime.EnsureSession(ctx, execution.ToDriverSession(session), execution.ToDriverVMState(vmState), execution.ToDriverProxyState(proxyState))
	if err != nil {
		return domain.SessionVMInfo{}, err
	}
	return execution.FromDriverSessionVMInfo(info), nil
}

func (r driverRuntimeAdapter) StopSession(ctx context.Context, session *Session, vmState VMState) (bool, error) {
	return r.runtime.StopSession(ctx, execution.ToDriverSession(session), execution.ToDriverVMState(vmState))
}

func (r driverRuntimeAdapter) Exec(ctx context.Context, session *Session, vmState VMState, spec ExecSpec) (ExecResult, error) {
	result, err := r.runtime.Exec(ctx, execution.ToDriverSession(session), execution.ToDriverVMState(vmState), execution.ToDriverExecSpec(spec))
	return execution.FromDriverExecResult(result), err
}

func (r driverRuntimeAdapter) ExecStream(ctx context.Context, session *Session, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	driverStream := func(chunk driverpkg.ExecChunk) {
		if stream != nil {
			stream(ExecChunk{Text: chunk.Text, IsStderr: chunk.IsStderr})
		}
	}
	result, err := r.runtime.ExecStream(ctx, execution.ToDriverSession(session), execution.ToDriverVMState(vmState), execution.ToDriverExecSpec(spec), driverStream)
	return execution.FromDriverExecResult(result), err
}

func (r driverRuntimeAdapter) IsSessionAlive(ctx context.Context, session *Session, vmState VMState) (bool, error) {
	aliveRuntime, ok := r.runtime.(interface {
		IsSessionAlive(context.Context, *driverpkg.Session, driverpkg.VMState) (bool, error)
	})
	if !ok {
		return false, fmt.Errorf("runtime does not support session liveness checks")
	}
	return aliveRuntime.IsSessionAlive(ctx, execution.ToDriverSession(session), execution.ToDriverVMState(vmState))
}
