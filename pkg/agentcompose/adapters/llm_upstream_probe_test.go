package adapters

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/llms"
)

func TestLLMUpstreamProberSkipsEverythingWhenDisabled(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	store := &probeCatalogStub{providers: []llms.Provider{probeUpstreamProvider(server.URL)}}
	prober := NewLLMUpstreamProber(&appconfig.Config{LLMUpstreamProbe: false}, discardProbeLogger())
	prober.ProbeConnection(context.Background(), probeUpstreamProvider(server.URL), "")
	prober.ProbeConfigured(context.Background(), store)

	if requests.Load() != 0 {
		t.Fatalf("disabled prober issued %d requests", requests.Load())
	}
	if store.listCalls != 0 {
		t.Fatalf("disabled prober read the catalog %d times", store.listCalls)
	}
}

func TestLLMUpstreamProberProbesConfiguredConnectionsAndSingleWrites(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"served-model"}]}`))
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := probeUpstreamProvider(server.URL)
	store := &probeCatalogStub{providers: []llms.Provider{provider}}
	prober := NewLLMUpstreamProber(&appconfig.Config{LLMUpstreamProbe: true, LLMProbeTimeout: 5 * time.Second}, discardProbeLogger())
	prober.ProbeConfigured(context.Background(), store)
	if store.listCalls == 0 {
		t.Fatal("enabled prober did not read the catalog")
	}
	prober.ProbeConnection(context.Background(), provider, "served-model")
}

func TestNewLLMUpstreamProberHandlesMissingConfig(t *testing.T) {
	prober := NewLLMUpstreamProber(nil, nil)
	// A prober built without configuration must not panic or probe.
	prober.ProbeConnection(context.Background(), probeUpstreamProvider("https://gateway.example/v1"), "")
	if prober.enabled {
		t.Fatal("a prober built without configuration must stay disabled")
	}
}

type probeCatalogStub struct {
	providers []llms.Provider
	bindings  []llms.ProviderModelBinding
	listCalls int
}

func (s *probeCatalogStub) ListEnabledLLMProviders(context.Context) ([]llms.Provider, error) {
	s.listCalls++
	return s.providers, nil
}

func (s *probeCatalogStub) ListEnabledLLMModels(context.Context) ([]llms.Model, error) {
	return nil, nil
}

func (s *probeCatalogStub) ListLLMProviderModelConfigs(context.Context) ([]llms.ProviderModelBinding, error) {
	return s.bindings, nil
}

func (s *probeCatalogStub) DefaultLLMModelReference(context.Context) (string, string, bool, error) {
	return "", "", false, nil
}

func probeUpstreamProvider(baseURL string) llms.Provider {
	return llms.Provider{
		ID: "gateway", Name: "gateway", ProviderType: llms.ProviderFamilyOpenAI,
		DefaultWireAPI: llms.APIProtocolResponses, BaseURL: baseURL + "/v1", APIKey: "key",
		AuthHeader: "Authorization", AuthScheme: "Bearer", Enabled: true, Scope: llms.ProviderScopeCatalog,
	}
}

func discardProbeLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
