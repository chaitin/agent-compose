package llms

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeCatalogStore struct {
	providers   []Provider
	models      []Model
	bindings    []ProviderModelBinding
	defProvider string
	defModel    string
	hasDefault  bool
}

func (f fakeCatalogStore) ListEnabledLLMProviders(context.Context) ([]Provider, error) {
	return f.providers, nil
}

func (f fakeCatalogStore) ListEnabledLLMModels(context.Context) ([]Model, error) {
	return f.models, nil
}

func (f fakeCatalogStore) ListLLMProviderModelConfigs(context.Context) ([]ProviderModelBinding, error) {
	return f.bindings, nil
}

func (f fakeCatalogStore) DefaultLLMModelReference(context.Context) (string, string, bool, error) {
	return f.defProvider, f.defModel, f.hasDefault, nil
}

func catalogOpenAIConnection(id, baseURL string) Provider {
	return Provider{
		ID: id, Name: id, ProviderType: ProviderFamilyOpenAI,
		DefaultWireAPI: APIProtocolResponses, BaseURL: baseURL, APIKey: "sk-" + id,
		AuthHeader: "Authorization", AuthScheme: "Bearer", Enabled: true,
	}
}

func catalogAnthropicConnection(id, baseURL string) Provider {
	return Provider{
		ID: id, Name: id, ProviderType: ProviderFamilyAnthropic,
		DefaultWireAPI: APIProtocolMessages, BaseURL: baseURL, APIKey: "ak-" + id,
		AuthHeader: "x-api-key", Enabled: true,
	}
}

func mustLoadCatalog(t *testing.T, store CatalogStore) *Catalog {
	t.Helper()
	catalog, err := LoadCatalog(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	return catalog
}

func TestLoadCatalogRequiresStore(t *testing.T) {
	if _, err := LoadCatalog(context.Background(), nil); err == nil {
		t.Fatal("LoadCatalog(nil store) error = nil, want error")
	}
}

func TestCatalogResolveSelectionPrecedence(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{
			catalogOpenAIConnection("gateway", "https://gw.example.com"),
			catalogAnthropicConnection("anthropic", "https://api.anthropic.com"),
		},
		bindings: []ProviderModelBinding{
			{ProviderID: "gateway", ModelID: "gpt-5.5"},
			{ProviderID: "anthropic", ModelID: "claude-sonnet-4"},
		},
		defProvider: "gateway", defModel: "gpt-5.5", hasDefault: true,
	})

	cases := []struct {
		name         string
		connectionID string
		model        string
		wantProvider string
		wantWireAPI  string
	}{
		{"explicit connection wins over the model binding", "anthropic", "gpt-5.5", "anthropic", APIProtocolMessages},
		{"model bound to exactly one connection", "", "claude-sonnet-4", "anthropic", APIProtocolMessages},
		{"catalog default names its owning connection", "", "gpt-5.5", "gateway", APIProtocolResponses},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, err := catalog.Resolve(tc.connectionID, tc.model)
			if err != nil {
				t.Fatalf("Resolve(%q, %q) error = %v", tc.connectionID, tc.model, err)
			}
			if target.Provider.ID != tc.wantProvider {
				t.Errorf("provider = %q, want %q", target.Provider.ID, tc.wantProvider)
			}
			if target.WireAPI != tc.wantWireAPI {
				t.Errorf("wire api = %q, want %q", target.WireAPI, tc.wantWireAPI)
			}
			if target.Model.ID != tc.model {
				t.Errorf("model = %q, want the opaque model %q", target.Model.ID, tc.model)
			}
		})
	}
}

func TestCatalogResolveRejectsAmbiguity(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{
			catalogOpenAIConnection("gateway", "https://gw.example.com"),
			catalogOpenAIConnection("backup", "https://backup.example.com"),
		},
		bindings: []ProviderModelBinding{
			{ProviderID: "gateway", ModelID: "shared"},
			{ProviderID: "backup", ModelID: "shared"},
		},
	})
	_, err := catalog.Resolve("", "shared")
	if !errors.Is(err, ErrAmbiguousConnection) {
		t.Fatalf("Resolve error = %v, want ErrAmbiguousConnection", err)
	}
	for _, want := range []string{"gateway", "backup"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name candidate %q", err, want)
		}
	}
}

func TestCatalogResolveErrors(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{catalogOpenAIConnection("gateway", "https://gw.example.com")},
		bindings:  []ProviderModelBinding{{ProviderID: "gateway", ModelID: "gpt-5.5"}},
	})
	cases := []struct {
		name         string
		connectionID string
		model        string
		wantErr      error
	}{
		{"unknown connection", "missing", "gpt-5.5", ErrConnectionNotFound},
		{"empty model", "", "", ErrNoModel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := catalog.Resolve(tc.connectionID, tc.model); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Resolve error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestCatalogResolveUsesSoleConnectionForAnyModel(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{catalogOpenAIConnection("gateway", "https://gw.example.com")},
	})
	target, err := catalog.Resolve("", "vendor/some-totally-unknown/model")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if target.Provider.ID != "gateway" {
		t.Errorf("provider = %q, want gateway", target.Provider.ID)
	}
	if target.Model.ID != "vendor/some-totally-unknown/model" {
		t.Errorf("model = %q, want the literal model string", target.Model.ID)
	}
}

func TestCatalogDiagnosesRetiredQualifiedModelSyntax(t *testing.T) {
	newCatalog := func() *Catalog {
		return mustLoadCatalog(t, fakeCatalogStore{
			providers: []Provider{
				catalogOpenAIConnection("gateway", "https://gw.example.com"),
				catalogAnthropicConnection("anthropic", "https://api.anthropic.com"),
			},
			bindings: []ProviderModelBinding{
				{ProviderID: "gateway", ModelID: "gpt-5.5"},
			},
		})
	}

	t.Run("a qualified model naming its own connection is diagnosed", func(t *testing.T) {
		_, err := newCatalog().Resolve("", "gateway/gpt-5.5")
		if !errors.Is(err, ErrLegacyQualifiedModel) {
			t.Fatalf("Resolve() error = %v, want ErrLegacyQualifiedModel", err)
		}
		// The hint has to be actionable: it names the connection to configure
		// and the model to write instead of the retired syntax.
		for _, want := range []string{"gateway", "gpt-5.5", "use model"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("an opaque model whose prefix is a connection is left alone", func(t *testing.T) {
		// "gateway" is a connection and the model starts with it, but the
		// connection does not serve "some-model", so the slash is part of a
		// legitimate model id and must survive untouched.
		catalog := mustLoadCatalog(t, fakeCatalogStore{
			providers: []Provider{catalogOpenAIConnection("gateway", "https://gw.example.com")},
		})
		target, err := catalog.Resolve("", "gateway/some-model")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if target.Model.ID != "gateway/some-model" {
			t.Errorf("model = %q, want the literal model string", target.Model.ID)
		}
	})

	t.Run("a bound model containing a slash is served", func(t *testing.T) {
		catalog := mustLoadCatalog(t, fakeCatalogStore{
			providers: []Provider{catalogOpenAIConnection("gateway", "https://gw.example.com")},
			bindings: []ProviderModelBinding{
				{ProviderID: "gateway", ModelID: "meta-llama/Llama-3.1-8B"},
			},
		})
		target, err := catalog.Resolve("", "meta-llama/Llama-3.1-8B")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if target.Model.ID != "meta-llama/Llama-3.1-8B" {
			t.Errorf("model = %q, want the literal model string", target.Model.ID)
		}
	})
}

func TestCatalogIgnoresBindingsOfDisabledConnections(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{catalogOpenAIConnection("gateway", "https://gw.example.com")},
		bindings: []ProviderModelBinding{
			{ProviderID: "gateway", ModelID: "shared"},
			{ProviderID: "retired", ModelID: "shared"},
		},
	})
	target, err := catalog.Resolve("", "shared")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if target.Provider.ID != "gateway" {
		t.Errorf("provider = %q, want gateway", target.Provider.ID)
	}
}

func TestCatalogResolveAppliesPerModelOverrides(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{catalogOpenAIConnection("gateway", "https://gw.example.com")},
		bindings: []ProviderModelBinding{{
			ProviderID: "gateway", ModelID: "gpt-5.5",
			Config: ProviderModelConfig{
				WireAPI:         APIProtocolChatCompletions,
				BaseURL:         "https://chat-only.example.com",
				HeadersJSON:     `{"X-Tenant":"acme"}`,
				MaxOutputTokens: 4096,
			},
		}},
	})
	target, err := catalog.Resolve("gateway", "gpt-5.5")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if target.WireAPI != APIProtocolChatCompletions {
		t.Errorf("wire api = %q, want %q", target.WireAPI, APIProtocolChatCompletions)
	}
	if target.Endpoint != "https://chat-only.example.com/v1/chat/completions" {
		t.Errorf("endpoint = %q", target.Endpoint)
	}
	if got := target.Headers.Get("X-Tenant"); got != "acme" {
		t.Errorf("X-Tenant header = %q, want acme", got)
	}
	if target.MaxOutputTokens != 4096 {
		t.Errorf("max output tokens = %d, want 4096", target.MaxOutputTokens)
	}
}

func TestCatalogResolveAnthropicEndpoint(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{catalogAnthropicConnection("anthropic", "https://api.anthropic.com")},
	})
	target, err := catalog.Resolve("anthropic", "claude-sonnet-4")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if target.Endpoint != "https://api.anthropic.com/v1/messages" {
		t.Errorf("endpoint = %q", target.Endpoint)
	}
}

func TestCatalogDefaultModelFallsBackToModelFlag(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{catalogOpenAIConnection("env", "https://env.example.com")},
		models:    []Model{{ID: "gpt-5.5", Name: "gpt-5.5", DefaultModel: true, Enabled: true}},
		bindings:  []ProviderModelBinding{{ProviderID: "env", ModelID: "gpt-5.5"}},
	})
	if got := catalog.DefaultModel(); got != "gpt-5.5" {
		t.Fatalf("DefaultModel() = %q, want gpt-5.5", got)
	}
	target, err := catalog.Resolve("", "gpt-5.5")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if target.Provider.ID != "env" {
		t.Errorf("provider = %q, want env", target.Provider.ID)
	}
}

func TestCatalogSelectModelPrefersAgentThenDefault(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers:   []Provider{catalogOpenAIConnection("env", "https://env.example.com")},
		defProvider: "env", defModel: "gpt-5.5", hasDefault: true,
	})
	got, err := catalog.SelectModel("claude-sonnet-4")
	if err != nil || got != "claude-sonnet-4" {
		t.Fatalf("SelectModel(agent model) = %q, %v; want claude-sonnet-4, nil", got, err)
	}
	got, err = catalog.SelectModel("   ")
	if err != nil || got != "gpt-5.5" {
		t.Fatalf("SelectModel(empty) = %q, %v; want gpt-5.5, nil", got, err)
	}

	empty := mustLoadCatalog(t, fakeCatalogStore{})
	if _, err := empty.SelectModel(""); !errors.Is(err, ErrNoModel) {
		t.Fatalf("SelectModel(empty catalog) error = %v, want ErrNoModel", err)
	}
}

func TestCatalogConnectionsAreStable(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{
			catalogOpenAIConnection("zeta", "https://z.example.com"),
			catalogOpenAIConnection("alpha", "https://a.example.com"),
		},
	})
	got := catalog.Connections()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Fatalf("Connections() = %v, want [alpha zeta]", got)
	}
}
