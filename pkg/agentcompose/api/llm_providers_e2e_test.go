package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/chaitin/agent-compose/pkg/agentcompose/adapters"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/llms"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestE2ELLMProviderLiveConfiguration(t *testing.T) {
	db, err := storagesqlite.Open(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store := configstore.FromDB(db.DB())
	type upstreamRequest struct{ path, auth, model string }
	requests := make(chan upstreamRequest, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
			http.Error(w, "bad request", 400)
			return
		}
		requests <- upstreamRequest{path: r.URL.Path, auth: r.Header.Get("Authorization"), model: body.Model}
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprint(w, `{"id":"completion-1","model":"literal-model","choices":[{"index":0,"message":{"role":"assistant","content":"works"},"finish_reason":"stop"}]}`); err != nil {
			t.Errorf("write upstream response: %v", err)
		}
	}))
	t.Cleanup(upstream.Close)
	service := api.NewLLMHandler(adapters.NewLLMClient(nil, store), store)
	path, handler := agentcomposev2connect.NewLLMServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := agentcomposev2connect.NewLLMServiceClient(server.Client(), server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	spec := &agentcomposev2.LLMProviderSpec{Id: "live", BaseUrl: upstream.URL + "/first/v1", Protocol: "chat_completions", ApiKey: proto.String("initial-key")}
	if _, err := client.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: spec})); err != nil {
		t.Fatal(err)
	}
	generate := func(expectedPath, expectedAuth string) {
		t.Helper()
		response, err := client.Generate(ctx, connect.NewRequest(&agentcomposev2.GenerateLLMRequest{Prompt: "hello", Model: "literal-model"}))
		if err != nil {
			t.Fatal(err)
		}
		if response.Msg.Text != "works" || response.Msg.Model != "literal-model" {
			t.Fatalf("unexpected model response: %v", response.Msg)
		}
		select {
		case request := <-requests:
			if request.path != expectedPath || request.auth != expectedAuth || request.model != "literal-model" {
				t.Fatal("upstream URL, credential or model did not match current provider configuration")
			}
		case <-ctx.Done():
			t.Fatal("upstream did not receive model request")
		}
	}
	generate("/first/v1/chat/completions", "Bearer initial-key")
	spec.BaseUrl = upstream.URL + "/second/v1"
	spec.ApiKey = proto.String("rotated-key")
	if _, err := client.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{Provider: spec})); err != nil {
		t.Fatal(err)
	}
	generate("/second/v1/chat/completions", "Bearer rotated-key")
	spec.Enabled = proto.Bool(false)
	spec.ApiKey = nil
	if _, err := client.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{Provider: spec})); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Generate(ctx, connect.NewRequest(&agentcomposev2.GenerateLLMRequest{Prompt: "hello", Model: "literal-model"})); err == nil {
		t.Fatal("disabled provider accepted generation")
	}
	select {
	case <-requests:
		t.Fatal("disabled provider contacted upstream")
	default:
	}
}

func TestE2ELLMProviderAnthropicMessagesLiveConfiguration(t *testing.T) {
	db, err := storagesqlite.Open(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store := configstore.FromDB(db.DB())
	type upstreamRequest struct{ path, version, apiKey, model string }
	requests := make(chan upstreamRequest, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
			http.Error(w, "bad request", 400)
			return
		}
		requests <- upstreamRequest{path: r.URL.Path, version: r.Header.Get("anthropic-version"), apiKey: r.Header.Get("x-api-key"), model: body.Model}
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprint(w, `{"id":"msg-1","model":"claude","stop_reason":"end_turn","content":[{"type":"text","text":"works"}]}`); err != nil {
			t.Errorf("write upstream response: %v", err)
		}
	}))
	t.Cleanup(upstream.Close)
	service := api.NewLLMHandler(adapters.NewLLMClient(nil, store), store)
	path, handler := agentcomposev2connect.NewLLMServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := agentcomposev2connect.NewLLMServiceClient(server.Client(), server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	spec := &agentcomposev2.LLMProviderSpec{Id: "claude", BaseUrl: upstream.URL + "/v1", Protocol: "anthropic_messages", ApiKey: proto.String("anthropic-key")}
	if _, err := client.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: spec})); err != nil {
		t.Fatal(err)
	}
	response, err := client.Generate(ctx, connect.NewRequest(&agentcomposev2.GenerateLLMRequest{Prompt: "hello", Model: "literal-model"}))
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.Text != "works" {
		t.Fatalf("unexpected model response: %v", response.Msg)
	}
	select {
	case request := <-requests:
		if request.path != "/v1/messages" || request.version != "2023-06-01" || request.apiKey != "anthropic-key" || request.model != "literal-model" {
			t.Fatalf("anthropic upstream request = %#v", request)
		}
	case <-ctx.Done():
		t.Fatal("upstream did not receive model request")
	}
}

// An operator declares the models a connection serves and then reads back what
// the daemon learned about the endpoint, so the UI never has to guess either.
func TestE2ELLMProviderDeclaredModelsAndProbeCapabilities(t *testing.T) {
	db, err := storagesqlite.Open(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store := configstore.FromDB(db.DB())
	var mu sync.Mutex
	probedModels := map[string]string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			if _, err := fmt.Fprint(w, `{"object":"list","data":[{"id":"advertised-model"}]}`); err != nil {
				t.Errorf("write model list: %v", err)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode probe request: %v", err)
			}
			mu.Lock()
			probedModels[r.URL.Path] = body.Model
			mu.Unlock()
			if _, err := fmt.Fprint(w, `{"object":"response","output":[{"type":"message"}]}`); err != nil {
				t.Errorf("write probe response: %v", err)
			}
		default:
			// Any other protocol is genuinely absent from this endpoint.
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	prober := adapters.NewLLMUpstreamProber(
		&appconfig.Config{LLMUpstreamProbe: true, LLMProbeTimeout: 5 * time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	service := api.NewLLMHandler(adapters.NewLLMClient(nil, store), store).WithUpstreamProbe(prober)
	path, handler := agentcomposev2connect.NewLLMServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := agentcomposev2connect.NewLLMServiceClient(server.Client(), server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	created, err := client.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{
		Provider: &agentcomposev2.LLMProviderSpec{
			Id: "declared", BaseUrl: upstream.URL + "/v1", Protocol: "responses", ApiKey: proto.String("declared-key"),
			Models: &agentcomposev2.LLMProviderModels{Models: []*agentcomposev2.LLMModelSpec{
				{Id: "alpha-model", Name: "Alpha", Protocol: "chat_completions", MaxOutputTokens: proto.Int32(2048)},
				{Id: "beta-model"},
			}},
		},
	}))
	if err != nil {
		t.Fatalf("create provider with declared models: %v", err)
	}
	models := created.Msg.GetProvider().GetModels()
	if len(models) != 2 || models[0].GetId() != "alpha-model" || models[1].GetId() != "beta-model" {
		t.Fatalf("declared models = %v, want both ordered by model ID", models)
	}
	if models[0].GetProtocol() != "chat_completions" || models[0].GetMaxOutputTokens() != 2048 || models[0].GetName() != "Alpha" {
		t.Fatalf("declared override = %v", models[0])
	}
	// An unset model protocol inherits the connection protocol, which the client
	// reads as unset rather than as a fabricated protocol.
	if models[1].GetProtocol() != "" || models[1].GetMaxOutputTokens() != 0 {
		t.Fatalf("declared inherited model = %v", models[1])
	}

	// The write probed synchronously with a declared model instead of depending
	// on what the endpoint advertises.
	mu.Lock()
	probedModel := probedModels["/v1/responses"]
	mu.Unlock()
	if probedModel != "alpha-model" {
		t.Fatalf("probe model = %q, want the first declared model", probedModel)
	}
	capabilities := created.Msg.GetProvider().GetCapabilities()
	if capabilities == nil || capabilities.GetProbedModel() != "alpha-model" {
		t.Fatalf("capabilities after a write = %v", capabilities)
	}
	if capabilities.GetProbedAt() == nil {
		t.Fatal("capabilities did not report when the probe ran")
	}
	outcomes := map[string]string{}
	for _, probe := range capabilities.GetProbes() {
		outcomes[probe.GetProtocol()] = probe.GetOutcome()
	}
	if outcomes["responses"] != string(llms.ProbeSupported) {
		t.Fatalf("responses outcome = %q, want supported", outcomes["responses"])
	}
	if outcomes["chat_completions"] != string(llms.ProbeUnsupported) {
		t.Fatalf("chat_completions outcome = %q, want a proven absence", outcomes["chat_completions"])
	}
	if models := capabilities.GetModels(); len(models) != 1 || models[0] != "advertised-model" {
		t.Fatalf("advertised models = %v", models)
	}

	// Reading the connection reports the same declared set and the cached verdict.
	read, err := client.GetProvider(ctx, connect.NewRequest(&agentcomposev2.GetProviderRequest{Id: "declared"}))
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if len(read.Msg.GetProvider().GetModels()) != 2 || read.Msg.GetProvider().GetCapabilities().GetProbedModel() != "alpha-model" {
		t.Fatalf("read provider = %v", read.Msg.GetProvider())
	}

	listed, err := client.ListProviders(ctx, connect.NewRequest(&agentcomposev2.ListProvidersRequest{}))
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	if len(listed.Msg.GetProviders()) != 1 || len(listed.Msg.GetProviders()[0].GetModels()) != 2 {
		t.Fatalf("listed providers = %v", listed.Msg.GetProviders())
	}
}

// Choosing the default model is a service operation, and a protocol-family move
// that would strand a declared model is refused instead of silently rerouted.
func TestE2ELLMDefaultModelAndProtocolFamilyGuard(t *testing.T) {
	db, err := storagesqlite.Open(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store := configstore.FromDB(db.DB())
	service := api.NewLLMHandler(adapters.NewLLMClient(nil, store), store)
	path, handler := agentcomposev2connect.NewLLMServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := agentcomposev2connect.NewLLMServiceClient(server.Client(), server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := client.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{
		Provider: &agentcomposev2.LLMProviderSpec{
			Id: "gateway", BaseUrl: "https://gateway.example/v1", Protocol: "responses", ApiKey: proto.String("gateway-key"),
		},
	})); err != nil {
		t.Fatalf("create provider: %v", err)
	}

	empty, err := client.GetDefaultModel(ctx, connect.NewRequest(&agentcomposev2.GetDefaultModelRequest{}))
	if err != nil {
		t.Fatalf("get default model: %v", err)
	}
	if empty.Msg.GetModel() != nil {
		t.Fatalf("default model before a write = %v, want absent", empty.Msg.GetModel())
	}

	reference := &agentcomposev2.LLMModelReference{ProviderId: "gateway", ModelId: "literal-default"}
	set, err := client.SetDefaultModel(ctx, connect.NewRequest(&agentcomposev2.SetDefaultModelRequest{Model: reference}))
	if err != nil {
		t.Fatalf("set default model: %v", err)
	}
	if set.Msg.GetModel().GetProviderId() != "gateway" || set.Msg.GetModel().GetModelId() != "literal-default" {
		t.Fatalf("set default model = %v", set.Msg.GetModel())
	}
	read, err := client.GetDefaultModel(ctx, connect.NewRequest(&agentcomposev2.GetDefaultModelRequest{}))
	if err != nil {
		t.Fatalf("get default model after a write: %v", err)
	}
	if read.Msg.GetModel().GetModelId() != "literal-default" {
		t.Fatalf("stored default model = %v", read.Msg.GetModel())
	}

	// An incomplete reference is rejected and must not replace the stored one.
	if _, err := client.SetDefaultModel(ctx, connect.NewRequest(&agentcomposev2.SetDefaultModelRequest{
		Model: &agentcomposev2.LLMModelReference{ProviderId: "gateway"},
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("set an incomplete default model error = %v, want invalid argument", err)
	}
	kept, err := client.GetDefaultModel(ctx, connect.NewRequest(&agentcomposev2.GetDefaultModelRequest{}))
	if err != nil {
		t.Fatalf("get default model after a rejected write: %v", err)
	}
	if kept.Msg.GetModel().GetModelId() != "literal-default" {
		t.Fatalf("a rejected write changed the default model: %v", kept.Msg.GetModel())
	}

	// A model may pin its own protocol, so moving the connection to the other
	// family while keeping it is refused rather than resolved incoherently.
	if _, err := client.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{
		Provider: &agentcomposev2.LLMProviderSpec{
			Id: "pinned", BaseUrl: "https://pinned.example/v1", Protocol: "responses", ApiKey: proto.String("pinned-key"),
			Models: &agentcomposev2.LLMProviderModels{Models: []*agentcomposev2.LLMModelSpec{
				{Id: "pinned-model", Protocol: "chat_completions"},
			}},
		},
	})); err != nil {
		t.Fatalf("create provider with a pinned model: %v", err)
	}
	_, err = client.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{
		Provider: &agentcomposev2.LLMProviderSpec{Id: "pinned", Protocol: "anthropic_messages"},
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("cross-family update error = %v, want invalid argument", err)
	}
	unchanged, err := client.GetProvider(ctx, connect.NewRequest(&agentcomposev2.GetProviderRequest{Id: "pinned"}))
	if err != nil {
		t.Fatalf("get provider after a rejected update: %v", err)
	}
	if unchanged.Msg.GetProvider().GetProtocol() != "responses" || len(unchanged.Msg.GetProvider().GetModels()) != 1 {
		t.Fatalf("a rejected update changed the provider: %v", unchanged.Msg.GetProvider())
	}

	// Restating the set states the new intent.
	moved, err := client.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{
		Provider: &agentcomposev2.LLMProviderSpec{
			Id: "pinned", Protocol: "anthropic_messages",
			Models: &agentcomposev2.LLMProviderModels{Models: []*agentcomposev2.LLMModelSpec{{Id: "claude-model"}}},
		},
	}))
	if err != nil {
		t.Fatalf("cross-family update restating models: %v", err)
	}
	if moved.Msg.GetProvider().GetProtocol() != "anthropic_messages" || moved.Msg.GetProvider().GetModels()[0].GetId() != "claude-model" {
		t.Fatalf("restated provider = %v", moved.Msg.GetProvider())
	}

	if _, err := client.ClearDefaultModel(ctx, connect.NewRequest(&agentcomposev2.ClearDefaultModelRequest{})); err != nil {
		t.Fatalf("clear default model: %v", err)
	}
	cleared, err := client.GetDefaultModel(ctx, connect.NewRequest(&agentcomposev2.GetDefaultModelRequest{}))
	if err != nil {
		t.Fatalf("get default model after clearing: %v", err)
	}
	if cleared.Msg.GetModel() != nil {
		t.Fatalf("default model after clearing = %v, want absent", cleared.Msg.GetModel())
	}
}
