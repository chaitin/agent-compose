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

func catalogChatConnection(id, baseURL string) Provider {
	return Provider{
		ID: id, Name: id, ProviderType: ProviderFamilyOpenAI,
		DefaultWireAPI: APIProtocolChatCompletions, BaseURL: baseURL, APIKey: "sk-" + id,
		AuthHeader: "Authorization", AuthScheme: "Bearer", Enabled: true,
	}
}

func mustLoadCatalog(t *testing.T, store CatalogStore, options ...CatalogOption) *Catalog {
	t.Helper()
	catalog, err := LoadCatalog(context.Background(), store, options...)
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
			target, err := catalog.Resolve(tc.connectionID, tc.model, nil)
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

// TestCatalogResolvePrefersTheCallersProtocol pins the affinity rule: a model
// several connections serve resolves to the connection the caller speaks
// natively, so the run is a passthrough instead of a conversion.
func TestCatalogResolvePrefersTheCallersProtocol(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{
			catalogOpenAIConnection("shared-responses", "https://responses.example.com"),
			catalogChatConnection("shared-chat", "https://chat.example.com"),
			catalogAnthropicConnection("shared-messages", "https://messages.example.com"),
		},
		bindings: []ProviderModelBinding{
			{ProviderID: "shared-responses", ModelID: "shared-model"},
			{ProviderID: "shared-chat", ModelID: "shared-model"},
			{ProviderID: "shared-messages", ModelID: "shared-model"},
		},
	})
	cases := []struct {
		agent        string
		wantProvider string
	}{
		{"codex", "shared-responses"},
		{"claude", "shared-messages"},
		{"opencode", "shared-chat"},
		{"pi", "shared-responses"},
		{"dsh", "shared-responses"},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			dialect, err := DialectFor(tc.agent)
			if err != nil {
				t.Fatalf("DialectFor(%q) error = %v", tc.agent, err)
			}
			target, err := catalog.Resolve("", "shared-model", dialect.PreferredProtocols())
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if target.Provider.ID != tc.wantProvider {
				t.Fatalf("provider = %q, want %q", target.Provider.ID, tc.wantProvider)
			}
			upstream := NormalizeProtocol(target.WireAPI)
			if inbound := dialect.InboundProtocol(upstream); inbound != upstream {
				t.Errorf("%s resolved over %s but was served %s, want a passthrough", tc.agent, upstream, inbound)
			}
		})
	}
}

// TestCatalogResolveFallsBackToConversion pins that a dialect whose native
// protocols no connection offers still resolves: converting is better than
// refusing to run, and the conversion order is stable.
func TestCatalogResolveFallsBackToConversion(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{
			catalogChatConnection("shared-chat", "https://chat.example.com"),
			catalogAnthropicConnection("shared-messages", "https://messages.example.com"),
		},
		bindings: []ProviderModelBinding{
			{ProviderID: "shared-chat", ModelID: "shared-model"},
			{ProviderID: "shared-messages", ModelID: "shared-model"},
		},
	})
	codex, err := DialectFor("codex")
	if err != nil {
		t.Fatalf("DialectFor(codex) error = %v", err)
	}
	target, err := catalog.Resolve("", "shared-model", codex.PreferredProtocols())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	// Codex speaks only responses, so both candidates are conversions; chat
	// completions precedes messages in the conversion order.
	if target.Provider.ID != "shared-chat" {
		t.Fatalf("provider = %q, want shared-chat", target.Provider.ID)
	}
	if !codex.NeedsConversion(NormalizeProtocol(target.WireAPI)) {
		t.Fatalf("wire api = %q, want a conversion for codex", target.WireAPI)
	}
}

// TestCatalogResolveRanksTheEffectivePerModelProtocol pins that affinity ranks
// the protocol a run would really use, not the connection's own default.
func TestCatalogResolveRanksTheEffectivePerModelProtocol(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{
			catalogOpenAIConnection("gateway", "https://gateway.example.com"),
			catalogChatConnection("baizhi", "https://baizhi.example.com"),
		},
		bindings: []ProviderModelBinding{
			{ProviderID: "gateway", ModelID: "shared-model", Config: ProviderModelConfig{WireAPI: APIProtocolMessages}},
			{ProviderID: "baizhi", ModelID: "shared-model"},
		},
	})
	claude, err := DialectFor("claude")
	if err != nil {
		t.Fatalf("DialectFor(claude) error = %v", err)
	}
	target, err := catalog.Resolve("", "shared-model", claude.PreferredProtocols())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	// The gateway connection is configured for responses, but its binding serves
	// this model over messages, which is what claude can speak natively.
	if target.Provider.ID != "gateway" || target.WireAPI != APIProtocolMessages {
		t.Fatalf("target = %q over %q, want gateway over %s", target.Provider.ID, target.WireAPI, APIProtocolMessages)
	}
}

// TestCatalogResolveBreaksTiesRandomly pins the tie-break: connections serving a
// model over the same protocol are interchangeable, so the catalog asks its
// chooser and honours the answer.
func TestCatalogResolveBreaksTiesRandomly(t *testing.T) {
	store := fakeCatalogStore{
		providers: []Provider{
			catalogOpenAIConnection("gateway", "https://gateway.example.com"),
			catalogOpenAIConnection("backup", "https://backup.example.com"),
		},
		bindings: []ProviderModelBinding{
			{ProviderID: "gateway", ModelID: "shared"},
			{ProviderID: "backup", ModelID: "shared"},
		},
	}
	var offered []int
	pickWith := func(index int) Provider {
		t.Helper()
		catalog := mustLoadCatalog(t, store, WithConnectionChooser(func(candidates int) int {
			offered = append(offered, candidates)
			return index
		}))
		target, err := catalog.Resolve("", "shared", nil)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		return target.Provider
	}
	first, second := pickWith(0), pickWith(1)
	if first.ID == second.ID {
		t.Fatalf("chooser index 0 and 1 both picked %q, want the chooser to decide between the candidates", first.ID)
	}
	for _, provider := range []Provider{first, second} {
		if provider.ID != "gateway" && provider.ID != "backup" {
			t.Fatalf("provider = %q, want one of the equivalent connections", provider.ID)
		}
	}
	if len(offered) != 2 || offered[0] != 2 || offered[1] != 2 {
		t.Fatalf("chooser candidate counts = %v, want [2 2]: both equivalent connections must be offered", offered)
	}

	// Without an injected chooser the pick stays random, but it must remain a
	// configured connection that serves the model rather than a zero target.
	random := mustLoadCatalog(t, store)
	for range 20 {
		target, err := random.Resolve("", "shared", nil)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if target.Provider.ID != "gateway" && target.Provider.ID != "backup" {
			t.Fatalf("provider = %q, want one of the equivalent connections", target.Provider.ID)
		}
	}
}

// TestCatalogResolveTreatsEveryConnectionAsACandidateForAnUnboundModel pins
// that an opaque model no connection declares still resolves: a connection that
// never claimed the model may still serve it, and the preference decides.
func TestCatalogResolveTreatsEveryConnectionAsACandidateForAnUnboundModel(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{
			catalogOpenAIConnection("gateway", "https://gateway.example.com"),
			catalogChatConnection("baizhi", "https://baizhi.example.com"),
		},
	})
	opencode, err := DialectFor("opencode")
	if err != nil {
		t.Fatalf("DialectFor(opencode) error = %v", err)
	}
	target, err := catalog.Resolve("", "vendor/unknown-model", opencode.PreferredProtocols())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if target.Provider.ID != "baizhi" {
		t.Fatalf("provider = %q, want the chat-completions connection baizhi", target.Provider.ID)
	}
	if target.Model.ID != "vendor/unknown-model" {
		t.Fatalf("model = %q, want the opaque model string", target.Model.ID)
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
			if _, err := catalog.Resolve(tc.connectionID, tc.model, nil); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Resolve error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestCatalogResolveUsesSoleConnectionForAnyModel(t *testing.T) {
	catalog := mustLoadCatalog(t, fakeCatalogStore{
		providers: []Provider{catalogOpenAIConnection("gateway", "https://gw.example.com")},
	})
	target, err := catalog.Resolve("", "vendor/some-totally-unknown/model", nil)
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
		_, err := newCatalog().Resolve("", "gateway/gpt-5.5", nil)
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
		target, err := catalog.Resolve("", "gateway/some-model", nil)
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
		target, err := catalog.Resolve("", "meta-llama/Llama-3.1-8B", nil)
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
	target, err := catalog.Resolve("", "shared", nil)
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
	target, err := catalog.Resolve("gateway", "gpt-5.5", nil)
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
	target, err := catalog.Resolve("anthropic", "claude-sonnet-4", nil)
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
	target, err := catalog.Resolve("", "gpt-5.5", nil)
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
