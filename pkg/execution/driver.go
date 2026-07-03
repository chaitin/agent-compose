package execution

import (
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func ToDriverSession(session *domain.Session) *driverpkg.Session {
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

func ToDriverVMState(state domain.VMState) driverpkg.VMState {
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

func FromDriverVMState(state driverpkg.VMState) domain.VMState {
	return domain.VMState{
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

func ToDriverProxyState(state domain.ProxyState) driverpkg.ProxyState {
	return driverpkg.ProxyState{
		ProxyPath:  state.ProxyPath,
		GuestHost:  state.GuestHost,
		HostPort:   state.HostPort,
		GuestPort:  state.GuestPort,
		JupyterURL: state.JupyterURL,
		Token:      state.Token,
	}
}

func ToDriverExecSpec(spec domain.ExecSpec) driverpkg.ExecSpec {
	return driverpkg.ExecSpec{
		Command: spec.Command,
		Args:    append([]string(nil), spec.Args...),
		Env:     spec.Env,
		Cwd:     spec.Cwd,
	}
}

func FromDriverSessionVMInfo(info driverpkg.SessionVMInfo) domain.SessionVMInfo {
	result := domain.SessionVMInfo{BoxID: info.BoxID, JupyterURL: info.JupyterURL}
	if info.ProxyState != nil {
		proxyState := FromDriverProxyState(*info.ProxyState)
		result.ProxyState = &proxyState
	}
	return result
}

func FromDriverProxyState(state driverpkg.ProxyState) domain.ProxyState {
	return domain.ProxyState{
		ProxyPath:  state.ProxyPath,
		GuestHost:  state.GuestHost,
		HostPort:   state.HostPort,
		GuestPort:  state.GuestPort,
		JupyterURL: state.JupyterURL,
		Token:      state.Token,
	}
}

func FromDriverExecResult(result driverpkg.ExecResult) domain.ExecResult {
	return domain.ExecResult{
		ExitCode: result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Output:   result.Output,
		Success:  result.Success,
	}
}
