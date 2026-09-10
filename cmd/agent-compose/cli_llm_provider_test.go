package main

import (
	"context"
	"encoding/json"
	"fmt"
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
