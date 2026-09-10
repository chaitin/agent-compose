package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/chaitin/agent-compose/pkg/agentcompose/adapters"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
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
		response, err := client.Generate(ctx, connect.NewRequest(&agentcomposev2.GenerateLLMRequest{Prompt: "hello", Model: "live/literal-model"}))
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
	if _, err := client.Generate(ctx, connect.NewRequest(&agentcomposev2.GenerateLLMRequest{Prompt: "hello", Model: "live/literal-model"})); err == nil {
		t.Fatal("disabled provider accepted generation")
	}
	select {
	case <-requests:
		t.Fatal("disabled provider contacted upstream")
	default:
	}
}
