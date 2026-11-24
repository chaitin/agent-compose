package api_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/chaitin/agent-compose/pkg/agentcompose/adapters"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	"github.com/chaitin/agent-compose/pkg/llms"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"google.golang.org/protobuf/proto"
)

func TestProviderModelContextWindowRoundTrip(t *testing.T) {
	db, err := storagesqlite.Open(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := configstore.FromDB(db.DB())
	handler := api.NewLLMHandler(adapters.NewLLMClient(nil, store), store)
	ctx := context.Background()
	for id, window := range map[string]int32{"gateway-a": 32_000, "gateway-b": 128_000, "gateway-default": 0} {
		created, err := handler.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{
			Provider: &agentcomposev2.LLMProviderSpec{
				Id: id, BaseUrl: "https://llm.example/v1", Protocol: "responses", ApiKey: proto.String("test-key"),
				Models: &agentcomposev2.LLMProviderModels{Models: []*agentcomposev2.LLMModelSpec{{
					Id: "shared-model", ContextWindow: proto.Int32(window), MaxOutputTokens: proto.Int32(2048),
				}}},
			},
		}))
		if err != nil {
			t.Fatal(err)
		}
		models := created.Msg.GetProvider().GetModels()
		if len(models) != 1 || models[0].GetContextWindow() != window || models[0].GetMaxOutputTokens() != 2048 {
			t.Fatalf("provider %s: unexpected model %v", id, models)
		}
	}
	catalog, err := llms.LoadCatalog(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	for id, window := range map[string]int{"gateway-a": 32_000, "gateway-b": 128_000, "gateway-default": 0} {
		target, err := catalog.Resolve(id, "shared-model", nil)
		if err != nil || target.ContextWindow != window {
			t.Fatalf("provider %s: window = %d, err = %v, want %d", id, target.ContextWindow, err, window)
		}
	}
	for _, tc := range []struct {
		name   string
		models *agentcomposev2.LLMProviderModels
		want   int32
	}{
		{name: "omitted list preserves window", want: 32_000},
		{name: "zero clears window", models: &agentcomposev2.LLMProviderModels{Models: []*agentcomposev2.LLMModelSpec{{
			Id: "shared-model", ContextWindow: proto.Int32(0), MaxOutputTokens: proto.Int32(2048),
		}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updated, err := handler.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{
				Provider: &agentcomposev2.LLMProviderSpec{Id: "gateway-a", Models: tc.models},
			}))
			if err != nil {
				t.Fatal(err)
			}
			models := updated.Msg.GetProvider().GetModels()
			if len(models) != 1 || models[0].GetContextWindow() != tc.want || models[0].GetMaxOutputTokens() != 2048 {
				t.Fatalf("unexpected model %v", models)
			}
		})
	}
}
