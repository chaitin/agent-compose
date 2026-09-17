package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestIntegrationLLMProviderConnectLifecycle(t *testing.T) {
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
	path, handler := agentcomposev2connect.NewLLMServiceHandler(NewLLMHandler(nil, store))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := agentcomposev2connect.NewLLMServiceClient(server.Client(), server.URL)
	spec := &agentcomposev2.LLMProviderSpec{Id: "gateway", BaseUrl: "https://example.com/v1", Protocol: "responses", ApiKey: proto.String("upstream-secret")}
	created, err := client.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: spec}))
	if err != nil {
		t.Fatal(err)
	}
	if !created.Msg.Provider.Enabled || !created.Msg.Provider.ApiKeySet || created.Msg.Provider.Id != "gateway" {
		t.Fatalf("create response: %v", created.Msg)
	}
	assertProviderResponseRedacted(t, created.Msg)
	_, err = client.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: spec}))
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate: %v", err)
	}
	spec.ApiKey = nil
	spec.Name = "renamed"
	spec.Enabled = proto.Bool(false)
	updated, err := client.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{Provider: spec}))
	if err != nil || updated.Msg.Provider.Enabled || updated.Msg.Provider.Name != "renamed" || !updated.Msg.Provider.ApiKeySet {
		t.Fatalf("update: %v", err)
	}
	assertProviderResponseRedacted(t, updated.Msg)
	saved, err := store.GetManagedLLMProvider(ctx, spec.Id)
	if err != nil || saved.APIKey != "upstream-secret" {
		t.Fatalf("omitted key not preserved: %v", err)
	}
	partial, err := client.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{Provider: &agentcomposev2.LLMProviderSpec{Id: spec.Id, BaseUrl: "https://rotated.example.com/v1"}}))
	if err != nil || partial.Msg.Provider.Enabled || partial.Msg.Provider.Name != "renamed" || partial.Msg.Provider.BaseUrl != "https://rotated.example.com/v1" || !partial.Msg.Provider.ApiKeySet {
		t.Fatalf("partial update replaced omitted fields: %v %#v", err, partial)
	}
	saved, err = store.GetManagedLLMProvider(ctx, spec.Id)
	if err != nil || saved.APIKey != "upstream-secret" || saved.Enabled || saved.Name != "renamed" {
		t.Fatalf("partial update clobbered stored values: %v %#v", err, saved)
	}
	got, err := client.GetProvider(ctx, connect.NewRequest(&agentcomposev2.GetProviderRequest{Id: spec.Id}))
	if err != nil || got.Msg.Provider.Enabled {
		t.Fatalf("get disabled: %v", err)
	}
	assertProviderResponseRedacted(t, got.Msg)
	listed, err := client.ListProviders(ctx, connect.NewRequest(&agentcomposev2.ListProvidersRequest{Limit: 1}))
	if err != nil || listed.Msg.Total != 1 || len(listed.Msg.Providers) != 1 {
		t.Fatalf("list: %v", err)
	}
	assertProviderResponseRedacted(t, listed.Msg)
	listed, err = client.ListProviders(ctx, connect.NewRequest(&agentcomposev2.ListProvidersRequest{Offset: 1, Limit: 1}))
	if err != nil || listed.Msg.Total != 1 || len(listed.Msg.Providers) != 0 {
		t.Fatalf("pagination: %v", err)
	}
	if _, err := client.DeleteProvider(ctx, connect.NewRequest(&agentcomposev2.DeleteProviderRequest{Id: spec.Id})); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetProvider(ctx, connect.NewRequest(&agentcomposev2.GetProviderRequest{Id: spec.Id})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("get deleted: %v", err)
	}
	if _, err := client.DeleteProvider(ctx, connect.NewRequest(&agentcomposev2.DeleteProviderRequest{Id: spec.Id})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("delete missing: %v", err)
	}
	// The credential presentation travels over the transport, is stored on the
	// connection, and is reported back as the effective configuration.
	messagesSpec := &agentcomposev2.LLMProviderSpec{
		Id: "messages", BaseUrl: "https://messages.example", Protocol: "anthropic_messages",
		ApiKey: proto.String("messages-secret"),
		Auth:   authPtr(agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_BEARER),
	}
	createdMessages, err := client.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: messagesSpec}))
	if err != nil || createdMessages.Msg.Provider.Auth != agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_BEARER {
		t.Fatalf("create messages provider: %v %#v", err, createdMessages)
	}
	savedMessages, err := store.GetManagedLLMProvider(ctx, messagesSpec.Id)
	if err != nil || savedMessages.AuthHeader != "Authorization" || savedMessages.AuthScheme != "Bearer" {
		t.Fatalf("stored auth = %#v, err %v", savedMessages, err)
	}
	if _, err := client.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{Provider: &agentcomposev2.LLMProviderSpec{Id: messagesSpec.Id, Name: "renamed"}})); err != nil {
		t.Fatal(err)
	}
	savedMessages, err = store.GetManagedLLMProvider(ctx, messagesSpec.Id)
	if err != nil || savedMessages.AuthHeader != "Authorization" || savedMessages.AuthScheme != "Bearer" {
		t.Fatalf("omitted auth was not preserved: %#v, err %v", savedMessages, err)
	}
	if _, err := client.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: &agentcomposev2.LLMProviderSpec{
		Id: "bad-auth", BaseUrl: "https://example.com", Protocol: "anthropic_messages",
		ApiKey: proto.String("upstream-secret"), Auth: authPtr(agentcomposev2.LLMProviderAuth(99)),
	}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown auth create: %v", err)
	}
	for _, invalid := range []*agentcomposev2.LLMProviderSpec{
		nil,
		{Id: "bad", BaseUrl: "https://example.com", Protocol: "unknown", ApiKey: proto.String("upstream-secret")},
		{Id: "bad", BaseUrl: "https://example.com", Protocol: "responses"},
		{Id: "default", BaseUrl: "https://example.com", Protocol: "responses", ApiKey: proto.String("upstream-secret")},
	} {
		_, err := client.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{Provider: invalid}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument || strings.Contains(err.Error(), "upstream-secret") {
			t.Fatalf("invalid create: %v", err)
		}
	}
	if _, err := client.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty update: %v", err)
	}
	if _, err := client.ListProviders(ctx, connect.NewRequest(&agentcomposev2.ListProvidersRequest{Limit: 10001})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid pagination: %v", err)
	}
}

func assertProviderResponseRedacted(t *testing.T, message proto.Message) {
	t.Helper()
	raw, err := protojson.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "upstream-secret") || strings.Contains(string(raw), `"apiKey":`) {
		t.Fatalf("credential leaked in response: %T", message)
	}
}

func TestE2ELLMProviderConnectLifecycle(t *testing.T) {
	TestIntegrationLLMProviderConnectLifecycle(t)
}
