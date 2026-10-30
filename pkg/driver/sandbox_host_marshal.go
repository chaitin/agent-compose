package driver

import (
	"time"

	"github.com/chaitin/agent-compose/pkg/driver/sandboxhost"
	"github.com/chaitin/agent-compose/pkg/egress"
)

// This file maps the driver-boundary types to and from the sandbox-host wire
// messages. ProxyEnv (a closure) never crosses the wire; the daemon resolves it
// and folds the result into RuntimeEnvItems before Create.

func timeToNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func nanosToTime(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

func marshalSandboxEnvVar(v SandboxEnvVar) *sandboxhost.SandboxEnvVar {
	return &sandboxhost.SandboxEnvVar{Name: v.Name, Value: v.Value, Secret: v.Secret}
}

func unmarshalSandboxEnvVar(v *sandboxhost.SandboxEnvVar) SandboxEnvVar {
	if v == nil {
		return SandboxEnvVar{}
	}
	return SandboxEnvVar{Name: v.GetName(), Value: v.GetValue(), Secret: v.GetSecret()}
}

func marshalSandboxSummary(s SandboxSummary) *sandboxhost.SandboxSummary {
	return &sandboxhost.SandboxSummary{
		Id:            s.ID,
		Driver:        s.Driver,
		GuestImage:    s.GuestImage,
		PullPolicy:    s.PullPolicy,
		RuntimeRef:    s.RuntimeRef,
		WorkspacePath: s.WorkspacePath,
		CreatedAt:     timeToNanos(s.CreatedAt),
		UpdatedAt:     timeToNanos(s.UpdatedAt),
	}
}

func unmarshalSandboxSummary(s *sandboxhost.SandboxSummary) SandboxSummary {
	if s == nil {
		return SandboxSummary{}
	}
	return SandboxSummary{
		ID:            s.GetId(),
		Driver:        s.GetDriver(),
		GuestImage:    s.GetGuestImage(),
		PullPolicy:    s.GetPullPolicy(),
		RuntimeRef:    s.GetRuntimeRef(),
		WorkspacePath: s.GetWorkspacePath(),
		CreatedAt:     nanosToTime(s.GetCreatedAt()),
		UpdatedAt:     nanosToTime(s.GetUpdatedAt()),
	}
}

func marshalSandboxWorkspace(w *SandboxWorkspace) *sandboxhost.SandboxWorkspace {
	if w == nil {
		return nil
	}
	return &sandboxhost.SandboxWorkspace{Type: w.Type, ConfigJson: w.ConfigJSON}
}

func unmarshalSandboxWorkspace(w *sandboxhost.SandboxWorkspace) *SandboxWorkspace {
	if w == nil {
		return nil
	}
	return &SandboxWorkspace{Type: w.GetType(), ConfigJSON: w.GetConfigJson()}
}

func marshalSandboxVolumeMount(m SandboxVolumeMount) *sandboxhost.SandboxVolumeMount {
	return &sandboxhost.SandboxVolumeMount{
		Id:       m.ID,
		Type:     m.Type,
		Source:   m.Source,
		Target:   m.Target,
		ReadOnly: m.ReadOnly,
		VolumeId: m.VolumeID,
		Driver:   m.Driver,
		HostPath: m.HostPath,
	}
}

func unmarshalSandboxVolumeMount(m *sandboxhost.SandboxVolumeMount) SandboxVolumeMount {
	if m == nil {
		return SandboxVolumeMount{}
	}
	return SandboxVolumeMount{
		ID:       m.GetId(),
		Type:     m.GetType(),
		Source:   m.GetSource(),
		Target:   m.GetTarget(),
		ReadOnly: m.GetReadOnly(),
		VolumeID: m.GetVolumeId(),
		Driver:   m.GetDriver(),
		HostPath: m.GetHostPath(),
	}
}

func marshalNetworkPolicy(p *SandboxNetworkPolicy) *sandboxhost.NetworkPolicy {
	if p == nil {
		return nil
	}
	policy := &sandboxhost.NetworkPolicy{
		DefaultAction:          string(p.Default),
		DenyDomains:            append([]string(nil), p.DenyDomains...),
		MediatedByForwardProxy: p.MediatedByForwardProxy,
	}
	for _, entry := range p.Allow {
		policy.Allow = append(policy.Allow, &sandboxhost.AllowEntry{
			Host:       entry.Host,
			Port:       int32(entry.Port),
			Protocol:   string(entry.Protocol),
			AllowedIps: append([]string(nil), entry.AllowedIPs...),
		})
	}
	for _, entry := range p.Deny {
		policy.Deny = append(policy.Deny, &sandboxhost.DenyEntry{
			Host:     entry.Host,
			Port:     int32(entry.Port),
			Protocol: string(entry.Protocol),
		})
	}
	for _, endpoint := range p.EngineEndpoints {
		policy.EngineEndpoints = append(policy.EngineEndpoints, &sandboxhost.EngineEndpoint{
			Purpose: string(endpoint.Purpose),
			Endpoint: &sandboxhost.Endpoint{
				Host:     endpoint.Endpoint.Host,
				Port:     int32(endpoint.Endpoint.Port),
				Protocol: string(endpoint.Endpoint.Protocol),
			},
			Source: endpoint.Source,
		})
	}
	return policy
}

func unmarshalNetworkPolicy(p *sandboxhost.NetworkPolicy) *SandboxNetworkPolicy {
	if p == nil {
		return nil
	}
	policy := &SandboxNetworkPolicy{
		Default:                egress.Action(p.GetDefaultAction()),
		DenyDomains:            append([]string(nil), p.GetDenyDomains()...),
		MediatedByForwardProxy: p.GetMediatedByForwardProxy(),
	}
	for _, entry := range p.GetAllow() {
		policy.Allow = append(policy.Allow, egress.AllowEntry{
			Host:       entry.GetHost(),
			Port:       int(entry.GetPort()),
			Protocol:   egress.Protocol(entry.GetProtocol()),
			AllowedIPs: append([]string(nil), entry.GetAllowedIps()...),
		})
	}
	for _, entry := range p.GetDeny() {
		policy.Deny = append(policy.Deny, egress.DenyEntry{
			Host:     entry.GetHost(),
			Port:     int(entry.GetPort()),
			Protocol: egress.Protocol(entry.GetProtocol()),
		})
	}
	for _, endpoint := range p.GetEngineEndpoints() {
		policy.EngineEndpoints = append(policy.EngineEndpoints, egress.EngineEndpoint{
			Purpose: egress.EndpointPurpose(endpoint.GetPurpose()),
			Endpoint: egress.Endpoint{
				Host:     endpoint.GetEndpoint().GetHost(),
				Port:     int(endpoint.GetEndpoint().GetPort()),
				Protocol: egress.Protocol(endpoint.GetEndpoint().GetProtocol()),
			},
			Source: endpoint.GetSource(),
		})
	}
	return policy
}

func marshalSandbox(s *Sandbox) *sandboxhost.Sandbox {
	if s == nil {
		return nil
	}
	out := &sandboxhost.Sandbox{
		Summary:       marshalSandboxSummary(s.Summary),
		Workspace:     marshalSandboxWorkspace(s.Workspace),
		NetworkPolicy: marshalNetworkPolicy(s.NetworkPolicy),
	}
	for _, item := range s.EnvItems {
		out.EnvItems = append(out.EnvItems, marshalSandboxEnvVar(item))
	}
	for _, item := range s.RuntimeEnvItems {
		out.RuntimeEnvItems = append(out.RuntimeEnvItems, marshalSandboxEnvVar(item))
	}
	for _, mount := range s.VolumeMounts {
		out.VolumeMounts = append(out.VolumeMounts, marshalSandboxVolumeMount(mount))
	}
	return out
}

func unmarshalSandbox(s *sandboxhost.Sandbox) *Sandbox {
	if s == nil {
		return nil
	}
	out := &Sandbox{
		Summary:       unmarshalSandboxSummary(s.GetSummary()),
		Workspace:     unmarshalSandboxWorkspace(s.GetWorkspace()),
		NetworkPolicy: unmarshalNetworkPolicy(s.GetNetworkPolicy()),
	}
	for _, item := range s.GetEnvItems() {
		out.EnvItems = append(out.EnvItems, unmarshalSandboxEnvVar(item))
	}
	for _, item := range s.GetRuntimeEnvItems() {
		out.RuntimeEnvItems = append(out.RuntimeEnvItems, unmarshalSandboxEnvVar(item))
	}
	for _, mount := range s.GetVolumeMounts() {
		out.VolumeMounts = append(out.VolumeMounts, unmarshalSandboxVolumeMount(mount))
	}
	return out
}

func marshalVMState(s VMState) *sandboxhost.VMState {
	return &sandboxhost.VMState{
		Driver:           s.Driver,
		Mode:             s.Mode,
		BoxName:          s.BoxName,
		BoxId:            s.BoxID,
		Image:            s.Image,
		Registry:         s.Registry,
		RuntimeHome:      s.RuntimeHome,
		StartedAt:        timeToNanos(s.StartedAt),
		StartAttemptedAt: timeToNanos(s.StartAttemptedAt),
		StoppedAt:        timeToNanos(s.StoppedAt),
		LastError:        s.LastError,
		BootstrapRef:     s.BootstrapRef,
		K8SContext:       s.K8sContext,
		K8SNamespace:     s.K8sNamespace,
		SandboxInitState: s.SandboxInitState,
	}
}

func unmarshalVMState(s *sandboxhost.VMState) VMState {
	if s == nil {
		return VMState{}
	}
	return VMState{
		Driver:           s.GetDriver(),
		Mode:             s.GetMode(),
		BoxName:          s.GetBoxName(),
		BoxID:            s.GetBoxId(),
		Image:            s.GetImage(),
		Registry:         s.GetRegistry(),
		RuntimeHome:      s.GetRuntimeHome(),
		StartedAt:        nanosToTime(s.GetStartedAt()),
		StartAttemptedAt: nanosToTime(s.GetStartAttemptedAt()),
		StoppedAt:        nanosToTime(s.GetStoppedAt()),
		LastError:        s.GetLastError(),
		BootstrapRef:     s.GetBootstrapRef(),
		K8sContext:       s.GetK8SContext(),
		K8sNamespace:     s.GetK8SNamespace(),
		SandboxInitState: s.GetSandboxInitState(),
	}
}

func marshalProxyState(s ProxyState) *sandboxhost.ProxyState {
	return &sandboxhost.ProxyState{
		ProxyPath:  s.ProxyPath,
		GuestHost:  s.GuestHost,
		HostPort:   int32(s.HostPort),
		GuestPort:  int32(s.GuestPort),
		JupyterUrl: s.JupyterURL,
		Token:      s.Token,
		Enabled:    s.Enabled,
		Exposed:    s.Exposed,
	}
}

func unmarshalProxyState(s *sandboxhost.ProxyState) ProxyState {
	if s == nil {
		return ProxyState{}
	}
	return ProxyState{
		ProxyPath:  s.GetProxyPath(),
		GuestHost:  s.GetGuestHost(),
		HostPort:   int(s.GetHostPort()),
		GuestPort:  int(s.GetGuestPort()),
		JupyterURL: s.GetJupyterUrl(),
		Token:      s.GetToken(),
		Enabled:    s.GetEnabled(),
		Exposed:    s.GetExposed(),
	}
}

func marshalExecSpec(s ExecSpec) *sandboxhost.ExecSpec {
	return &sandboxhost.ExecSpec{
		Command:          s.Command,
		Args:             append([]string(nil), s.Args...),
		Env:              s.Env,
		Cwd:              s.Cwd,
		Marker:           s.Marker,
		KillOnlyOnCancel: s.KillOnlyOnCancel,
	}
}

func unmarshalExecSpec(s *sandboxhost.ExecSpec) ExecSpec {
	if s == nil {
		return ExecSpec{}
	}
	return ExecSpec{
		Command:          s.GetCommand(),
		Args:             append([]string(nil), s.GetArgs()...),
		Env:              s.GetEnv(),
		Cwd:              s.GetCwd(),
		Marker:           s.GetMarker(),
		KillOnlyOnCancel: s.GetKillOnlyOnCancel(),
	}
}

func marshalExecChunk(c ExecChunk) *sandboxhost.ExecChunk {
	return &sandboxhost.ExecChunk{Text: c.Text, Stream: string(NormalizeStdioStream(c.Stream))}
}

func unmarshalExecChunk(c *sandboxhost.ExecChunk) ExecChunk {
	if c == nil {
		return ExecChunk{}
	}
	return ExecChunk{Text: c.GetText(), Stream: NormalizeStdioStream(StdioStream(c.GetStream()))}
}

func marshalExecSecurityFacts(f ExecSecurityFacts) *sandboxhost.ExecSecurityFacts {
	return &sandboxhost.ExecSecurityFacts{
		SeccompUnavailable:         int32(f.SeccompUnavailable),
		NoNewPrivilegesUnavailable: int32(f.NoNewPrivilegesUnavailable),
	}
}

func unmarshalExecSecurityFacts(f *sandboxhost.ExecSecurityFacts) ExecSecurityFacts {
	if f == nil {
		return ExecSecurityFacts{}
	}
	return ExecSecurityFacts{
		SeccompUnavailable:         int(f.GetSeccompUnavailable()),
		NoNewPrivilegesUnavailable: int(f.GetNoNewPrivilegesUnavailable()),
	}
}

func marshalExecResult(r ExecResult) *sandboxhost.ExecResult {
	return &sandboxhost.ExecResult{
		ExitCode:      int32(r.ExitCode),
		Stdout:        r.Stdout,
		Stderr:        r.Stderr,
		Output:        r.Output,
		Success:       r.Success,
		SecurityFacts: marshalExecSecurityFacts(r.SecurityFacts),
	}
}

func unmarshalExecResult(r *sandboxhost.ExecResult) ExecResult {
	if r == nil {
		return ExecResult{}
	}
	return ExecResult{
		ExitCode:      int(r.GetExitCode()),
		Stdout:        r.GetStdout(),
		Stderr:        r.GetStderr(),
		Output:        r.GetOutput(),
		Success:       r.GetSuccess(),
		SecurityFacts: unmarshalExecSecurityFacts(r.GetSecurityFacts()),
	}
}

func marshalSandboxVMInfo(i SandboxVMInfo) *sandboxhost.SandboxVMInfo {
	out := &sandboxhost.SandboxVMInfo{
		BoxId:            i.BoxID,
		JupyterUrl:       i.JupyterURL,
		RuntimeCreated:   i.RuntimeCreated,
		SandboxInitState: i.SandboxInitState,
	}
	if i.ProxyState != nil {
		state := marshalProxyState(*i.ProxyState)
		out.ProxyState = state
	}
	return out
}

func unmarshalSandboxVMInfo(i *sandboxhost.SandboxVMInfo) SandboxVMInfo {
	if i == nil {
		return SandboxVMInfo{}
	}
	out := SandboxVMInfo{
		BoxID:            i.GetBoxId(),
		JupyterURL:       i.GetJupyterUrl(),
		RuntimeCreated:   i.GetRuntimeCreated(),
		SandboxInitState: i.GetSandboxInitState(),
	}
	if i.GetProxyState() != nil {
		state := unmarshalProxyState(i.GetProxyState())
		out.ProxyState = &state
	}
	return out
}

func marshalMetricValue(m MetricValue) *sandboxhost.MetricValue {
	out := &sandboxhost.MetricValue{Unit: m.Unit, Status: m.Status, Message: m.Message}
	if m.Value != nil {
		out.Value = m.Value
	}
	return out
}

func unmarshalMetricValue(m *sandboxhost.MetricValue) MetricValue {
	if m == nil {
		return MetricValue{}
	}
	out := MetricValue{Unit: m.GetUnit(), Status: m.GetStatus(), Message: m.GetMessage()}
	if m.Value != nil {
		value := m.GetValue()
		out.Value = &value
	}
	return out
}

func marshalSandboxStats(s SandboxStats) *sandboxhost.SandboxStats {
	return &sandboxhost.SandboxStats{
		SandboxId:        s.SandboxID,
		Driver:           s.Driver,
		SampledAt:        timeToNanos(s.SampledAt),
		CpuPercent:       marshalMetricValue(s.CPUPercent),
		MemoryUsageBytes: marshalMetricValue(s.MemoryUsageBytes),
		MemoryLimitBytes: marshalMetricValue(s.MemoryLimitBytes),
		MemoryPercent:    marshalMetricValue(s.MemoryPercent),
		NetworkRxBytes:   marshalMetricValue(s.NetworkRxBytes),
		NetworkTxBytes:   marshalMetricValue(s.NetworkTxBytes),
		BlockReadBytes:   marshalMetricValue(s.BlockReadBytes),
		BlockWriteBytes:  marshalMetricValue(s.BlockWriteBytes),
		UptimeSeconds:    marshalMetricValue(s.UptimeSeconds),
	}
}

func unmarshalSandboxStats(s *sandboxhost.SandboxStats) SandboxStats {
	if s == nil {
		return SandboxStats{}
	}
	return SandboxStats{
		SandboxID:        s.GetSandboxId(),
		Driver:           s.GetDriver(),
		SampledAt:        nanosToTime(s.GetSampledAt()),
		CPUPercent:       unmarshalMetricValue(s.GetCpuPercent()),
		MemoryUsageBytes: unmarshalMetricValue(s.GetMemoryUsageBytes()),
		MemoryLimitBytes: unmarshalMetricValue(s.GetMemoryLimitBytes()),
		MemoryPercent:    unmarshalMetricValue(s.GetMemoryPercent()),
		NetworkRxBytes:   unmarshalMetricValue(s.GetNetworkRxBytes()),
		NetworkTxBytes:   unmarshalMetricValue(s.GetNetworkTxBytes()),
		BlockReadBytes:   unmarshalMetricValue(s.GetBlockReadBytes()),
		BlockWriteBytes:  unmarshalMetricValue(s.GetBlockWriteBytes()),
		UptimeSeconds:    unmarshalMetricValue(s.GetUptimeSeconds()),
	}
}

func marshalRuntimeCommandSpec(c *RuntimeCommandSpec) *sandboxhost.RuntimeCommandSpec {
	if c == nil {
		return nil
	}
	return &sandboxhost.RuntimeCommandSpec{
		Command: c.Command,
		Args:    append([]string(nil), c.Args...),
		Env:     c.Env,
		Cwd:     c.Cwd,
	}
}

func unmarshalRuntimeCommandSpec(c *sandboxhost.RuntimeCommandSpec) *RuntimeCommandSpec {
	if c == nil {
		return nil
	}
	return &RuntimeCommandSpec{
		Command: c.GetCommand(),
		Args:    append([]string(nil), c.GetArgs()...),
		Env:     c.GetEnv(),
		Cwd:     c.GetCwd(),
	}
}

func marshalRuntimeAgentSpec(a *RuntimeAgentSpec) *sandboxhost.RuntimeAgentSpec {
	if a == nil {
		return nil
	}
	return &sandboxhost.RuntimeAgentSpec{
		Provider:  a.Provider,
		Model:     a.Model,
		Prompt:    a.Prompt,
		SessionId: a.SessionID,
		Metadata:  a.Metadata,
	}
}

func unmarshalRuntimeAgentSpec(a *sandboxhost.RuntimeAgentSpec) *RuntimeAgentSpec {
	if a == nil {
		return nil
	}
	return &RuntimeAgentSpec{
		Provider:  a.GetProvider(),
		Model:     a.GetModel(),
		Prompt:    a.GetPrompt(),
		SessionID: a.GetSessionId(),
		Metadata:  a.GetMetadata(),
	}
}

func marshalRuntimeStartSpec(s RuntimeStartSpec) *sandboxhost.RuntimeStartSpec {
	return &sandboxhost.RuntimeStartSpec{
		OperationId: s.OperationID,
		Kind:        string(s.Kind),
		Origin:      s.Origin,
		Command:     marshalRuntimeCommandSpec(s.Command),
		Agent:       marshalRuntimeAgentSpec(s.Agent),
		Cwd:         s.Cwd,
		Env:         s.Env,
		AttachStdin: s.AttachStdin,
		Tty:         s.TTY,
		Rows:        s.Rows,
		Cols:        s.Cols,
		TimeoutMs:   s.TimeoutMs,
		ArtifactDir: s.ArtifactDir,
	}
}

func unmarshalRuntimeStartSpec(s *sandboxhost.RuntimeStartSpec) RuntimeStartSpec {
	if s == nil {
		return RuntimeStartSpec{}
	}
	return RuntimeStartSpec{
		OperationID: s.GetOperationId(),
		Kind:        RuntimeOperationKind(s.GetKind()),
		Origin:      s.GetOrigin(),
		Command:     unmarshalRuntimeCommandSpec(s.GetCommand()),
		Agent:       unmarshalRuntimeAgentSpec(s.GetAgent()),
		Cwd:         s.GetCwd(),
		Env:         s.GetEnv(),
		AttachStdin: s.GetAttachStdin(),
		TTY:         s.GetTty(),
		Rows:        s.GetRows(),
		Cols:        s.GetCols(),
		TimeoutMs:   s.GetTimeoutMs(),
		ArtifactDir: s.GetArtifactDir(),
	}
}

func marshalRuntimeInputFrame(f RuntimeInputFrame) *sandboxhost.RuntimeInputFrame {
	return &sandboxhost.RuntimeInputFrame{
		Type:    string(f.Type),
		Data:    f.Data,
		Rows:    f.Rows,
		Cols:    f.Cols,
		Signal:  string(f.Signal),
		Message: f.Message,
	}
}

func unmarshalRuntimeInputFrame(f *sandboxhost.RuntimeInputFrame) RuntimeInputFrame {
	if f == nil {
		return RuntimeInputFrame{}
	}
	return RuntimeInputFrame{
		Type:    RuntimeInputFrameType(f.GetType()),
		Data:    f.GetData(),
		Rows:    f.GetRows(),
		Cols:    f.GetCols(),
		Signal:  RuntimeSignal(f.GetSignal()),
		Message: f.GetMessage(),
	}
}

func marshalRuntimeAgentEvent(e *RuntimeAgentEvent) *sandboxhost.RuntimeAgentEvent {
	if e == nil {
		return nil
	}
	return &sandboxhost.RuntimeAgentEvent{
		Type:     e.Type,
		Message:  e.Message,
		Payload:  e.Payload,
		Metadata: e.Metadata,
	}
}

func unmarshalRuntimeAgentEvent(e *sandboxhost.RuntimeAgentEvent) *RuntimeAgentEvent {
	if e == nil {
		return nil
	}
	return &RuntimeAgentEvent{
		Type:     e.GetType(),
		Message:  e.GetMessage(),
		Payload:  e.GetPayload(),
		Metadata: e.GetMetadata(),
	}
}

func marshalRuntimeError(e *RuntimeError) *sandboxhost.RuntimeError {
	if e == nil {
		return nil
	}
	return &sandboxhost.RuntimeError{Code: e.Code, Message: e.Message}
}

func unmarshalRuntimeError(e *sandboxhost.RuntimeError) *RuntimeError {
	if e == nil {
		return nil
	}
	return &RuntimeError{Code: e.GetCode(), Message: e.GetMessage()}
}

func marshalRuntimeResult(r *RuntimeResult) *sandboxhost.RuntimeResult {
	if r == nil {
		return nil
	}
	return &sandboxhost.RuntimeResult{
		OperationId:   r.OperationID,
		ExitCode:      int32(r.ExitCode),
		Success:       r.Success,
		Error:         r.Error,
		StartedAt:     timeToNanos(r.StartedAt),
		CompletedAt:   timeToNanos(r.CompletedAt),
		Artifacts:     r.Artifacts,
		SecurityFacts: marshalRuntimeSecurityFacts(r.SecurityFacts),
	}
}

func marshalRuntimeSecurityFacts(f *ExecSecurityFacts) *sandboxhost.ExecSecurityFacts {
	if f == nil {
		return nil
	}
	return marshalExecSecurityFacts(*f)
}

func unmarshalRuntimeResult(r *sandboxhost.RuntimeResult) *RuntimeResult {
	if r == nil {
		return nil
	}
	return &RuntimeResult{
		OperationID:   r.GetOperationId(),
		ExitCode:      int(r.GetExitCode()),
		Success:       r.GetSuccess(),
		Error:         r.GetError(),
		StartedAt:     nanosToTime(r.GetStartedAt()),
		CompletedAt:   nanosToTime(r.GetCompletedAt()),
		Artifacts:     r.GetArtifacts(),
		SecurityFacts: unmarshalRuntimeSecurityFacts(r.GetSecurityFacts()),
	}
}

func unmarshalRuntimeSecurityFacts(f *sandboxhost.ExecSecurityFacts) *ExecSecurityFacts {
	if f == nil {
		return nil
	}
	facts := unmarshalExecSecurityFacts(f)
	return &facts
}

func marshalRuntimeOutputFrame(f RuntimeOutputFrame) *sandboxhost.RuntimeOutputFrame {
	return &sandboxhost.RuntimeOutputFrame{
		Type:      string(f.Type),
		Data:      f.Data,
		Event:     marshalRuntimeAgentEvent(f.Event),
		Result:    marshalRuntimeResult(f.Result),
		Error:     marshalRuntimeError(f.Error),
		StartedAt: timeToNanos(f.StartedAt),
	}
}

func unmarshalRuntimeOutputFrame(f *sandboxhost.RuntimeOutputFrame) RuntimeOutputFrame {
	if f == nil {
		return RuntimeOutputFrame{}
	}
	return RuntimeOutputFrame{
		Type:      RuntimeOutputFrameType(f.GetType()),
		Data:      f.GetData(),
		Event:     unmarshalRuntimeAgentEvent(f.GetEvent()),
		Result:    unmarshalRuntimeResult(f.GetResult()),
		Error:     unmarshalRuntimeError(f.GetError()),
		StartedAt: nanosToTime(f.GetStartedAt()),
	}
}

func marshalRuntimeInteractionCapabilities(c RuntimeInteractionCapabilities) *sandboxhost.RuntimeInteractionCapabilities {
	return &sandboxhost.RuntimeInteractionCapabilities{
		NativeExec:    c.NativeExec,
		WrapperStream: c.WrapperStream,
		Stdin:         c.Stdin,
		StdinEof:      c.StdinEOF,
		Tty:           c.TTY,
		Resize:        c.Resize,
		Signal:        c.Signal,
		Artifacts:     c.Artifacts,
		AgentTurns:    c.AgentTurns,
	}
}

func unmarshalRuntimeInteractionCapabilities(c *sandboxhost.RuntimeInteractionCapabilities) RuntimeInteractionCapabilities {
	if c == nil {
		return RuntimeInteractionCapabilities{}
	}
	return RuntimeInteractionCapabilities{
		NativeExec:    c.GetNativeExec(),
		WrapperStream: c.GetWrapperStream(),
		Stdin:         c.GetStdin(),
		StdinEOF:      c.GetStdinEof(),
		TTY:           c.GetTty(),
		Resize:        c.GetResize(),
		Signal:        c.GetSignal(),
		Artifacts:     c.GetArtifacts(),
		AgentTurns:    c.GetAgentTurns(),
	}
}
