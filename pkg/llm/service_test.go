package llm

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func TestServiceGenerateRequiresClient(t *testing.T) {
	ctx := context.Background()
	req := connect.NewRequest(&agentcomposev1.GenerateLLMRequest{Prompt: "hello"})

	for name, service := range map[string]*Service{
		"nil service": nil,
		"nil client":  {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.Generate(ctx, req)
			if connect.CodeOf(err) != connect.CodeUnavailable {
				t.Fatalf("Generate error code = %v, want %v: %v", connect.CodeOf(err), connect.CodeUnavailable, err)
			}
			if !strings.Contains(err.Error(), "llm client is unavailable") {
				t.Fatalf("Generate error = %v, want unavailable message", err)
			}
		})
	}
}

func TestServiceGenerateWrapsClientErrorsAsInternal(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream boom"}}`))
	}))
	t.Cleanup(server.Close)

	service := NewService(nil, nil, nil, NewClient(&appconfig.Config{
		LLMAPIEndpoint: server.URL,
		LLMModel:       "model-a",
	}, newTestConfigStore(t), server.Client()))

	_, err := service.Generate(ctx, connect.NewRequest(&agentcomposev1.GenerateLLMRequest{
		Prompt: "hello",
		Model:  "model-a",
	}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("Generate error code = %v, want %v: %v", connect.CodeOf(err), connect.CodeInternal, err)
	}
	if !strings.Contains(err.Error(), "upstream boom") {
		t.Fatalf("Generate error = %v, want upstream message", err)
	}
}

func TestServiceGenerateStructuredJSONResponse(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-service","model":"model-a","status":"completed","output_text":"{\"answer\":\"ok\"}"}`))
	}))
	t.Cleanup(server.Close)

	service := NewService(nil, nil, nil, NewClient(&appconfig.Config{
		LLMAPIEndpoint: server.URL,
		LLMModel:       "model-a",
	}, newTestConfigStore(t), server.Client()))
	schema := `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`

	resp, err := service.Generate(ctx, connect.NewRequest(&agentcomposev1.GenerateLLMRequest{
		Prompt:       "hello",
		Model:        "model-a",
		OutputSchema: schema,
	}))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if resp.Msg.GetText() != `{"answer":"ok"}` || resp.Msg.GetJson() != `{"answer":"ok"}` {
		t.Fatalf("unexpected text/json response: %+v", resp.Msg)
	}
	if resp.Msg.GetModel() != "model-a" || resp.Msg.GetResponseId() != "resp-service" || resp.Msg.GetFinishReason() != "completed" {
		t.Fatalf("unexpected metadata response: %+v", resp.Msg)
	}
}

func TestServiceGeneratePlainTextLeavesJSONEmpty(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-service","model":"model-a","status":"completed","output_text":"plain text"}`))
	}))
	t.Cleanup(server.Close)

	service := NewService(nil, nil, nil, NewClient(&appconfig.Config{
		LLMAPIEndpoint: server.URL,
		LLMModel:       "model-a",
	}, newTestConfigStore(t), server.Client()))

	resp, err := service.Generate(ctx, connect.NewRequest(&agentcomposev1.GenerateLLMRequest{
		Prompt: "hello",
		Model:  "model-a",
	}))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if resp.Msg.GetText() != "plain text" {
		t.Fatalf("text = %q, want plain text", resp.Msg.GetText())
	}
	if resp.Msg.GetJson() != "" {
		t.Fatalf("json = %q, want empty", resp.Msg.GetJson())
	}
}
