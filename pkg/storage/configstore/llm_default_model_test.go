package configstore

import (
	"context"
	"errors"
	"testing"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestIntegrationDefaultModelLifecycle(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	key := "gateway-key"
	if _, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "gateway", BaseURL: "https://gateway.example/v1", Protocol: llms.APIProtocolResponses, APIKey: &key,
		Models: declaredModels(llms.ModelSpec{ID: "gpt-4o"}),
	}); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if _, _, ok, err := store.DefaultLLMModelReference(ctx); err != nil || ok {
		t.Fatalf("default before any write ok=%v err=%v, want none", ok, err)
	}
	if _, err := resolveCatalogModel(ctx, store, ""); !errors.Is(err, llms.ErrNoModel) {
		t.Fatalf("resolve without a default error = %v, want ErrNoModel", err)
	}

	// A literal model the connection does not enumerate is still a valid default:
	// model ids are opaque, and the connection forwards them as written.
	stored, err := store.SetDefaultLLMModel(ctx, llms.ModelReference{ProviderID: "gateway", ModelID: "gpt-5-preview"})
	if err != nil {
		t.Fatalf("set default: %v", err)
	}
	if stored.ProviderID != "gateway" || stored.ModelID != "gpt-5-preview" {
		t.Fatalf("stored default = %#v", stored)
	}
	target, err := resolveCatalogModel(ctx, store, "")
	if err != nil {
		t.Fatalf("resolve the stored default: %v", err)
	}
	if target.Provider.ID != "gateway" || target.Model.ID != "gpt-5-preview" {
		t.Fatalf("default target = %#v", target)
	}

	// A models.json that declares no default must not wipe the operator's
	// choice, which is what makes the reference usable across restarts.
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{Providers: map[string]llms.CatalogProvider{}}); err != nil {
		t.Fatalf("apply an empty catalog: %v", err)
	}
	if providerID, modelID, ok, err := store.DefaultLLMModelReference(ctx); err != nil || !ok || providerID != "gateway" || modelID != "gpt-5-preview" {
		t.Fatalf("default after an empty catalog = %q/%q ok=%v err=%v, want preserved", providerID, modelID, ok, err)
	}

	// A models.json that does declare a default has the last word.
	baseURL, protocol, apiKey := "https://catalog.example/v1", llms.APIProtocolResponses, "catalog-key"
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{
		Default: "catalog/catalog-model",
		Providers: map[string]llms.CatalogProvider{
			"catalog": {BaseURL: &baseURL, Protocol: &protocol, APIKey: &apiKey},
		},
	}); err != nil {
		t.Fatalf("apply a declared default: %v", err)
	}
	if providerID, modelID, ok, err := store.DefaultLLMModelReference(ctx); err != nil || !ok || providerID != "catalog" || modelID != "catalog-model" {
		t.Fatalf("default after a declared catalog default = %q/%q ok=%v err=%v", providerID, modelID, ok, err)
	}

	if err := store.ClearDefaultLLMModel(ctx); err != nil {
		t.Fatalf("clear default: %v", err)
	}
	if providerID, modelID, ok, err := store.DefaultLLMModelReference(ctx); err != nil || ok {
		t.Fatalf("default after clearing = %q/%q ok=%v err=%v, want none", providerID, modelID, ok, err)
	}
	if _, err := resolveCatalogModel(ctx, store, ""); !errors.Is(err, llms.ErrNoModel) {
		t.Fatalf("resolve after clearing error = %v, want ErrNoModel", err)
	}
}

func TestIntegrationSetDefaultModelRejectsUnusableReferences(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	key := "gateway-key"
	disabled := false
	if _, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "gateway", BaseURL: "https://gateway.example/v1", Protocol: llms.APIProtocolResponses, APIKey: &key, Enabled: &disabled,
	}); err != nil {
		t.Fatalf("create disabled provider: %v", err)
	}
	cases := []struct {
		name      string
		reference llms.ModelReference
		want      error
	}{
		{name: "missing provider", reference: llms.ModelReference{ProviderID: "absent", ModelID: "gpt-4o"}, want: domain.ErrNotFound},
		{name: "missing model", reference: llms.ModelReference{ProviderID: "gateway"}, want: domain.ErrInvalidArgument},
		{name: "missing provider id", reference: llms.ModelReference{ModelID: "gpt-4o"}, want: domain.ErrInvalidArgument},
		{name: "disabled provider", reference: llms.ModelReference{ProviderID: "gateway", ModelID: "gpt-4o"}, want: domain.ErrFailedPrecondition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.SetDefaultLLMModel(ctx, tc.reference); !errors.Is(err, tc.want) {
				t.Fatalf("SetDefaultLLMModel error = %v, want %v", err, tc.want)
			}
			if _, _, ok, err := store.DefaultLLMModelReference(ctx); err != nil || ok {
				t.Fatalf("default after a rejected write ok=%v err=%v, want none", ok, err)
			}
		})
	}
}
