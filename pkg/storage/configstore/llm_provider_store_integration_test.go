package configstore

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

func TestIntegrationManagedProviderLifecycleAndRouting(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	open := func() *storagesqlite.Database {
		t.Helper()
		db, err := storagesqlite.Open(path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		})
		return db
	}
	db := open()
	store := FromDB(db.DB())
	key := "first-key"
	input := llms.ProviderReplacement{ID: "gateway", BaseURL: "https://first.example/v1", Protocol: llms.APIProtocolResponses, APIKey: &key, Enabled: true}
	created, err := store.CreateLLMProvider(ctx, input)
	if err != nil || created.Scope != llms.ProviderScopeAPI || created.APIKey != key {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.CreateLLMProvider(ctx, input); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate: %v", err)
	}
	resolve := func() llms.ResolvedTarget {
		t.Helper()
		target, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{RequestedModel: "gateway/literal/model"})
		if err != nil {
			t.Fatal(err)
		}
		return target
	}
	target := resolve()
	if target.Model.ID != "literal/model" || target.Endpoint != "https://first.example/v1/responses" || target.Headers.Get("Authorization") != "Bearer first-key" {
		t.Fatal("created provider was not used for routing")
	}
	input.APIKey = nil
	input.BaseURL = "https://second.example/v1"
	if _, err := store.UpdateLLMProvider(ctx, input); err != nil {
		t.Fatal(err)
	}
	target = resolve()
	if target.Endpoint != "https://second.example/v1/responses" || target.Provider.APIKey != key {
		t.Fatal("update did not preserve key or replace URL")
	}
	rotated := "second-key"
	input.APIKey = &rotated
	input.Protocol = llms.APIProtocolMessages
	if _, err := store.UpdateLLMProvider(ctx, input); err != nil {
		t.Fatal(err)
	}
	target = resolve()
	if target.Headers.Get("x-api-key") != rotated || target.Headers.Get("Authorization") != "" || target.WireAPI != llms.APIProtocolMessages {
		t.Fatal("rotated credential/protocol not effective")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store = FromDB(open().DB())
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{}); err != nil {
		t.Fatal(err)
	}
	if resolve().Provider.APIKey != rotated {
		t.Fatal("restart/catalog synchronization lost API configuration")
	}
	input.Enabled = false
	if _, err := store.UpdateLLMProvider(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{RequestedModel: "gateway/literal/model"}); err == nil {
		t.Fatal("disabled provider routed a request")
	}
	listed, err := store.ListManagedLLMProviders(ctx)
	if err != nil || len(listed) != 1 || listed[0].Enabled {
		t.Fatalf("list disabled provider: %v", err)
	}
	hash, fingerprint := llms.HashFacadeToken("facade-key")
	if err := store.SaveLLMFacadeToken(ctx, llms.FacadeToken{TokenHash: hash, TokenFingerprint: fingerprint, SandboxID: "sandbox", ProviderID: input.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteLLMProvider(ctx, input.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetManagedLLMProvider(ctx, input.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	input.Enabled = true
	if _, err := store.CreateLLMProvider(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetLLMFacadeToken(ctx, "facade-key"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old token survived recreation: %v", err)
	}
}

func TestIntegrationManagedProviderOwnershipAndCancellation(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	url, protocol, key := "https://example.com/v1", llms.APIProtocolResponses, "catalog-key"
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{Providers: map[string]llms.CatalogProvider{"catalog": {BaseURL: &url, Protocol: &protocol, APIKey: &key}}}); err != nil {
		t.Fatal(err)
	}
	input := llms.ProviderReplacement{ID: "catalog", BaseURL: url, Protocol: protocol, APIKey: &key, Enabled: true}
	if _, err := store.CreateLLMProvider(ctx, input); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("create collision: %v", err)
	}
	if _, err := store.UpdateLLMProvider(ctx, input); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("update ownership: %v", err)
	}
	if err := store.DeleteLLMProvider(ctx, input.ID); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("delete ownership: %v", err)
	}
	providers, err := store.ListManagedLLMProviders(ctx)
	if err != nil || len(providers) != 0 {
		t.Fatalf("catalog leaked into API list: %v", err)
	}
	input.ID = "missing"
	if _, err := store.UpdateLLMProvider(ctx, input); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	if err := store.DeleteLLMProvider(ctx, input.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
	input.APIKey = nil
	if _, err := store.CreateLLMProvider(ctx, input); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("create missing key: %v", err)
	}
	input.APIKey = &key
	if _, err := store.CreateLLMProvider(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{Providers: map[string]llms.CatalogProvider{input.ID: {BaseURL: &url, Protocol: &protocol, APIKey: &key}}}); err == nil {
		t.Fatal("catalog overwrote API-owned provider")
	}
	if provider, err := store.GetManagedLLMProvider(ctx, input.ID); err != nil || provider.APIKey != key {
		t.Fatalf("collision damaged provider: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.CreateLLMProvider(canceled, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("create canceled: %v", err)
	}
	if _, err := store.UpdateLLMProvider(canceled, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("update canceled: %v", err)
	}
	if _, err := store.GetManagedLLMProvider(canceled, input.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("get canceled: %v", err)
	}
	if _, err := store.ListManagedLLMProviders(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("list canceled: %v", err)
	}
	if err := store.DeleteLLMProvider(canceled, input.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("delete canceled: %v", err)
	}
}

func TestIntegrationManagedProviderConcurrentCreate(t *testing.T) {
	db, err := storagesqlite.Open(filepath.Join(t.TempDir(), "data.db"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store := FromDB(db.DB())
	key := "key"
	input := llms.ProviderReplacement{ID: "concurrent", BaseURL: "https://example.com", Protocol: llms.APIProtocolResponses, APIKey: &key, Enabled: true}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() { _, err := store.CreateLLMProvider(context.Background(), input); results <- err })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, domain.ErrAlreadyExists) {
			t.Errorf("unexpected concurrent create error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful creates = %d, want 1", successes)
	}
}
