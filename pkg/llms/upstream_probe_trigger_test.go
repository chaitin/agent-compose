package llms

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestProbeModelIndexPrefersDefaultModel(t *testing.T) {
	bindings := []ProviderModelBinding{
		{ProviderID: "a", ModelID: "zeta"},
		{ProviderID: "a", ModelID: "alpha"},
		{ProviderID: "b", ModelID: "only"},
	}

	withDefault := probeModelIndex(bindings, "a", "default-model", true)
	if want := map[string]string{"a": "default-model", "b": "only"}; !reflect.DeepEqual(withDefault, want) {
		t.Fatalf("with default = %v, want %v", withDefault, want)
	}

	withoutDefault := probeModelIndex(bindings, "", "", false)
	if want := map[string]string{"a": "alpha", "b": "only"}; !reflect.DeepEqual(withoutDefault, want) {
		t.Fatalf("without default = %v, want %v", withoutDefault, want)
	}
}

func TestProbeConfiguredUpstreamsProbesEveryConnection(t *testing.T) {
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

	store := fakeCatalogStore{
		providers:   []Provider{catalogOpenAIConnection("a", server.URL+"/v1"), catalogOpenAIConnection("b", server.URL+"/v1")},
		bindings:    []ProviderModelBinding{{ProviderID: "a", ModelID: "served-model"}},
		defProvider: "a",
		defModel:    "served-model",
		hasDefault:  true,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := ProbeConfiguredUpstreams(context.Background(), logger, NewUpstreamProber(server.Client()), store); err != nil {
		t.Fatalf("ProbeConfiguredUpstreams: %v", err)
	}
	// Each connection gets one inventory request plus one request per protocol
	// its family can serve.
	if got, want := requests.Load(), int64(2*(1+2)); got != want {
		t.Fatalf("probe requests = %d, want %d", got, want)
	}
}
