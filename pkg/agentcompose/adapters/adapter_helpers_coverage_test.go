package adapters

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

func TestAdapterHelperCoverage(t *testing.T) {
	t.Run("loader host unavailable dependencies", func(t *testing.T) {
		if err := (LoaderHostEvents{}).Add(context.Background(), "", "", "", "", "", "", nil, "", "", ""); err == nil {
			t.Fatalf("LoaderHostEvents.Add returned nil error")
		}
		if _, err := (LoaderHostEvents{}).AddRecord(context.Background(), "", "", "", "", "", "", nil, "", "", ""); err == nil {
			t.Fatalf("LoaderHostEvents.AddRecord returned nil error")
		}
		if _, err := (LoaderHostAgentExecutor{}).ExecuteAgent(context.Background(), nil, loaders.HostAgentExecutionRequest{}); err == nil {
			t.Fatalf("LoaderHostAgentExecutor.ExecuteAgent returned nil error")
		}
		if _, err := (LoaderHostCommandExecutor{}).ExecuteLoaderCommand(context.Background(), nil, domain.LoaderCommandRequest{}); err == nil {
			t.Fatalf("LoaderHostCommandExecutor.ExecuteLoaderCommand returned nil error")
		}
		if _, err := (LoaderHostLLMRunner{}).Generate(context.Background(), "prompt", "model", ""); err == nil {
			t.Fatalf("LoaderHostLLMRunner.Generate returned nil error")
		}
	})

	t.Run("loader sandbox rpc linked sandbox id", func(t *testing.T) {
		if got := LoaderSandboxRPCLinkedSandboxID("CreateSession", `{"sessionId":"request-sandbox"}`, `{"session":{"summary":{"sessionId":"response-sandbox"}}}`); got != "response-sandbox" {
			t.Fatalf("response sandbox id = %q", got)
		}
		if got := LoaderSandboxRPCLinkedSandboxID("StopSession", `{"sessionId":" request-sandbox "}`, `{bad`); got != "request-sandbox" {
			t.Fatalf("request sandbox id = %q", got)
		}
		if got := LoaderSandboxRPCLinkedSandboxID("ListSessions", `{"sessionId":"ignored"}`, `{}`); got != "" {
			t.Fatalf("ListSessions linked id = %q, want empty", got)
		}
		if got := loaderSandboxIDFromJSON(`{"session":{"summary":{"sessionId":" nested "}}}`); got != "nested" {
			t.Fatalf("nested sandbox id = %q", got)
		}
	})

	t.Run("llm client guards and defaults", func(t *testing.T) {
		client := NewLLMClient(&appconfig.Config{LLMTimeout: 3 * time.Second}, nil)
		if client.client.Timeout != 3*time.Second {
			t.Fatalf("client timeout = %s", client.client.Timeout)
		}
		var nilClient *LLMClient
		if _, err := nilClient.Generate(context.Background(), "prompt", "model", ""); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("nil client Generate error = %v", err)
		}
		if got := firstNonEmpty("", "model", "fallback"); got != "model" {
			t.Fatalf("firstNonEmpty = %q", got)
		}
	})

	t.Run("runtime provider driver resolution", func(t *testing.T) {
		runtime := &fakeAgentRuntime{}
		provider := &runtimeProvider{
			config: &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverDocker},
			runtimes: map[string]SandboxRuntime{
				driverpkg.RuntimeDriverDocker:  runtime,
				driverpkg.RuntimeDriverBoxlite: runtime,
			},
		}
		if got, err := provider.ForDriver("docker-engine"); err != nil || got != runtime {
			t.Fatalf("ForDriver docker-engine = %T/%v", got, err)
		}
		if got, err := provider.ForSession(&domain.Sandbox{Summary: domain.SandboxSummary{Driver: ""}}); err != nil || got != runtime {
			t.Fatalf("ForSession default driver = %T/%v", got, err)
		}
		if _, err := provider.ForDriver("bad-driver"); err == nil {
			t.Fatalf("ForDriver bad-driver returned nil error")
		}
		_, err := provider.ForDriver(driverpkg.RuntimeDriverMicrosandbox)
		if driverpkg.IsRuntimeDriverCompiled(driverpkg.RuntimeDriverMicrosandbox) {
			if err == nil || !strings.Contains(err.Error(), "not configured") {
				t.Fatalf("ForDriver compiled but unconfigured runtime error = %v", err)
			}
		} else if err == nil || !errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) || !errors.Is(err, domain.ErrUnsupported) {
			t.Fatalf("ForDriver uncompiled runtime error = %v", err)
		}
		if _, err := provider.ForSession(nil); err == nil || !strings.Contains(err.Error(), "session is required") {
			t.Fatalf("ForSession nil error = %v", err)
		}
	})

	t.Run("driver runtime adapter exec and stats", func(t *testing.T) {
		driverRuntime := &fakeDriverAdapterRuntime{
			execResult: driverpkg.ExecResult{Stdout: "out", Output: "out", ExitCode: 0, Success: true},
			stats: driverpkg.SandboxStats{
				SandboxID:  "session-1",
				Driver:     driverpkg.RuntimeDriverDocker,
				CPUPercent: driverpkg.MetricValue{Value: float64PtrForAdapter(12), Unit: driverpkg.MetricUnitPercent, Status: driverpkg.MetricStatusOK},
			},
		}
		adapter := driverRuntimeAdapter{runtime: driverRuntime}
		session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-1", WorkspacePath: "/tmp/workspace"}}
		result, err := adapter.Exec(context.Background(), session, domain.VMState{BoxID: "sandbox-1"}, domain.ExecSpec{Command: "echo"})
		if err != nil || result.Stdout != "out" || driverRuntime.execSpec.Command != "echo" {
			t.Fatalf("Exec result=%#v spec=%#v err=%v", result, driverRuntime.execSpec, err)
		}
		var chunks []domain.ExecChunk
		streamed, err := adapter.ExecStream(context.Background(), session, domain.VMState{}, domain.ExecSpec{Command: "stream"}, func(chunk domain.ExecChunk) {
			chunks = append(chunks, chunk)
		})
		if err != nil || streamed.Output != "out" || len(chunks) != 1 || domain.NormalizeStdioStream(chunks[0].Stream) != domain.StdioStderr {
			t.Fatalf("ExecStream result=%#v chunks=%#v err=%v", streamed, chunks, err)
		}
		stats, err := adapter.Stats(context.Background(), session, domain.VMState{})
		if err != nil || stats.SandboxID != "session-1" || stats.CPUPercent.Value == nil || *stats.CPUPercent.Value != 12 {
			t.Fatalf("Stats = %#v err=%v", stats, err)
		}
		unsupported := driverRuntimeAdapter{runtime: fakeDriverNoStatsRuntime{}}
		if _, err := unsupported.Stats(context.Background(), session, domain.VMState{}); err == nil {
			t.Fatalf("unsupported Stats returned nil error")
		}
	})
}

type fakeDriverAdapterRuntime struct {
	execResult driverpkg.ExecResult
	execSpec   driverpkg.ExecSpec
	stats      driverpkg.SandboxStats
}

func (r *fakeDriverAdapterRuntime) EnsureSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ProxyState) (driverpkg.SandboxVMInfo, error) {
	return driverpkg.SandboxVMInfo{}, nil
}

func (r *fakeDriverAdapterRuntime) StopSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState) (bool, error) {
	return false, nil
}

func (r *fakeDriverAdapterRuntime) RemoveSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState) error {
	return nil
}

func (r *fakeDriverAdapterRuntime) Exec(_ context.Context, _ *driverpkg.Sandbox, _ driverpkg.VMState, spec driverpkg.ExecSpec) (driverpkg.ExecResult, error) {
	r.execSpec = spec
	return r.execResult, nil
}

func (r *fakeDriverAdapterRuntime) ExecStream(_ context.Context, _ *driverpkg.Sandbox, _ driverpkg.VMState, spec driverpkg.ExecSpec, stream driverpkg.ExecStreamWriter) (driverpkg.ExecResult, error) {
	r.execSpec = spec
	if stream != nil {
		stream(driverpkg.ExecChunk{Text: "err", Stream: driverpkg.StdioStderr})
	}
	return r.execResult, nil
}

func (r *fakeDriverAdapterRuntime) Stats(context.Context, *driverpkg.Sandbox, driverpkg.VMState) (driverpkg.SandboxStats, error) {
	return r.stats, nil
}

type fakeDriverNoStatsRuntime struct{}

func (fakeDriverNoStatsRuntime) EnsureSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ProxyState) (driverpkg.SandboxVMInfo, error) {
	return driverpkg.SandboxVMInfo{}, nil
}

func (fakeDriverNoStatsRuntime) StopSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState) (bool, error) {
	return false, nil
}

func (fakeDriverNoStatsRuntime) RemoveSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState) error {
	return nil
}

func (fakeDriverNoStatsRuntime) Exec(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ExecSpec) (driverpkg.ExecResult, error) {
	return driverpkg.ExecResult{}, nil
}

func (fakeDriverNoStatsRuntime) ExecStream(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ExecSpec, driverpkg.ExecStreamWriter) (driverpkg.ExecResult, error) {
	return driverpkg.ExecResult{}, nil
}

func float64PtrForAdapter(value float64) *float64 {
	return &value
}

func TestCapabilitySandboxResolverCoverage(t *testing.T) {
	ctx := context.Background()
	running := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:       "sandbox-running",
			VMStatus: domain.VMStatusRunning,
			Tags: []domain.SandboxTag{
				{Name: capabilities.CapsetTagName, Value: "dev"},
				{Name: capabilities.CapsetTagName, Value: " dev "},
				{Name: "project", Value: " project-1 "},
				{Name: domain.AgentSandboxTagID, Value: " agent-1 "},
			},
		},
		EnvItems: []domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: "token-2", Secret: true}},
	}
	stopped := &domain.Sandbox{
		Summary:  domain.SandboxSummary{ID: "sandbox-stopped", VMStatus: domain.VMStatusStopped, Tags: []domain.SandboxTag{{Name: capabilities.CapsetTagName, Value: "dev"}}},
		EnvItems: []domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: "token-stopped", Secret: true}},
	}
	legacyScoped := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:       "sandbox-legacy-scope",
			VMStatus: domain.VMStatusRunning,
			Tags: []domain.SandboxTag{
				{Name: capabilities.CapsetTagName, Value: "legacy/dev"},
				{Name: "project_id", Value: "legacy-project"},
				{Name: domain.AgentSandboxTagID, Value: "legacy-agent"},
			},
		},
		EnvItems: []domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: "token-legacy-scope", Secret: true}},
	}
	unscoped := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:       "sandbox-unscoped",
			VMStatus: domain.VMStatusRunning,
			Tags:     []domain.SandboxTag{{Name: capabilities.CapsetTagName, Value: "legacy"}},
		},
		EnvItems: []domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: "token-unscoped", Secret: true}},
	}
	noCapset := &domain.Sandbox{
		Summary:  domain.SandboxSummary{ID: "sandbox-no-capset", VMStatus: domain.VMStatusRunning},
		EnvItems: []domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: "token-no-capset", Secret: true}},
	}
	store := &fakeCapabilitySandboxStore{pages: []domain.SandboxListResult{
		{Sandboxes: []*domain.Sandbox{{Summary: domain.SandboxSummary{ID: "sandbox-other", VMStatus: domain.VMStatusRunning}}}, HasMore: true, NextOffset: 200},
		{Sandboxes: []*domain.Sandbox{nil, running, stopped, noCapset, legacyScoped, unscoped}},
	}}
	resolver := NewCapabilitySandboxResolver(store)
	binding, err := resolver.ResolveCapabilitySandbox(ctx, " token-2 ")
	if err != nil {
		t.Fatalf("ResolveCapabilitySandbox returned error: %v", err)
	}
	if binding.SandboxID != "sandbox-running" || binding.ManagedProjectID != "project-1" || binding.ManagedAgentID != "agent-1" || len(binding.CapsetIDs) != 1 || binding.CapsetIDs[0] != "dev" {
		t.Fatalf("binding = %#v", binding)
	}
	if len(store.offsets) != 2 || store.offsets[0] != 0 || store.offsets[1] != 200 {
		t.Fatalf("offsets = %#v", store.offsets)
	}
	legacyBinding, err := resolver.ResolveCapabilitySandbox(ctx, "token-legacy-scope")
	if err != nil || legacyBinding.ManagedProjectID != "legacy-project" || legacyBinding.ManagedAgentID != "legacy-agent" {
		t.Fatalf("legacy scoped binding = %#v err=%v", legacyBinding, err)
	}
	unscopedBinding, err := resolver.ResolveCapabilitySandbox(ctx, "token-unscoped")
	if err != nil || unscopedBinding.ManagedProjectID != "" || unscopedBinding.ManagedAgentID != "" {
		t.Fatalf("unscoped binding = %#v err=%v", unscopedBinding, err)
	}
	resolver.RevokeSandbox("sandbox-running")
	if _, err := resolver.ResolveCapabilitySandbox(ctx, "token-2"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("revoked token error = %v", err)
	}
	resolver.IndexSandbox(running)
	binding, err = resolver.ResolveCapabilitySandbox(ctx, "token-2")
	if err != nil || binding.SandboxID != "sandbox-running" {
		t.Fatalf("indexed binding = %#v err=%v", binding, err)
	}

	for _, tc := range []struct {
		name  string
		token string
		part  string
	}{
		{name: "empty token", token: " ", part: "required"},
		{name: "stopped sandbox", token: "token-stopped", part: "not found"},
		{name: "no capset", token: "token-no-capset", part: "not found"},
		{name: "not found", token: "missing", part: "not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolver.ResolveCapabilitySandbox(ctx, tc.token)
			if err == nil || !strings.Contains(err.Error(), tc.part) {
				t.Fatalf("ResolveCapabilitySandbox error = %v, want %q", err, tc.part)
			}
		})
	}

	if _, err := (*CapabilitySandboxResolver)(nil).ResolveCapabilitySandbox(ctx, "token"); err == nil || !strings.Contains(err.Error(), "store") {
		t.Fatalf("nil resolver error = %v", err)
	}
	listErr := errors.New("list failed")
	failing := NewCapabilitySandboxResolver(&fakeCapabilitySandboxStore{err: listErr})
	if _, err := failing.ResolveCapabilitySandbox(ctx, "token-2"); !errors.Is(err, listErr) {
		t.Fatalf("store error = %v, want %v", err, listErr)
	}
}

type fakeCapabilitySandboxStore struct {
	pages   []domain.SandboxListResult
	offsets []int
	err     error
}

func (s *fakeCapabilitySandboxStore) ListSandboxes(_ context.Context, opts domain.SandboxListOptions) (domain.SandboxListResult, error) {
	s.offsets = append(s.offsets, opts.Offset)
	if s.err != nil {
		return domain.SandboxListResult{}, s.err
	}
	index := opts.Offset / 200
	if index >= len(s.pages) {
		return domain.SandboxListResult{}, nil
	}
	return s.pages[index], nil
}
