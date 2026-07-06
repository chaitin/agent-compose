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

	t.Run("loader session rpc linked session id", func(t *testing.T) {
		if got := LoaderSessionRPCLinkedSessionID("CreateSession", `{"sessionId":"request-session"}`, `{"session":{"summary":{"sessionId":"response-session"}}}`); got != "response-session" {
			t.Fatalf("response session id = %q", got)
		}
		if got := LoaderSessionRPCLinkedSessionID("StopSession", `{"sessionId":" request-session "}`, `{bad`); got != "request-session" {
			t.Fatalf("request session id = %q", got)
		}
		if got := LoaderSessionRPCLinkedSessionID("ListSessions", `{"sessionId":"ignored"}`, `{}`); got != "" {
			t.Fatalf("ListSessions linked id = %q, want empty", got)
		}
		if got := loaderSessionIDFromJSON(`{"session":{"summary":{"sessionId":" nested "}}}`); got != "nested" {
			t.Fatalf("nested session id = %q", got)
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
			config: &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverBoxlite},
			runtimes: map[string]BoxRuntime{
				driverpkg.RuntimeDriverDocker:  runtime,
				driverpkg.RuntimeDriverBoxlite: runtime,
			},
		}
		if got, err := provider.ForDriver("docker-engine"); err != nil || got != runtime {
			t.Fatalf("ForDriver docker-engine = %T/%v", got, err)
		}
		if got, err := provider.ForSession(&domain.Session{Summary: domain.SessionSummary{Driver: ""}}); err != nil || got != runtime {
			t.Fatalf("ForSession default driver = %T/%v", got, err)
		}
		if _, err := provider.ForDriver("bad-driver"); err == nil {
			t.Fatalf("ForDriver bad-driver returned nil error")
		}
		if _, err := provider.ForDriver(driverpkg.RuntimeDriverMicrosandbox); err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("ForDriver missing runtime error = %v", err)
		}
		if _, err := provider.ForSession(nil); err == nil || !strings.Contains(err.Error(), "session is required") {
			t.Fatalf("ForSession nil error = %v", err)
		}
	})
}

func TestCapabilitySessionResolverCoverage(t *testing.T) {
	ctx := context.Background()
	running := &domain.Session{
		Summary: domain.SessionSummary{
			ID:       "session-running",
			VMStatus: domain.VMStatusRunning,
			Tags: []domain.SessionTag{
				{Name: capabilities.CapsetTagName, Value: "dev"},
				{Name: capabilities.CapsetTagName, Value: " dev "},
			},
		},
		EnvItems: []domain.SessionEnvVar{{Name: capabilities.SessionTokenEnvName, Value: "token-2", Secret: true}},
	}
	stopped := &domain.Session{
		Summary:  domain.SessionSummary{ID: "session-stopped", VMStatus: domain.VMStatusStopped, Tags: []domain.SessionTag{{Name: capabilities.CapsetTagName, Value: "dev"}}},
		EnvItems: []domain.SessionEnvVar{{Name: capabilities.SessionTokenEnvName, Value: "token-stopped", Secret: true}},
	}
	noCapset := &domain.Session{
		Summary:  domain.SessionSummary{ID: "session-no-capset", VMStatus: domain.VMStatusRunning},
		EnvItems: []domain.SessionEnvVar{{Name: capabilities.SessionTokenEnvName, Value: "token-no-capset", Secret: true}},
	}
	store := &fakeCapabilitySessionStore{pages: []domain.SessionListResult{
		{Sessions: []*domain.Session{{Summary: domain.SessionSummary{ID: "session-other", VMStatus: domain.VMStatusRunning}}}, HasMore: true, NextOffset: 200},
		{Sessions: []*domain.Session{nil, running, stopped, noCapset}},
	}}
	resolver := NewCapabilitySessionResolver(store)
	binding, err := resolver.ResolveCapabilitySession(ctx, " token-2 ")
	if err != nil {
		t.Fatalf("ResolveCapabilitySession returned error: %v", err)
	}
	if binding.SessionID != "session-running" || len(binding.CapsetIDs) != 1 || binding.CapsetIDs[0] != "dev" {
		t.Fatalf("binding = %#v", binding)
	}
	if len(store.offsets) != 2 || store.offsets[0] != 0 || store.offsets[1] != 200 {
		t.Fatalf("offsets = %#v", store.offsets)
	}

	for _, tc := range []struct {
		name  string
		token string
		part  string
	}{
		{name: "empty token", token: " ", part: "required"},
		{name: "stopped session", token: "token-stopped", part: "not active"},
		{name: "no capset", token: "token-no-capset", part: "no capability capset"},
		{name: "not found", token: "missing", part: "not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolver.ResolveCapabilitySession(ctx, tc.token)
			if err == nil || !strings.Contains(err.Error(), tc.part) {
				t.Fatalf("ResolveCapabilitySession error = %v, want %q", err, tc.part)
			}
		})
	}

	if _, err := (*CapabilitySessionResolver)(nil).ResolveCapabilitySession(ctx, "token"); err == nil || !strings.Contains(err.Error(), "store") {
		t.Fatalf("nil resolver error = %v", err)
	}
	store.err = errors.New("list failed")
	if _, err := resolver.ResolveCapabilitySession(ctx, "token-2"); !errors.Is(err, store.err) {
		t.Fatalf("store error = %v, want %v", err, store.err)
	}
}

type fakeCapabilitySessionStore struct {
	pages   []domain.SessionListResult
	offsets []int
	err     error
}

func (s *fakeCapabilitySessionStore) ListSessions(_ context.Context, opts domain.SessionListOptions) (domain.SessionListResult, error) {
	s.offsets = append(s.offsets, opts.Offset)
	if s.err != nil {
		return domain.SessionListResult{}, s.err
	}
	index := opts.Offset / 200
	if index >= len(s.pages) {
		return domain.SessionListResult{}, nil
	}
	return s.pages[index], nil
}
