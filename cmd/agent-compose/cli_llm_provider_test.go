package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestIntegrationCLILLMProviderCommands(t *testing.T) {
	var created *agentcomposev2.LLMProviderSpec
	var updated *agentcomposev2.LLMProviderSpec
	var deleted string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		llm: llmServiceStub{
			listProviders: func(ctx context.Context, req *connect.Request[agentcomposev2.ListProvidersRequest]) (*connect.Response[agentcomposev2.ListProvidersResponse], error) {
				if req.Msg.GetLimit() != 500 {
					t.Fatalf("ListProviders request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.ListProvidersResponse{
					Providers: []*agentcomposev2.LLMProvider{testCLILLMProvider("gateway")},
					Total:     1,
				}), nil
			},
			createProvider: func(ctx context.Context, req *connect.Request[agentcomposev2.CreateProviderRequest]) (*connect.Response[agentcomposev2.CreateProviderResponse], error) {
				created = req.Msg.GetProvider()
				if created.GetId() != "gateway" || created.GetBaseUrl() != "https://example.com/v1" || created.GetProtocol() != "responses" || created.GetApiKey() != "secret" || !created.GetEnabled() {
					t.Fatalf("CreateProvider request = %#v", created)
				}
				return connect.NewResponse(&agentcomposev2.CreateProviderResponse{Provider: testCLILLMProvider("gateway")}), nil
			},
			getProvider: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProviderRequest]) (*connect.Response[agentcomposev2.GetProviderResponse], error) {
				if req.Msg.GetId() != "gateway" {
					t.Fatalf("GetProvider request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.GetProviderResponse{Provider: testCLILLMProvider("gateway")}), nil
			},
			updateProvider: func(ctx context.Context, req *connect.Request[agentcomposev2.UpdateProviderRequest]) (*connect.Response[agentcomposev2.UpdateProviderResponse], error) {
				updated = req.Msg.GetProvider()
				if updated.GetId() != "gateway" || updated.GetBaseUrl() != "https://rotated.example.com/v1" || updated.Name != "" || updated.Protocol != "" || updated.ApiKey != nil || updated.Enabled != nil {
					t.Fatalf("UpdateProvider request = %#v", updated)
				}
				provider := testCLILLMProvider("gateway")
				provider.BaseUrl = updated.GetBaseUrl()
				return connect.NewResponse(&agentcomposev2.UpdateProviderResponse{Provider: provider}), nil
			},
			deleteProvider: func(ctx context.Context, req *connect.Request[agentcomposev2.DeleteProviderRequest]) (*connect.Response[agentcomposev2.DeleteProviderResponse], error) {
				deleted = req.Msg.GetId()
				return connect.NewResponse(&agentcomposev2.DeleteProviderResponse{}), nil
			},
		},
	})
	defer server.Close()

	textOut, textErr, _, textCode := executeCLICommand("llm", "provider", "ls", "--host", server.URL)
	if textCode != 0 || textErr != "" || !strings.Contains(textOut, "gateway") || !strings.Contains(textOut, "responses") {
		t.Fatalf("llm provider ls text code/stdout/stderr = %d / %q / %q", textCode, textOut, textErr)
	}

	listOut, listErr, _, listCode := executeCLICommand("llm", "provider", "ls", "--host", server.URL, "--json")
	if listCode != 0 || listErr != "" {
		t.Fatalf("llm provider ls json code/stderr = %d / %q", listCode, listErr)
	}
	var listDecoded composeLLMProviderListOutput
	if err := json.Unmarshal([]byte(listOut), &listDecoded); err != nil {
		t.Fatalf("llm provider ls JSON decode failed: %v\n%s", err, listOut)
	}
	if len(listDecoded.Providers) != 1 || listDecoded.Providers[0].ID != "gateway" || !listDecoded.Providers[0].APIKeySet {
		t.Fatalf("llm provider ls JSON = %#v", listDecoded)
	}

	createOut, createErr, _, createCode := executeCLICommand("llm", "provider", "create", "--host", server.URL, "--base-url", "https://example.com/v1", "--protocol", "responses", "--api-key", "secret", "gateway")
	if createCode != 0 || createErr != "" || strings.TrimSpace(createOut) != "gateway" || created == nil {
		t.Fatalf("llm provider create code/stdout/stderr = %d / %q / %q", createCode, createOut, createErr)
	}

	inspectOut, inspectErr, _, inspectCode := executeCLICommand("llm", "provider", "inspect", "--host", server.URL, "gateway")
	if inspectCode != 0 || inspectErr != "" || !strings.Contains(inspectOut, "ID: gateway") || !strings.Contains(inspectOut, "API Key Set: true") {
		t.Fatalf("llm provider inspect code/stdout/stderr = %d / %q / %q", inspectCode, inspectOut, inspectErr)
	}

	updateOut, updateErr, _, updateCode := executeCLICommand("llm", "provider", "update", "--host", server.URL, "--base-url", "https://rotated.example.com/v1", "gateway")
	if updateCode != 0 || updateErr != "" || strings.TrimSpace(updateOut) != "gateway" || updated == nil {
		t.Fatalf("llm provider update code/stdout/stderr = %d / %q / %q", updateCode, updateOut, updateErr)
	}

	removeOut, removeErr, _, removeCode := executeCLICommand("llm", "provider", "rm", "--host", server.URL, "gateway")
	if removeCode != 0 || removeErr != "" || strings.TrimSpace(removeOut) != "gateway" || deleted != "gateway" {
		t.Fatalf("llm provider rm code/stdout/stderr = %d / %q / %q deleted=%q", removeCode, removeOut, removeErr, deleted)
	}
}

func TestE2ECLILLMProviderCommands(t *testing.T) {
	TestIntegrationCLILLMProviderCommands(t)
}

func TestIntegrationCLILLMProviderJSONAndPartialUpdate(t *testing.T) {
	var created *agentcomposev2.LLMProviderSpec
	var updated *agentcomposev2.LLMProviderSpec
	var listOffsets []uint32
	server := newComposeServiceStubServer(t, composeServiceStubs{
		llm: llmServiceStub{
			listProviders: func(ctx context.Context, req *connect.Request[agentcomposev2.ListProvidersRequest]) (*connect.Response[agentcomposev2.ListProvidersResponse], error) {
				listOffsets = append(listOffsets, req.Msg.GetOffset())
				if req.Msg.GetOffset() == 0 {
					return connect.NewResponse(&agentcomposev2.ListProvidersResponse{
						Providers: []*agentcomposev2.LLMProvider{testCLILLMProvider("gateway")},
						Total:     2,
					}), nil
				}
				second := testCLILLMProvider("messages")
				second.Protocol = "anthropic_messages"
				second.Enabled = false
				return connect.NewResponse(&agentcomposev2.ListProvidersResponse{
					Providers: []*agentcomposev2.LLMProvider{second},
					Total:     2,
				}), nil
			},
			createProvider: func(ctx context.Context, req *connect.Request[agentcomposev2.CreateProviderRequest]) (*connect.Response[agentcomposev2.CreateProviderResponse], error) {
				created = req.Msg.GetProvider()
				if created.GetId() != "messages" || created.GetName() != "Claude" || created.GetProtocol() != "anthropic_messages" || created.GetApiKey() != "secret" || created.Enabled == nil || !*created.Enabled {
					t.Fatalf("CreateProvider request = %#v", created)
				}
				provider := testCLILLMProvider("messages")
				provider.Name = created.GetName()
				provider.Protocol = created.GetProtocol()
				return connect.NewResponse(&agentcomposev2.CreateProviderResponse{Provider: provider}), nil
			},
			getProvider: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProviderRequest]) (*connect.Response[agentcomposev2.GetProviderResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProviderResponse{Provider: testCLILLMProvider(req.Msg.GetId())}), nil
			},
			updateProvider: func(ctx context.Context, req *connect.Request[agentcomposev2.UpdateProviderRequest]) (*connect.Response[agentcomposev2.UpdateProviderResponse], error) {
				updated = req.Msg.GetProvider()
				if updated.GetId() != "gateway" || updated.GetName() != "renamed" || updated.GetProtocol() != "anthropic_messages" || updated.GetApiKey() != "rotated" || updated.Enabled == nil || *updated.Enabled || updated.GetBaseUrl() != "" {
					t.Fatalf("UpdateProvider request = %#v", updated)
				}
				provider := testCLILLMProvider("gateway")
				provider.Name = updated.GetName()
				provider.Protocol = updated.GetProtocol()
				provider.Enabled = false
				return connect.NewResponse(&agentcomposev2.UpdateProviderResponse{Provider: provider}), nil
			},
			deleteProvider: func(ctx context.Context, req *connect.Request[agentcomposev2.DeleteProviderRequest]) (*connect.Response[agentcomposev2.DeleteProviderResponse], error) {
				return connect.NewResponse(&agentcomposev2.DeleteProviderResponse{}), nil
			},
		},
	})
	defer server.Close()

	listOut, listErr, _, listCode := executeCLICommand("llm", "provider", "ls", "--host", server.URL, "--json")
	if listCode != 0 || listErr != "" {
		t.Fatalf("paginated ls code/stderr = %d / %q", listCode, listErr)
	}
	var listDecoded composeLLMProviderListOutput
	if err := json.Unmarshal([]byte(listOut), &listDecoded); err != nil {
		t.Fatalf("paginated ls JSON: %v\n%s", err, listOut)
	}
	if listDecoded.Total != 2 || len(listDecoded.Providers) != 2 || listDecoded.Providers[1].Protocol != "anthropic_messages" || !reflect.DeepEqual(listOffsets, []uint32{0, 1}) {
		t.Fatalf("paginated ls = %#v offsets=%v", listDecoded, listOffsets)
	}

	createOut, createErr, _, createCode := executeCLICommand("llm", "provider", "create", "--json", "--host", server.URL, "--name", "Claude", "--base-url", "https://api.anthropic.com", "--protocol", "anthropic_messages", "--api-key", "secret", "messages")
	if createCode != 0 || createErr != "" || created == nil {
		t.Fatalf("json create code/stdout/stderr = %d / %q / %q", createCode, createOut, createErr)
	}
	var createDecoded composeLLMProviderCreateOutput
	if err := json.Unmarshal([]byte(createOut), &createDecoded); err != nil || createDecoded.Provider.ID != "messages" || createDecoded.Provider.Protocol != "anthropic_messages" {
		t.Fatalf("json create output = %q err=%v", createOut, err)
	}

	inspectOut, inspectErr, _, inspectCode := executeCLICommand("llm", "provider", "inspect", "--json", "--host", server.URL, "gateway")
	if inspectCode != 0 || inspectErr != "" {
		t.Fatalf("json inspect code/stderr = %d / %q", inspectCode, inspectErr)
	}
	var inspectDecoded composeLLMProviderInspectOutput
	if err := json.Unmarshal([]byte(inspectOut), &inspectDecoded); err != nil || inspectDecoded.Provider.ID != "gateway" || !inspectDecoded.Provider.APIKeySet {
		t.Fatalf("json inspect = %q err=%v", inspectOut, err)
	}

	updateOut, updateErr, _, updateCode := executeCLICommand("llm", "provider", "update", "--json", "--host", server.URL, "--name", "renamed", "--protocol", "anthropic_messages", "--api-key", "rotated", "--enabled=false", "gateway")
	if updateCode != 0 || updateErr != "" || updated == nil {
		t.Fatalf("json update code/stdout/stderr = %d / %q / %q", updateCode, updateOut, updateErr)
	}
	var updateDecoded composeLLMProviderUpdateOutput
	if err := json.Unmarshal([]byte(updateOut), &updateDecoded); err != nil || updateDecoded.Provider.Name != "renamed" || updateDecoded.Provider.Enabled {
		t.Fatalf("json update = %q err=%v", updateOut, err)
	}

	removeOut, removeErr, _, removeCode := executeCLICommand("llm", "provider", "rm", "--json", "--host", server.URL, "gateway")
	if removeCode != 0 || removeErr != "" {
		t.Fatalf("json rm code/stderr = %d / %q", removeCode, removeErr)
	}
	var removeDecoded composeLLMProviderRemoveOutput
	if err := json.Unmarshal([]byte(removeOut), &removeDecoded); err != nil || removeDecoded.ID != "gateway" || !removeDecoded.Removed {
		t.Fatalf("json rm = %q err=%v", removeOut, err)
	}
}

func TestE2ECLILLMProviderJSONAndPartialUpdate(t *testing.T) {
	TestIntegrationCLILLMProviderJSONAndPartialUpdate(t)
}

func TestCLILLMProviderUsageAndOutputHelpers(t *testing.T) {
	for _, args := range [][]string{
		{"llm", "provider", "create", "--base-url", "https://example.com", "--protocol", "responses", "--api-key", "secret"},
		{"llm", "provider", "inspect"},
		{"llm", "provider", "update"},
		{"llm", "provider", "rm"},
	} {
		stdout, stderr, runCount, code := executeCLICommand(args...)
		if code == 0 || stdout != "" || runCount != 0 || stderr == "" {
			t.Fatalf("%v code/stdout/stderr/runCount = %d / %q / %q / %d", args, code, stdout, stderr, runCount)
		}
	}
	if got := composeLLMProviderOutputFromProto(nil); got != (composeLLMProviderOutput{}) {
		t.Fatalf("nil provider output = %#v", got)
	}
	if err := writeLLMProviderJSON(failingWriter{}, composeLLMProviderListOutput{}); err == nil {
		t.Fatal("writeLLMProviderJSON failing writer returned nil error")
	}
	if err := writeLLMProviderListText(failingWriter{}, []composeLLMProviderOutput{{ID: "gateway"}}); err == nil {
		t.Fatal("writeLLMProviderListText failing writer returned nil error")
	}
	if err := writeLLMProviderInspectText(failingWriter{}, composeLLMProviderOutput{ID: "gateway"}); err == nil {
		t.Fatal("writeLLMProviderInspectText failing writer returned nil error")
	}
}

func TestE2ECLILLMProviderUsageAndOutputHelpers(t *testing.T) {
	TestCLILLMProviderUsageAndOutputHelpers(t)
}

type llmServiceStub struct {
	listProviders  func(context.Context, *connect.Request[agentcomposev2.ListProvidersRequest]) (*connect.Response[agentcomposev2.ListProvidersResponse], error)
	createProvider func(context.Context, *connect.Request[agentcomposev2.CreateProviderRequest]) (*connect.Response[agentcomposev2.CreateProviderResponse], error)
	getProvider    func(context.Context, *connect.Request[agentcomposev2.GetProviderRequest]) (*connect.Response[agentcomposev2.GetProviderResponse], error)
	updateProvider func(context.Context, *connect.Request[agentcomposev2.UpdateProviderRequest]) (*connect.Response[agentcomposev2.UpdateProviderResponse], error)
	deleteProvider func(context.Context, *connect.Request[agentcomposev2.DeleteProviderRequest]) (*connect.Response[agentcomposev2.DeleteProviderResponse], error)

	agentcomposev2connect.UnimplementedLLMServiceHandler
}

func (s llmServiceStub) ListProviders(ctx context.Context, req *connect.Request[agentcomposev2.ListProvidersRequest]) (*connect.Response[agentcomposev2.ListProvidersResponse], error) {
	if s.listProviders == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListProviders stub is not configured"))
	}
	return s.listProviders(ctx, req)
}

func (s llmServiceStub) CreateProvider(ctx context.Context, req *connect.Request[agentcomposev2.CreateProviderRequest]) (*connect.Response[agentcomposev2.CreateProviderResponse], error) {
	if s.createProvider == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("CreateProvider stub is not configured"))
	}
	return s.createProvider(ctx, req)
}

func (s llmServiceStub) GetProvider(ctx context.Context, req *connect.Request[agentcomposev2.GetProviderRequest]) (*connect.Response[agentcomposev2.GetProviderResponse], error) {
	if s.getProvider == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("GetProvider stub is not configured"))
	}
	return s.getProvider(ctx, req)
}

func (s llmServiceStub) UpdateProvider(ctx context.Context, req *connect.Request[agentcomposev2.UpdateProviderRequest]) (*connect.Response[agentcomposev2.UpdateProviderResponse], error) {
	if s.updateProvider == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("UpdateProvider stub is not configured"))
	}
	return s.updateProvider(ctx, req)
}

func (s llmServiceStub) DeleteProvider(ctx context.Context, req *connect.Request[agentcomposev2.DeleteProviderRequest]) (*connect.Response[agentcomposev2.DeleteProviderResponse], error) {
	if s.deleteProvider == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("DeleteProvider stub is not configured"))
	}
	return s.deleteProvider(ctx, req)
}

func testCLILLMProvider(id string) *agentcomposev2.LLMProvider {
	return &agentcomposev2.LLMProvider{
		Id:        id,
		Name:      id,
		BaseUrl:   "https://example.com/v1",
		Protocol:  "responses",
		Enabled:   true,
		ApiKeySet: true,
		CreatedAt: mustProtoTimestamp("2026-07-07T12:00:00Z"),
		UpdatedAt: mustProtoTimestamp("2026-07-07T12:00:00Z"),
	}
}
