package adapters

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestSandboxRPCBridgeBindsSandboxToCallerTrustedHeaders(t *testing.T) {
	ctx := context.Background()
	bridge, _ := newTestSandboxRPCBridge(t)
	bridge.cap = testCapabilityProvider{target: "agent-compose:9100"}
	resolver := NewCapabilitySandboxResolver(bridge.store)
	resolver.initialized = true
	bridge.capTokens = resolver

	runCtx := domain.NewContextWithTrustedHeaders(ctx, []domain.TrustedHeader{{Name: "x-mpi-username", Value: "owner"}})
	responseJSON, err := bridge.CallJSONWithSource(runCtx, "CreateSandbox", `{"title":"scripted","capsetIds":["dev"]}`, domain.SandboxTypeScript+":scheduler-1")
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	var created sandboxRPCResponse
	if err := json.Unmarshal([]byte(responseJSON), &created); err != nil || created.Sandbox == nil || created.Sandbox.Summary == nil {
		t.Fatalf("CreateSandbox response = %s, err = %v", responseJSON, err)
	}
	sandbox, err := bridge.store.GetSandbox(ctx, created.Sandbox.Summary.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	token := capabilities.SandboxToken(sandbox)
	assertCapabilityTrustedHeaders(t, resolver, token, []domain.TrustedHeader{{Name: "x-octobus-ext-username", Value: "owner"}})

	if _, err := bridge.StopSandbox(ctx, sandbox.Summary.ID); err != nil {
		t.Fatalf("StopSandbox returned error: %v", err)
	}
	userCtx := domain.NewContextWithTrustedHeaders(ctx, []domain.TrustedHeader{{Name: "x-mpi-username", Value: "user"}})
	if _, err := bridge.ResumeSandbox(userCtx, sandbox.Summary.ID); err != nil {
		t.Fatalf("ResumeSandbox returned error: %v", err)
	}
	assertCapabilityTrustedHeaders(t, resolver, token, []domain.TrustedHeader{{Name: "x-octobus-ext-username", Value: "user"}})

	if _, err := bridge.StopSandbox(ctx, sandbox.Summary.ID); err != nil {
		t.Fatalf("StopSandbox returned error: %v", err)
	}
	if _, err := bridge.ResumeSandbox(ctx, sandbox.Summary.ID); err != nil {
		t.Fatalf("ResumeSandbox returned error: %v", err)
	}
	assertCapabilityTrustedHeaders(t, resolver, token, nil)
}

func assertCapabilityTrustedHeaders(t *testing.T, resolver *CapabilitySandboxResolver, token string, want []domain.TrustedHeader) {
	t.Helper()
	binding, err := resolver.ResolveCapabilitySandbox(context.Background(), token)
	if err != nil {
		t.Fatalf("ResolveCapabilitySandbox returned error: %v", err)
	}
	if len(want) == 0 && len(binding.TrustedHeaders) == 0 {
		return
	}
	if !reflect.DeepEqual(binding.TrustedHeaders, want) {
		t.Fatalf("trusted headers = %#v, want %#v", binding.TrustedHeaders, want)
	}
}
