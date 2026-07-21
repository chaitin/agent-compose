package configstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

func TestFacadeGrantPersistsSparseEnvironmentOutsideSelectableProviders(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema returned error: %v", err)
	}
	rawToken, grant := testFacadeEnvironmentGrant(t, "sandbox-a", "run-a", llms.FacadeEnvironment{
		ProviderType: llms.ProviderFamilyOpenAI,
		Endpoint:     "https://execution.example/v1",
	})
	if err := store.SaveLLMFacadeGrant(ctx, grant); err != nil {
		t.Fatalf("SaveLLMFacadeGrant returned error: %v", err)
	}

	storedToken, err := store.GetLLMFacadeToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if storedToken.ProviderID != grant.Token.ProviderID {
		t.Fatalf("stored token provider = %q", storedToken.ProviderID)
	}
	environment, ok, err := store.GetLLMFacadeEnvironment(ctx, storedToken.ProviderID)
	if err != nil || !ok {
		t.Fatalf("GetLLMFacadeEnvironment ok=%v err=%v", ok, err)
	}
	if environment.Endpoint != "https://execution.example/v1" || environment.APIKey != "" || environment.Protocol != "" {
		t.Fatalf("stored sparse environment = %#v", environment)
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	if len(providers) != 0 {
		t.Fatalf("token environment became selectable: %#v", providers)
	}

	if err := store.DeleteLLMFacadeToken(ctx, rawToken); err != nil {
		t.Fatalf("DeleteLLMFacadeToken returned error: %v", err)
	}
	if _, ok, err := store.GetLLMFacadeEnvironment(ctx, storedToken.ProviderID); err != nil || ok {
		t.Fatalf("deleted token environment ok=%v err=%v", ok, err)
	}
	if _, err := store.GetLLMFacadeToken(ctx, rawToken); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleted token lookup error = %v", err)
	}
}

func TestRevokeSandboxFacadeTokensDeletesOnlyOwnedEnvironments(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema returned error: %v", err)
	}
	_, first := testFacadeEnvironmentGrant(t, "sandbox-a", "run-a", llms.FacadeEnvironment{
		ProviderType: llms.ProviderFamilyOpenAI,
		APIKey:       "sandbox-a-key",
	})
	secondRaw, second := testFacadeEnvironmentGrant(t, "sandbox-b", "run-b", llms.FacadeEnvironment{
		ProviderType: llms.ProviderFamilyOpenAI,
		APIKey:       "sandbox-b-key",
	})
	for _, grant := range []llms.FacadeGrant{first, second} {
		if err := store.SaveLLMFacadeGrant(ctx, grant); err != nil {
			t.Fatalf("SaveLLMFacadeGrant returned error: %v", err)
		}
	}
	if err := store.UpsertDefaultLLMConfig(ctx,
		llms.Provider{ID: "system-provider", ProviderType: llms.ProviderFamilyOpenAI, BaseURL: "https://system.example/v1", Scope: llms.ProviderScopeSystem},
		llms.Model{ID: "system-model", Name: "system-model", DefaultModel: true, Scope: llms.ProviderScopeSystem},
	); err != nil {
		t.Fatalf("UpsertDefaultLLMConfig returned error: %v", err)
	}

	if err := store.RevokeLLMFacadeTokensForSandbox(ctx, "sandbox-a"); err != nil {
		t.Fatalf("RevokeLLMFacadeTokensForSandbox returned error: %v", err)
	}
	if _, ok, err := store.GetLLMFacadeEnvironment(ctx, first.Token.ProviderID); err != nil || ok {
		t.Fatalf("sandbox A environment remains ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.GetLLMFacadeEnvironment(ctx, second.Token.ProviderID); err != nil || !ok {
		t.Fatalf("sandbox B environment missing ok=%v err=%v", ok, err)
	}
	if token, err := store.GetLLMFacadeToken(ctx, secondRaw); err != nil || !token.RevokedAt.IsZero() {
		t.Fatalf("sandbox B token = %#v err=%v", token, err)
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil || len(providers) != 1 || providers[0].ID != "system-provider" {
		t.Fatalf("configured providers = %#v err=%v", providers, err)
	}
}

func TestFacadeGrantRejectsMissingEnvironmentAtomically(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema returned error: %v", err)
	}
	rawToken, token, err := llms.NewFacadeToken("sandbox-a", "model-a", "", llms.APIProtocolResponses, "agent", "run-a")
	if err != nil {
		t.Fatalf("NewFacadeToken returned error: %v", err)
	}
	token.ProviderID = llms.FacadeEnvironmentProviderID(token.TokenHash, llms.ProviderFamilyOpenAI)
	if err := store.SaveLLMFacadeGrant(ctx, llms.FacadeGrant{Token: token}); err == nil {
		t.Fatal("SaveLLMFacadeGrant accepted a missing environment")
	}
	if _, err := store.GetLLMFacadeToken(ctx, rawToken); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("invalid grant persisted token: %v", err)
	}
}

func testFacadeEnvironmentGrant(t *testing.T, sandboxID, runID string, environment llms.FacadeEnvironment) (string, llms.FacadeGrant) {
	t.Helper()
	target := llms.FacadeTarget{
		Target: llms.ResolvedTarget{
			Provider: llms.Provider{ID: "environment", ProviderType: environment.ProviderType},
			Model:    llms.Model{ID: "model-a", Name: "model-a"},
			WireAPI:  llms.APIProtocolResponses,
		},
		Environment: &environment,
	}
	rawToken, grant, err := llms.NewFacadeGrant(sandboxID, target, llms.APIProtocolResponses, "agent", runID)
	if err != nil {
		t.Fatalf("NewFacadeGrant returned error: %v", err)
	}
	grant.Token.ExpiresAt = time.Now().UTC().Add(time.Hour)
	return rawToken, grant
}
