package runtimes

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/model"
	"context"
	"fmt"

	"github.com/samber/do/v2"
)

type SessionVMInfo = model.SessionVMInfo
type Session = model.Session
type VMState = model.VMState
type ProxyState = model.ProxyState
type ExecSpec = model.ExecSpec
type ExecResult = model.ExecResult
type ExecChunk = model.ExecChunk
type ExecStreamWriter = model.ExecStreamWriter

type BoxRuntime interface {
	EnsureSession(context.Context, *Session, VMState, ProxyState) (SessionVMInfo, error)
	StopSession(context.Context, *Session, VMState) (bool, error)
	Exec(context.Context, *Session, VMState, ExecSpec) (ExecResult, error)
	ExecStream(context.Context, *Session, VMState, ExecSpec, ExecStreamWriter) (ExecResult, error)
}

type AliveRuntime interface {
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

func (r driverRuntimeAdapter) EnsureSession(ctx context.Context, session *Session, vmState VMState, proxyState ProxyState) (SessionVMInfo, error) {
	info, err := r.runtime.EnsureSession(ctx, ToDriverSession(session), ToDriverVMState(vmState), ToDriverProxyState(proxyState))
	if err != nil {
		return SessionVMInfo{}, err
	}
	return FromDriverSessionVMInfo(info), nil
}

func (r driverRuntimeAdapter) StopSession(ctx context.Context, session *Session, vmState VMState) (bool, error) {
	return r.runtime.StopSession(ctx, ToDriverSession(session), ToDriverVMState(vmState))
}

func (r driverRuntimeAdapter) Exec(ctx context.Context, session *Session, vmState VMState, spec ExecSpec) (ExecResult, error) {
	result, err := r.runtime.Exec(ctx, ToDriverSession(session), ToDriverVMState(vmState), ToDriverExecSpec(spec))
	return FromDriverExecResult(result), err
}

func (r driverRuntimeAdapter) ExecStream(ctx context.Context, session *Session, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	driverStream := func(chunk driverpkg.ExecChunk) {
		if stream != nil {
			stream(ExecChunk{Text: chunk.Text, IsStderr: chunk.IsStderr})
		}
	}
	result, err := r.runtime.ExecStream(ctx, ToDriverSession(session), ToDriverVMState(vmState), ToDriverExecSpec(spec), driverStream)
	return FromDriverExecResult(result), err
}

func (r driverRuntimeAdapter) IsSessionAlive(ctx context.Context, session *Session, vmState VMState) (bool, error) {
	aliveRuntime, ok := r.runtime.(interface {
		IsSessionAlive(context.Context, *driverpkg.Session, driverpkg.VMState) (bool, error)
	})
	if !ok {
		return false, fmt.Errorf("runtime does not support session liveness checks")
	}
	return aliveRuntime.IsSessionAlive(ctx, ToDriverSession(session), ToDriverVMState(vmState))
}

func ToDriverSession(session *Session) *driverpkg.Session {
	if session == nil {
		return nil
	}
	envItems := make([]driverpkg.SessionEnvVar, 0, len(session.EnvItems))
	for _, item := range session.EnvItems {
		envItems = append(envItems, driverpkg.SessionEnvVar{Name: item.Name, Value: item.Value, Secret: item.Secret})
	}
	runtimeEnvItems := make([]driverpkg.SessionEnvVar, 0, len(session.RuntimeEnvItems))
	for _, item := range session.RuntimeEnvItems {
		runtimeEnvItems = append(runtimeEnvItems, driverpkg.SessionEnvVar{Name: item.Name, Value: item.Value, Secret: item.Secret})
	}
	return &driverpkg.Session{
		Summary: driverpkg.SessionSummary{
			ID:            session.Summary.ID,
			Driver:        session.Summary.Driver,
			GuestImage:    session.Summary.GuestImage,
			RuntimeRef:    session.Summary.RuntimeRef,
			WorkspacePath: session.Summary.WorkspacePath,
			CreatedAt:     session.Summary.CreatedAt,
			UpdatedAt:     session.Summary.UpdatedAt,
		},
		EnvItems:        envItems,
		RuntimeEnvItems: runtimeEnvItems,
	}
}

func ToDriverVMState(state VMState) driverpkg.VMState {
	return driverpkg.VMState{
		Driver:       state.Driver,
		Mode:         state.Mode,
		BoxName:      state.BoxName,
		BoxID:        state.BoxID,
		Image:        state.Image,
		Registry:     state.Registry,
		RuntimeHome:  state.RuntimeHome,
		StartedAt:    state.StartedAt,
		StoppedAt:    state.StoppedAt,
		LastError:    state.LastError,
		BootstrapRef: state.BootstrapRef,
	}
}

func FromDriverVMState(state driverpkg.VMState) VMState {
	return VMState{
		Driver:       state.Driver,
		Mode:         state.Mode,
		BoxName:      state.BoxName,
		BoxID:        state.BoxID,
		Image:        state.Image,
		Registry:     state.Registry,
		RuntimeHome:  state.RuntimeHome,
		StartedAt:    state.StartedAt,
		StoppedAt:    state.StoppedAt,
		LastError:    state.LastError,
		BootstrapRef: state.BootstrapRef,
	}
}

func ToDriverProxyState(state ProxyState) driverpkg.ProxyState {
	return driverpkg.ProxyState{
		ProxyPath:  state.ProxyPath,
		GuestHost:  state.GuestHost,
		HostPort:   state.HostPort,
		GuestPort:  state.GuestPort,
		JupyterURL: state.JupyterURL,
		Token:      state.Token,
	}
}

func ToDriverExecSpec(spec ExecSpec) driverpkg.ExecSpec {
	return driverpkg.ExecSpec{
		Command: spec.Command,
		Args:    append([]string(nil), spec.Args...),
		Env:     spec.Env,
		Cwd:     spec.Cwd,
	}
}

func FromDriverSessionVMInfo(info driverpkg.SessionVMInfo) SessionVMInfo {
	result := SessionVMInfo{BoxID: info.BoxID, JupyterURL: info.JupyterURL}
	if info.ProxyState != nil {
		proxyState := FromDriverProxyState(*info.ProxyState)
		result.ProxyState = &proxyState
	}
	return result
}

func FromDriverProxyState(state driverpkg.ProxyState) ProxyState {
	return ProxyState{
		ProxyPath:  state.ProxyPath,
		GuestHost:  state.GuestHost,
		HostPort:   state.HostPort,
		GuestPort:  state.GuestPort,
		JupyterURL: state.JupyterURL,
		Token:      state.Token,
	}
}

func FromDriverExecResult(result driverpkg.ExecResult) ExecResult {
	return ExecResult{
		ExitCode: result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Output:   result.Output,
		Success:  result.Success,
	}
}
