package execution

import (
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func ToDriverSandbox(session *domain.Sandbox) *driverpkg.Sandbox {
	if session == nil {
		return nil
	}
	envItems := make([]driverpkg.SandboxEnvVar, 0, len(session.EnvItems))
	for _, item := range session.EnvItems {
		envItems = append(envItems, driverpkg.SandboxEnvVar{Name: item.Name, Value: item.Value, Secret: item.Secret})
	}
	runtimeEnvItems := make([]driverpkg.SandboxEnvVar, 0, len(session.RuntimeEnvItems))
	for _, item := range session.RuntimeEnvItems {
		runtimeEnvItems = append(runtimeEnvItems, driverpkg.SandboxEnvVar{Name: item.Name, Value: item.Value, Secret: item.Secret})
	}
	volumeMounts := make([]driverpkg.SandboxVolumeMount, 0, len(session.VolumeMounts))
	for _, item := range session.VolumeMounts {
		volumeMounts = append(volumeMounts, driverpkg.SandboxVolumeMount{
			ID:       item.ID,
			Type:     item.Type,
			Source:   item.Source,
			Target:   item.Target,
			ReadOnly: item.ReadOnly,
			VolumeID: item.VolumeID,
			Driver:   item.Driver,
			HostPath: item.HostPath,
		})
	}
	var networkIntent *driverpkg.SandboxNetworkIntent
	if session.NetworkIntent != nil {
		definitions := make([]driverpkg.SandboxNetworkDefinition, 0, len(session.NetworkIntent.Definitions))
		for _, definition := range session.NetworkIntent.Definitions {
			definitions = append(definitions, driverpkg.SandboxNetworkDefinition{Name: definition.Name, Driver: definition.Driver})
		}
		networkIntent = &driverpkg.SandboxNetworkIntent{
			Version:         session.NetworkIntent.Version,
			ProjectID:       session.NetworkIntent.ProjectID,
			ProjectRevision: session.NetworkIntent.ProjectRevision,
			AgentName:       session.NetworkIntent.AgentName,
			Definitions:     definitions,
			Attachments:     append([]string(nil), session.NetworkIntent.Attachments...),
			Expose:          append([]string(nil), session.NetworkIntent.Expose...),
			Ports:           append([]string(nil), session.NetworkIntent.Ports...),
		}
	}
	return &driverpkg.Sandbox{
		Summary: driverpkg.SandboxSummary{
			ID:            session.Summary.ID,
			Driver:        session.Summary.Driver,
			GuestImage:    session.Summary.GuestImage,
			PullPolicy:    session.Summary.PullPolicy,
			RuntimeRef:    session.Summary.RuntimeRef,
			WorkspacePath: session.Summary.WorkspacePath,
			CreatedAt:     session.Summary.CreatedAt,
			UpdatedAt:     session.Summary.UpdatedAt,
		},
		EnvItems:        envItems,
		VolumeMounts:    volumeMounts,
		RuntimeEnvItems: runtimeEnvItems,
		NetworkIntent:   networkIntent,
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
		NetworkState: toDriverNetworkState(state.NetworkState),
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
		NetworkState: fromDriverNetworkState(state.NetworkState),
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
		Enabled:    state.Enabled,
		Exposed:    state.Exposed,
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

func FromDriverSandboxVMInfo(info driverpkg.SandboxVMInfo) domain.SandboxVMInfo {
	result := domain.SandboxVMInfo{BoxID: info.BoxID, JupyterURL: info.JupyterURL, NetworkState: fromDriverNetworkState(info.NetworkState)}
	if info.ProxyState != nil {
		proxyState := FromDriverProxyState(*info.ProxyState)
		result.ProxyState = &proxyState
	}
	return result
}

func toDriverNetworkState(state *domain.SandboxNetworkState) *driverpkg.SandboxNetworkState {
	if state == nil {
		return nil
	}
	result := &driverpkg.SandboxNetworkState{Mode: state.Mode, ReconciledAt: state.ReconciledAt}
	for _, item := range state.Attachments {
		result.Attachments = append(result.Attachments, driverpkg.SandboxNetworkAttachmentState{
			LogicalName: item.LogicalName, RuntimeName: item.RuntimeName, NetworkID: item.NetworkID,
			Aliases: append([]string(nil), item.Aliases...), IPv4Address: item.IPv4Address, Primary: item.Primary,
		})
	}
	for _, item := range state.PortBindings {
		result.PortBindings = append(result.PortBindings, driverpkg.SandboxPortBindingState{ContainerPort: item.ContainerPort, HostIP: item.HostIP, HostPort: item.HostPort})
	}
	return result
}

func fromDriverNetworkState(state *driverpkg.SandboxNetworkState) *domain.SandboxNetworkState {
	if state == nil {
		return nil
	}
	result := &domain.SandboxNetworkState{Mode: state.Mode, ReconciledAt: state.ReconciledAt}
	for _, item := range state.Attachments {
		result.Attachments = append(result.Attachments, domain.SandboxNetworkAttachmentState{
			LogicalName: item.LogicalName, RuntimeName: item.RuntimeName, NetworkID: item.NetworkID,
			Aliases: append([]string(nil), item.Aliases...), IPv4Address: item.IPv4Address, Primary: item.Primary,
		})
	}
	for _, item := range state.PortBindings {
		result.PortBindings = append(result.PortBindings, domain.SandboxPortBindingState{ContainerPort: item.ContainerPort, HostIP: item.HostIP, HostPort: item.HostPort})
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
		Enabled:    state.Enabled,
		Exposed:    state.Exposed,
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

func FromDriverSandboxStats(stats driverpkg.SandboxStats) domain.SandboxStats {
	return domain.SandboxStats{
		SandboxID:        stats.SandboxID,
		Driver:           stats.Driver,
		SampledAt:        stats.SampledAt,
		CPUPercent:       FromDriverMetricValue(stats.CPUPercent),
		MemoryUsageBytes: FromDriverMetricValue(stats.MemoryUsageBytes),
		MemoryLimitBytes: FromDriverMetricValue(stats.MemoryLimitBytes),
		MemoryPercent:    FromDriverMetricValue(stats.MemoryPercent),
		NetworkRxBytes:   FromDriverMetricValue(stats.NetworkRxBytes),
		NetworkTxBytes:   FromDriverMetricValue(stats.NetworkTxBytes),
		BlockReadBytes:   FromDriverMetricValue(stats.BlockReadBytes),
		BlockWriteBytes:  FromDriverMetricValue(stats.BlockWriteBytes),
		UptimeSeconds:    FromDriverMetricValue(stats.UptimeSeconds),
	}
}

func FromDriverMetricValue(metric driverpkg.MetricValue) domain.MetricValue {
	return domain.MetricValue{
		Value:   metric.Value,
		Unit:    metric.Unit,
		Status:  metric.Status,
		Message: metric.Message,
	}
}
