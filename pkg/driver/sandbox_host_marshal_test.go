package driver

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/driver/sandboxhost"
	"github.com/chaitin/agent-compose/pkg/egress"
)

func TestSandboxHostMarshalRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	zero := time.Time{}
	value := 12.5

	sandbox := &Sandbox{
		Summary: SandboxSummary{
			ID:            "sandbox-1",
			Driver:        RuntimeDriverBoxlite,
			GuestImage:    "example.com/guest:latest",
			PullPolicy:    "always",
			RuntimeRef:    "agent-compose-sandbox-1",
			WorkspacePath: "/data/sandboxes/1/workspace",
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		Workspace: &SandboxWorkspace{Type: "bind", ConfigJSON: `{"source":"/x"}`},
		EnvItems:  []SandboxEnvVar{{Name: "A", Value: "1"}, {Name: "SECRET", Value: "x", Secret: true}},
		VolumeMounts: []SandboxVolumeMount{{
			ID: "mount-1", Type: "bind", Source: "/src", Target: "/dst", ReadOnly: true, HostPath: "/src",
		}},
		RuntimeEnvItems: []SandboxEnvVar{{Name: "AGENT_COMPOSE_FACADE_TOKEN", Value: "tok", Secret: true}},
		NetworkPolicy: &SandboxNetworkPolicy{
			Default: egress.Deny,
			Allow:   []egress.AllowEntry{{Host: "example.com", Port: 443, Protocol: "tcp", AllowedIPs: []string{"10.0.0.0/8"}}},
			EngineEndpoints: []egress.EngineEndpoint{{
				Purpose: egress.PurposeLLMFacade, Endpoint: egress.Endpoint{Host: "facade", Port: 7410, Protocol: "tcp"}, Source: "runtime_base_url",
			}},
			DenyDomains:            []string{"internal.example.com"},
			MediatedByForwardProxy: true,
		},
	}

	vmState := VMState{
		Driver: RuntimeDriverMicrosandbox, Mode: "detached", BoxName: "box-1", BoxID: "box-id-1",
		Image: "img", Registry: "reg", RuntimeHome: "/data/microsandbox", StartedAt: now, StoppedAt: zero,
		LastError: "boom", BootstrapRef: "bootstrap", K8sContext: "ctx", K8sNamespace: "ns",
	}

	execSpec := ExecSpec{
		Command: "sh", Args: []string{"-c", "echo hi"}, Env: map[string]string{"K": "V"},
		Cwd: "/work", Marker: "marker-1", KillOnlyOnCancel: true,
	}

	execResult := ExecResult{
		ExitCode: 3, Stdout: "out", Stderr: "err", Output: "out", Success: false,
		SecurityFacts: ExecSecurityFacts{SeccompUnavailable: 1, NoNewPrivilegesUnavailable: 2},
	}

	stats := SandboxStats{
		SandboxID: "sandbox-1", Driver: RuntimeDriverBoxlite, SampledAt: now,
		CPUPercent:       MetricValue{Value: &value, Unit: "percent", Status: "ok"},
		MemoryUsageBytes: MetricValue{Value: nil, Unit: "bytes"},
		MemoryLimitBytes: MetricValue{},
	}

	spec := RuntimeStartSpec{
		OperationID: "op-1", Kind: RuntimeOperationAgent, Origin: "scheduler",
		Command: &RuntimeCommandSpec{Command: "sh", Args: []string{"-c"}, Env: map[string]string{"K": "V"}, Cwd: "/w"},
		Agent:   &RuntimeAgentSpec{Provider: "claude", Model: "m", Prompt: "p", SessionID: "s", Metadata: map[string]string{"k": "v"}},
		Cwd:     "/w", Env: map[string]string{"E": "1"}, AttachStdin: true, TTY: true, Rows: 24, Cols: 80, TimeoutMs: 5000, ArtifactDir: "/a",
	}

	input := RuntimeInputFrame{Type: RuntimeInputStdin, Data: []byte("hi"), Rows: 24, Cols: 80, Signal: RuntimeSignalInterrupt, Message: "m"}

	output := RuntimeOutputFrame{
		Type:  RuntimeOutputAgentEvent,
		Data:  []byte("d"),
		Event: &RuntimeAgentEvent{Type: "turn", Message: "m", Payload: []byte("p"), Metadata: map[string]string{"k": "v"}},
		Result: &RuntimeResult{OperationID: "op", ExitCode: 0, Success: true, StartedAt: now, CompletedAt: now,
			Artifacts: map[string]string{"a": "b"}, SecurityFacts: &ExecSecurityFacts{SeccompUnavailable: 1}},
		Error:     &RuntimeError{Code: "c", Message: "m"},
		StartedAt: now,
	}

	caps := RuntimeInteractionCapabilities{NativeExec: true, Stdin: true, StdinEOF: true, TTY: true, Resize: true, Signal: true, Artifacts: true, AgentTurns: true}

	t.Run("sandbox", func(t *testing.T) {
		assertMarshalRoundTrip(t, sandbox, func(s *Sandbox) *sandboxhost.Sandbox { return marshalSandbox(s) }, unmarshalSandbox)
	})
	t.Run("vm_state", func(t *testing.T) {
		assertMarshalRoundTrip(t, vmState, marshalVMState, unmarshalVMState)
	})
	t.Run("exec_spec", func(t *testing.T) {
		assertMarshalRoundTrip(t, execSpec, marshalExecSpec, unmarshalExecSpec)
	})
	t.Run("exec_result", func(t *testing.T) {
		assertMarshalRoundTrip(t, execResult, marshalExecResult, unmarshalExecResult)
	})
	t.Run("stats", func(t *testing.T) {
		assertMarshalRoundTrip(t, stats, marshalSandboxStats, unmarshalSandboxStats)
	})
	t.Run("start_spec", func(t *testing.T) {
		assertMarshalRoundTrip(t, spec, marshalRuntimeStartSpec, unmarshalRuntimeStartSpec)
	})
	t.Run("input_frame", func(t *testing.T) {
		assertMarshalRoundTrip(t, input, marshalRuntimeInputFrame, unmarshalRuntimeInputFrame)
	})
	t.Run("output_frame", func(t *testing.T) {
		assertMarshalRoundTrip(t, output, marshalRuntimeOutputFrame, unmarshalRuntimeOutputFrame)
	})
	t.Run("capabilities", func(t *testing.T) {
		assertMarshalRoundTrip(t, caps, marshalRuntimeInteractionCapabilities, unmarshalRuntimeInteractionCapabilities)
	})
}

func TestSandboxHostNilRoundTrip(t *testing.T) {
	var nilSandbox *Sandbox
	if got := unmarshalSandbox(marshalSandbox(nilSandbox)); got != nil {
		t.Fatalf("expected nil sandbox round-trip, got %#v", got)
	}
	if got := unmarshalNetworkPolicy(marshalNetworkPolicy(nil)); got != nil {
		t.Fatalf("expected nil network policy round-trip, got %#v", got)
	}
	if got := unmarshalSandboxWorkspace(marshalSandboxWorkspace(nil)); got != nil {
		t.Fatalf("expected nil workspace round-trip, got %#v", got)
	}
}

func TestSandboxVMInfoRoundTripPreservesNilProxyState(t *testing.T) {
	info := SandboxVMInfo{BoxID: "b", JupyterURL: "http://jupyter", RuntimeCreated: true}
	assertMarshalRoundTrip(t, info, marshalSandboxVMInfo, unmarshalSandboxVMInfo)

	withProxy := SandboxVMInfo{BoxID: "b", ProxyState: &ProxyState{ProxyPath: "/p", Enabled: true}}
	assertMarshalRoundTrip(t, withProxy, marshalSandboxVMInfo, unmarshalSandboxVMInfo)
}

func TestSandboxHostProtocolCompatible(t *testing.T) {
	if !sandboxHostProtocolCompatible(sandboxHostProtocolVersion) {
		t.Fatalf("current protocol version %d must be compatible", sandboxHostProtocolVersion)
	}
	if sandboxHostProtocolVersion > 0 && !sandboxHostProtocolCompatible(sandboxHostProtocolVersion-1) {
		t.Fatalf("previous protocol version %d must be compatible", sandboxHostProtocolVersion-1)
	}
	if sandboxHostProtocolCompatible(sandboxHostProtocolVersion + 1) {
		t.Fatalf("future protocol version must be incompatible")
	}
	if sandboxHostProtocolCompatible(-1) {
		t.Fatalf("negative protocol version must be incompatible")
	}
}

func TestResolveSandboxCreateEnv(t *testing.T) {
	t.Run("no proxy provider returns input unchanged", func(t *testing.T) {
		sandbox := &Sandbox{RuntimeEnvItems: []SandboxEnvVar{{Name: "T", Value: "1"}}}
		got, err := resolveSandboxCreateEnv(context.Background(), sandbox)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != sandbox {
			t.Fatalf("expected the same sandbox pointer, got a copy")
		}
	})

	t.Run("proxy provider folds items and clears provider", func(t *testing.T) {
		sandbox := &Sandbox{
			RuntimeEnvItems: []SandboxEnvVar{{Name: "T", Value: "1"}},
			ProxyEnv: func(context.Context) ([]SandboxEnvVar, error) {
				return []SandboxEnvVar{{Name: "PROXY", Value: "cred", Secret: true}}, nil
			},
		}
		got, err := resolveSandboxCreateEnv(context.Background(), sandbox)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ProxyEnv != nil {
			t.Fatalf("expected ProxyEnv cleared, but it is still set")
		}
		want := []SandboxEnvVar{{Name: "T", Value: "1"}, {Name: "PROXY", Value: "cred", Secret: true}}
		if !reflect.DeepEqual(got.RuntimeEnvItems, want) {
			t.Fatalf("RuntimeEnvItems = %#v, want %#v", got.RuntimeEnvItems, want)
		}
		// The input must not be mutated.
		if len(sandbox.RuntimeEnvItems) != 1 {
			t.Fatalf("input sandbox was mutated: %#v", sandbox.RuntimeEnvItems)
		}
	})

	t.Run("proxy provider error propagates", func(t *testing.T) {
		sandbox := &Sandbox{ProxyEnv: func(context.Context) ([]SandboxEnvVar, error) {
			return nil, errors.New("proxy boom")
		}}
		_, err := resolveSandboxCreateEnv(context.Background(), sandbox)
		if err == nil || err.Error() != "proxy boom" {
			t.Fatalf("expected proxy error, got %v", err)
		}
	})
}

func assertMarshalRoundTrip[T, P any](t *testing.T, in T, marshal func(T) P, unmarshal func(P) T) {
	t.Helper()
	if got := unmarshal(marshal(in)); !reflect.DeepEqual(in, got) {
		t.Fatalf("round-trip mismatch:\n in: %#v\nout: %#v", in, got)
	}
}
