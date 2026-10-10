package llms

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpstreamProbeCacheExpiresVerdicts(t *testing.T) {
	current := time.Unix(0, 0)
	cache := NewUpstreamProbeCache(10 * time.Minute)
	cache.now = func() time.Time { return current }
	cache.Store("gateway", UpstreamProbeResult{Endpoint: "https://gateway.example/v1", Model: "m"})

	if _, ok := cache.Lookup("gateway"); !ok {
		t.Fatal("a fresh verdict was not returned")
	}
	current = current.Add(10 * time.Minute)
	if _, ok := cache.Lookup("gateway"); ok {
		t.Fatal("an expired verdict was still returned")
	}
	// The expired entry is dropped, so a later store starts clean.
	cache.Store("gateway", UpstreamProbeResult{Endpoint: "https://gateway.example/v1"})
	if _, ok := cache.Lookup("gateway"); !ok {
		t.Fatal("a verdict stored after expiry was not returned")
	}
}

func TestUpstreamProberReusesAFreshVerdict(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/v1/models":
			writeProbeJSON(w, `{"object":"list","data":[{"id":"served-model"}]}`)
		case "/v1/chat/completions":
			writeProbeJSON(w, `{"object":"chat.completion","choices":[{"index":0}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := probeTestProvider(server.URL + "/v1")
	prober := NewUpstreamProber(server.Client())
	first, err := prober.Probe(context.Background(), UpstreamProbeRequest{Provider: provider, Model: "served-model"})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if first.Cached {
		t.Fatal("the first probe reported a cached verdict")
	}
	afterFirst := requests.Load()

	second, err := prober.Probe(context.Background(), UpstreamProbeRequest{Provider: provider, Model: "served-model"})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !second.Cached {
		t.Fatal("the repeated probe did not report a cached verdict")
	}
	if second.Supports(ProtocolChatCompletions) != first.Supports(ProtocolChatCompletions) {
		t.Fatal("the cached verdict differs from the probed verdict")
	}
	if requests.Load() != afterFirst {
		t.Fatalf("the repeated probe issued %d extra requests", requests.Load()-afterFirst)
	}

	looked, ok := prober.LookupUpstreamProbe(provider)
	if !ok || looked.Model != "served-model" {
		t.Fatalf("LookupUpstreamProbe = %#v/%v", looked, ok)
	}
}

func TestUpstreamProberDoesNotCacheAVerdictItsBudgetEnded(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/v1/models":
			writeProbeJSON(w, `{"object":"list","data":[{"id":"served-model"}]}`)
		case "/v1/responses":
			writeProbeJSON(w, `{"object":"response","output":[{"type":"message"}]}`)
		case "/v1/chat/completions":
			writeProbeJSON(w, `{"object":"chat.completion","choices":[{"index":0}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := probeTestProvider(server.URL + "/v1")
	prober := NewUpstreamProber(server.Client())

	ended, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := prober.Probe(ended, UpstreamProbeRequest{Provider: provider, Model: "served-model"})
	if err != nil {
		t.Fatalf("Probe with an ended budget: %v", err)
	}
	if result.Proven() {
		t.Fatalf("a probe with an ended budget proved support: %#v", result.Protocols)
	}
	if requests.Load() != 0 {
		t.Fatalf("a probe with an ended budget issued %d requests", requests.Load())
	}
	if _, ok := prober.LookupUpstreamProbe(provider); ok {
		t.Fatal("a verdict the budget prevented was cached")
	}

	// The next trigger must reach the endpoint instead of reusing a verdict that
	// was never observed.
	fresh, err := prober.Probe(context.Background(), UpstreamProbeRequest{Provider: provider, Model: "served-model"})
	if err != nil {
		t.Fatalf("Probe after an ended budget: %v", err)
	}
	if fresh.Cached {
		t.Fatal("the retry reused the verdict of a probe that never ran")
	}
	if !fresh.Supports(ProtocolResponses) || !fresh.Supports(ProtocolChatCompletions) {
		t.Fatalf("the retry did not probe the endpoint: %#v", fresh.Protocols)
	}
}

func TestUpstreamProbeKeyFollowsEndpointAndCredential(t *testing.T) {
	provider := probeTestProvider("https://gateway.example/v1")
	base := UpstreamProbeKey(provider)

	rotated := provider
	rotated.APIKey = "rotated-key"
	if UpstreamProbeKey(rotated) == base {
		t.Fatal("a rotated credential reused the cache entry")
	}

	moved := provider
	moved.BaseURL = "https://other.example/v1"
	if UpstreamProbeKey(moved) == base {
		t.Fatal("a moved endpoint reused the cache entry")
	}

	republished := provider
	republished.Name = "renamed"
	if UpstreamProbeKey(republished) != base {
		t.Fatal("a renamed connection changed the probe identity")
	}
}
