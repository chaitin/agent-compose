package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestIntegrationLLMProviderAuthJSONRoundTrip(t *testing.T) {
	ctx := context.Background()
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
	_, handler := agentcomposev2connect.NewLLMServiceHandler(NewLLMHandler(nil, store))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := agentcomposev2connect.NewLLMServiceClient(server.Client(), server.URL, connect.WithProtoJSON())
	var request agentcomposev2.CreateProviderRequest
	if err := protojson.Unmarshal([]byte(`{"provider":{"id":"gateway","baseUrl":"https://gateway.example","protocol":"anthropic_messages","apiKey":"test-key","auth":"LLM_PROVIDER_AUTH_X_API_KEY"}}`), &request); err != nil {
		t.Fatal(err)
	}
	created, err := client.CreateProvider(ctx, connect.NewRequest(&request))
	if err != nil {
		t.Fatal(err)
	}
	if created.Msg.Provider.Auth != agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_X_API_KEY {
		t.Fatal("explicit auth matching the protocol default was lost")
	}
	// Read the public representation and write its explicit choice back. It
	// must stay pinned after the protocol changes, even across JSON transport.
	got, err := client.GetProvider(ctx, connect.NewRequest(&agentcomposev2.GetProviderRequest{Id: "gateway"}))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := client.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{
		Provider: &agentcomposev2.LLMProviderSpec{Id: got.Msg.Provider.Id, Protocol: "responses", Auth: authPtr(got.Msg.Provider.Auth)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertProviderResponseRedacted(t, updated.Msg)
	if updated.Msg.Provider.Auth != agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_X_API_KEY {
		t.Fatal("Get/Update round trip lost the explicit choice")
	}
	var clear agentcomposev2.UpdateProviderRequest
	if err := protojson.Unmarshal([]byte(`{"provider":{"id":"gateway","auth":"LLM_PROVIDER_AUTH_UNSPECIFIED"}}`), &clear); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateProvider(ctx, connect.NewRequest(&clear)); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetManagedLLMProvider(ctx, "gateway")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Auth != "" || stored.AuthHeader != "Authorization" || stored.AuthScheme != "Bearer" {
		t.Fatal("explicit unspecified auth did not restore the protocol default")
	}
}
