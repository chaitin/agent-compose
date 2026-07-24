package adapters

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/capproxy"
	domain "agent-compose/pkg/model"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

type integrationRawFrame []byte

type integrationRawCodec struct{}

func (integrationRawCodec) Name() string { return "project-octobus-integration" }

func (integrationRawCodec) Marshal(value any) ([]byte, error) {
	switch typed := value.(type) {
	case integrationRawFrame:
		return typed, nil
	case *integrationRawFrame:
		return *typed, nil
	case proto.Message:
		return proto.Marshal(typed)
	default:
		return nil, fmt.Errorf("unsupported integration message %T", value)
	}
}

func (integrationRawCodec) Unmarshal(data []byte, value any) error {
	switch typed := value.(type) {
	case *integrationRawFrame:
		*typed = append((*typed)[:0], data...)
		return nil
	case proto.Message:
		return proto.Unmarshal(data, typed)
	default:
		return fmt.Errorf("unsupported integration message %T", value)
	}
}

type integrationAgentStore struct {
	mu          sync.RWMutex
	definitions map[string]domain.AgentDefinition
}

func (s *integrationAgentStore) GetAgentDefinition(_ context.Context, id string) (domain.AgentDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	definition, ok := s.definitions[id]
	if !ok {
		return domain.AgentDefinition{}, domain.ErrNotFound
	}
	return definition, nil
}

func (s *integrationAgentStore) set(definition domain.AgentDefinition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.definitions[definition.ID] = definition
}

type integrationSandboxResolver struct {
	bindings map[string]capproxy.SandboxBinding
}

func (r integrationSandboxResolver) ResolveCapabilitySandbox(_ context.Context, token string) (capproxy.SandboxBinding, error) {
	binding, ok := r.bindings[token]
	if !ok {
		return capproxy.SandboxBinding{}, domain.ErrNotFound
	}
	return binding, nil
}

type observedOctoBusCall struct {
	Authorization string
	Capset        string
	Instance      string
}

func TestProjectOctoBusProxyRoutesLegacyAndQualifiedCapsets(t *testing.T) {
	legacyAddr, legacyCalls := startObservedOctoBus(t)
	internalAddr, internalCalls := startObservedOctoBus(t)
	publicAddr, publicCalls := startObservedOctoBus(t)
	store := &integrationAgentStore{definitions: map[string]domain.AgentDefinition{}}
	store.set(projectOctoBusDefinition("agent-1", "project-1", map[string][2]string{
		"internal": {internalAddr, "internal-token"},
		"public":   {publicAddr, "public-token"},
	}))
	binding := capproxy.SandboxBinding{
		SandboxID:        "sandbox-1",
		ManagedProjectID: "project-1",
		ManagedAgentID:   "agent-1",
		CapsetIDs:        []string{"legacy-capset", "internal/dev", "public/web-search"},
	}
	proxyAddr := startProjectOctoBusProxy(t, legacyAddr, "legacy-token", store, map[string]capproxy.SandboxBinding{"sandbox-token": binding})

	for _, declaration := range binding.CapsetIDs {
		if got := invokeProjectOctoBus(t, proxyAddr, "sandbox-token", declaration, "instance-1"); got != "ok" {
			t.Fatalf("response for %q = %q", declaration, got)
		}
	}
	assertObservedCall(t, legacyCalls, observedOctoBusCall{"Bearer legacy-token", "legacy-capset", "instance-1"})
	assertObservedCall(t, internalCalls, observedOctoBusCall{"Bearer internal-token", "dev", "instance-1"})
	assertObservedCall(t, publicCalls, observedOctoBusCall{"Bearer public-token", "web-search", "instance-1"})
}

func TestProjectOctoBusProxyUsesUpdatedDefinitionWithoutRebuildingSandbox(t *testing.T) {
	firstAddr, firstCalls := startObservedOctoBus(t)
	secondAddr, secondCalls := startObservedOctoBus(t)
	store := &integrationAgentStore{definitions: map[string]domain.AgentDefinition{}}
	store.set(projectOctoBusDefinition("agent-1", "project-1", map[string][2]string{"internal": {firstAddr, "first-token"}}))
	binding := capproxy.SandboxBinding{SandboxID: "sandbox-1", ManagedProjectID: "project-1", ManagedAgentID: "agent-1", CapsetIDs: []string{"internal/dev"}}
	proxyAddr := startProjectOctoBusProxy(t, "127.0.0.1:1", "", store, map[string]capproxy.SandboxBinding{"sandbox-token": binding})

	invokeProjectOctoBus(t, proxyAddr, "sandbox-token", "internal/dev", "instance-1")
	assertObservedCall(t, firstCalls, observedOctoBusCall{"Bearer first-token", "dev", "instance-1"})
	store.set(projectOctoBusDefinition("agent-1", "project-1", map[string][2]string{"internal": {secondAddr, "second-token"}}))
	invokeProjectOctoBus(t, proxyAddr, "sandbox-token", "internal/dev", "instance-2")
	assertObservedCall(t, secondCalls, observedOctoBusCall{"Bearer second-token", "dev", "instance-2"})
}

func TestProjectOctoBusProxyDoesNotCrossProjectCredentials(t *testing.T) {
	firstAddr, firstCalls := startObservedOctoBus(t)
	secondAddr, secondCalls := startObservedOctoBus(t)
	store := &integrationAgentStore{definitions: map[string]domain.AgentDefinition{}}
	store.set(projectOctoBusDefinition("agent-1", "project-1", map[string][2]string{"internal": {firstAddr, "project-1-token"}}))
	store.set(projectOctoBusDefinition("agent-2", "project-2", map[string][2]string{"internal": {secondAddr, "project-2-token"}}))
	proxyAddr := startProjectOctoBusProxy(t, "127.0.0.1:1", "", store, map[string]capproxy.SandboxBinding{
		"token-1": {SandboxID: "sandbox-1", ManagedProjectID: "project-1", ManagedAgentID: "agent-1", CapsetIDs: []string{"internal/dev"}},
		"token-2": {SandboxID: "sandbox-2", ManagedProjectID: "project-2", ManagedAgentID: "agent-2", CapsetIDs: []string{"internal/dev"}},
	})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			invokeProjectOctoBus(t, proxyAddr, "token-1", "internal/dev", "project-1-instance")
		}()
		go func() {
			defer wg.Done()
			invokeProjectOctoBus(t, proxyAddr, "token-2", "internal/dev", "project-2-instance")
		}()
	}
	wg.Wait()
	for i := 0; i < 20; i++ {
		assertObservedCall(t, firstCalls, observedOctoBusCall{"Bearer project-1-token", "dev", "project-1-instance"})
		assertObservedCall(t, secondCalls, observedOctoBusCall{"Bearer project-2-token", "dev", "project-2-instance"})
	}
}

func TestCapabilitySandboxResolverRebuildRestoresProjectScope(t *testing.T) {
	sandbox := &domain.Sandbox{
		Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning, Tags: []domain.SandboxTag{
			{Name: capabilities.CapsetTagName, Value: "legacy-capset"},
			{Name: capabilities.CapsetTagName, Value: "internal/dev"},
			{Name: "project", Value: "project-1"},
			{Name: domain.AgentSandboxTagID, Value: "agent-1"},
		}},
		EnvItems: []domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: "sandbox-token", Secret: true}},
	}
	resolver := NewCapabilitySandboxResolver(&integrationSandboxStore{sandboxes: []*domain.Sandbox{sandbox}})
	if err := resolver.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	binding, err := resolver.ResolveCapabilitySandbox(context.Background(), "sandbox-token")
	if err != nil {
		t.Fatal(err)
	}
	if binding.ManagedProjectID != "project-1" || binding.ManagedAgentID != "agent-1" || strings.Join(binding.CapsetIDs, ",") != "legacy-capset,internal/dev" {
		t.Fatalf("rebuilt binding = %#v", binding)
	}
}

type integrationSandboxStore struct{ sandboxes []*domain.Sandbox }

func (s *integrationSandboxStore) ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error) {
	return domain.SandboxListResult{Sandboxes: s.sandboxes}, nil
}

func projectOctoBusDefinition(agentID, projectID string, servers map[string][2]string) domain.AgentDefinition {
	parts := make([]string, 0, len(servers))
	for name, target := range servers {
		parts = append(parts, fmt.Sprintf("%q:{\"url\":%q,\"token\":%q}", name, target[0], target[1]))
	}
	return domain.AgentDefinition{ID: agentID, ManagedProjectID: projectID, ConfigJSON: `{"octobus_servers":{` + strings.Join(parts, ",") + `}}`}
}

func startObservedOctoBus(t *testing.T) (string, <-chan observedOctoBusCall) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan observedOctoBusCall, 64)
	server := grpc.NewServer(grpc.ForceServerCodec(integrationRawCodec{}), grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		md, _ := metadata.FromIncomingContext(stream.Context())
		calls <- observedOctoBusCall{firstIntegrationMetadata(md, "authorization"), firstIntegrationMetadata(md, "x-octobus-capset"), firstIntegrationMetadata(md, "x-octobus-instance")}
		var request integrationRawFrame
		if err := stream.RecvMsg(&request); err != nil {
			return err
		}
		return stream.SendMsg(integrationRawFrame("ok"))
	}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String(), calls
}

func startProjectOctoBusProxy(t *testing.T, globalAddr, globalToken string, store AgentDefinitionStore, bindings map[string]capproxy.SandboxBinding) string {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	ctx, cancel := context.WithCancel(context.Background())
	server := capproxy.NewServer(capproxy.Config{
		Listen:  addr,
		OctoBus: func(context.Context) (string, string, bool) { return globalAddr, globalToken, true },
		Targets: NewProjectOctoBusTargetResolver(store),
	}, integrationSandboxResolver{bindings: bindings})
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errCh; err != nil {
			t.Errorf("capability proxy: %v", err)
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("capability proxy did not listen at %s: %v", addr, dialErr)
		}
		time.Sleep(time.Millisecond)
	}
	return addr
}

func invokeProjectOctoBus(t *testing.T, proxyAddr, sandboxToken, capset, instance string) string {
	t.Helper()
	codec := integrationRawCodec{}
	conn, err := grpc.NewClient(proxyAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.ForceCodec(codec)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(
		capproxy.SandboxTokenMetadata, sandboxToken,
		"x-octobus-capset", capset,
		"x-octobus-instance", instance,
	))
	var response integrationRawFrame
	if err := conn.Invoke(ctx, "/integration.Capability/Call", integrationRawFrame("request"), &response); err != nil {
		t.Errorf("invoke %s: %v", capset, err)
	}
	return string(response)
}

func assertObservedCall(t *testing.T, calls <-chan observedOctoBusCall, want observedOctoBusCall) {
	t.Helper()
	select {
	case got := <-calls:
		if got != want {
			t.Fatalf("upstream metadata = %#v, want %#v", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for upstream metadata %#v", want)
	}
}

func firstIntegrationMetadata(md metadata.MD, key string) string {
	values := md.Get(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
