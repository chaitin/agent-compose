package agentcompose

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
	"github.com/labstack/echo/v4"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func TestIsRuntimeLLMFacadeRequestMatchesOnlyRegisteredPOSTRoutes(t *testing.T) {
	valid := []string{
		"/api/runtime/sessions/session-1/llm/openai/v1/responses",
		"/api/runtime/sessions/session-1/llm/openai/v1/chat/completions",
		"/api/runtime/sessions/session-1/llm/anthropic/v1/messages",
	}
	for _, path := range valid {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if !IsRuntimeLLMFacadeRequest(req) {
			t.Fatalf("IsRuntimeLLMFacadeRequest(%q) = false, want true", path)
		}
	}

	invalid := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/runtime/sessions/session-1/llm/openai/v1/responses"},
		{http.MethodPost, "/api/runtime/sessions/session-1/llm/openai/v1/responses/extra"},
		{http.MethodPost, "/api/runtime/sessions/session-1/not-llm/openai/v1/responses"},
		{http.MethodPost, "/api/runtime/sessions/session-1/llm/openai/v1/unknown"},
		{http.MethodPost, "/api/runtime/sessions/session-1/llm/anthropic/v1/messages/extra"},
		{http.MethodPost, "/api/other/session-1/llm/openai/v1/responses"},
	}
	for _, tc := range invalid {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if IsRuntimeLLMFacadeRequest(req) {
			t.Fatalf("IsRuntimeLLMFacadeRequest(%s %q) = true, want false", tc.method, tc.path)
		}
	}
}

func TestRuntimeLLMUseGenericResponsesTextPartsRequiresExplicitProviderFlag(t *testing.T) {
	target := llms.ResolvedTarget{
		Provider: llms.Provider{ID: "not-qwen", Name: "qwen-compatible-v2"},
		Model:    llms.Model{ID: "alias-qwen", Name: "qwen3.7-max"},
	}
	if llms.UseGenericResponsesTextParts(target, protocolbridge.ProtocolOpenAIResponses) {
		t.Fatalf("generic responses text parts should not be enabled by provider/model names")
	}
	target.Provider.UseGenericResponsesTextParts = true
	if !llms.UseGenericResponsesTextParts(target, protocolbridge.ProtocolOpenAIResponses) {
		t.Fatalf("generic responses text parts should be enabled by explicit provider flag")
	}
	if llms.UseGenericResponsesTextParts(target, protocolbridge.ProtocolOpenAIChat) {
		t.Fatalf("generic responses text parts should only apply to OpenAI Responses upstreams")
	}
}

func TestRuntimeLLMFacadeForwardsWithSessionToken(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.LLMAPIKey = "provider-key"

	var gotAuth string
	var gotRuntimeAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotRuntimeAuth = r.Header.Get("X-Runtime-Auth")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-facade","model":"model-a","status":"completed","output_text":"ok"}`))
	}))
	t.Cleanup(upstream.Close)
	service.config.LLMAPIEndpoint = upstream.URL
	service.llm.client = upstream.Client()

	session, err := service.store.CreateSession(ctx, "facade", "", "boxlite", "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := service.store.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, "model-a", "default", llms.APIProtocolResponses, "test", "run-1")
	if err != nil {
		t.Fatalf("llms.NewFacadeToken returned error: %v", err)
	}
	token.ExpiresAt = time.Now().Add(time.Hour)
	if err := service.configDB.SaveLLMFacadeToken(ctx, token); err != nil {
		t.Fatalf("SaveLLMFacadeToken returned error: %v", err)
	}

	app := echo.New()
	registerRuntimeLLMFacadeRoutes(app, service)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.Summary.ID+"/llm/openai/v1/responses", bytes.NewBufferString(`{"model":"model-a","input":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+tokenValue)
	req.Header.Set("X-Runtime-Auth", "should-not-forward")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("facade status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer provider-key" {
		t.Fatalf("upstream Authorization = %q, want provider auth", gotAuth)
	}
	if gotRuntimeAuth != "" {
		t.Fatalf("runtime sensitive header forwarded: %q", gotRuntimeAuth)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.Summary.ID+"/llm/openai/v1/responses", bytes.NewBufferString(`{"model":"model-a","input":"hello"}`))
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want 401", rec.Code)
	}
}

func TestRuntimeLLMFacadeFlushesSSEResponses(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.LLMAPIKey = "provider-key"

	const upstreamEvents = "event: message\ndata: hello\n\nevent: done\ndata: [DONE]\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(upstreamEvents))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	t.Cleanup(upstream.Close)
	service.config.LLMAPIEndpoint = upstream.URL
	service.llm.client = upstream.Client()

	session, err := service.store.CreateSession(ctx, "facade-sse", "", "boxlite", "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := service.store.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, "model-a", "default", llms.APIProtocolResponses, "test", "run-1")
	if err != nil {
		t.Fatalf("llms.NewFacadeToken returned error: %v", err)
	}
	token.ExpiresAt = time.Now().Add(time.Hour)
	if err := service.configDB.SaveLLMFacadeToken(ctx, token); err != nil {
		t.Fatalf("SaveLLMFacadeToken returned error: %v", err)
	}

	app := echo.New()
	registerRuntimeLLMFacadeRoutes(app, service)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.Summary.ID+"/llm/openai/v1/responses", bytes.NewBufferString(`{"model":"model-a","input":"hello","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+tokenValue)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("facade status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !rec.Flushed {
		t.Fatalf("sse response was not flushed")
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if body := rec.Body.String(); body != upstreamEvents {
		t.Fatalf("unexpected SSE body: %q", body)
	}
}

func TestRuntimeLLMAnthropicFacadeForwardsWithSessionToken(t *testing.T) {
	ctx := context.Background()
	t.Setenv("ANTHROPIC_API_KEY", "")
	service, _, _ := newTestServiceAPIHarness(t)

	var gotAPIKey string
	var gotAuth string
	var gotVersion string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("upstream path = %q, want /v1/messages", r.URL.Path)
		}
		gotAPIKey = r.Header.Get("x-api-key")
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_facade","type":"message","model":"claude-test","content":[{"type":"text","text":"ok"}]}`))
	}))
	t.Cleanup(upstream.Close)
	service.llm.client = upstream.Client()
	if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID:             "anthropic",
		Name:           "anthropic",
		ProviderType:   llms.ProviderFamilyAnthropic,
		DefaultWireAPI: llms.APIProtocolMessages,
		BaseURL:        upstream.URL,
		APIKey:         "provider-key",
		AuthHeader:     "x-api-key",
		AuthScheme:     "",
		HeadersJSON:    `{"anthropic-version":"2023-06-01"}`,
		Weight:         10,
		Enabled:        true,
		Scope:          llms.ProviderScopeSystem,
	}, llms.Model{ID: "claude-test", Name: "claude-test", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("UpsertDefaultLLMConfig returned error: %v", err)
	}

	session, err := service.store.CreateSession(ctx, "facade-claude", "", "boxlite", "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := service.store.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, "claude-test", "anthropic", llms.APIProtocolMessages, "test", "run-1")
	if err != nil {
		t.Fatalf("llms.NewFacadeToken returned error: %v", err)
	}
	token.ExpiresAt = time.Now().Add(time.Hour)
	if err := service.configDB.SaveLLMFacadeToken(ctx, token); err != nil {
		t.Fatalf("SaveLLMFacadeToken returned error: %v", err)
	}

	app := echo.New()
	registerRuntimeLLMFacadeRoutes(app, service)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.Summary.ID+"/llm/anthropic/v1/messages", bytes.NewBufferString(`{"model":"claude-test","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("x-api-key", tokenValue)
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("facade status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotAPIKey != "provider-key" {
		t.Fatalf("upstream x-api-key = %q, want provider key", gotAPIKey)
	}
	if gotAuth != "" {
		t.Fatalf("runtime Authorization forwarded to anthropic upstream: %q", gotAuth)
	}
	if gotVersion != "2023-06-01" {
		t.Fatalf("upstream anthropic-version = %q, want 2023-06-01", gotVersion)
	}
}

func TestRuntimeLLMOpenAIResponsesFacadeBridgesToAnthropicProvider(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	service, _, _ := newTestServiceAPIHarness(t)

	var gotPath string
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		if r.Header.Get("x-api-key") != "provider-key" {
			t.Errorf("upstream x-api-key = %q, want provider-key", r.Header.Get("x-api-key"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_bridge","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"bridged"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":4}}`))
	}))
	t.Cleanup(upstream.Close)
	service.llm.client = upstream.Client()
	if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID:             "anthropic",
		Name:           "anthropic",
		ProviderType:   llms.ProviderFamilyAnthropic,
		DefaultWireAPI: llms.APIProtocolMessages,
		BaseURL:        upstream.URL,
		APIKey:         "provider-key",
		AuthHeader:     "x-api-key",
		AuthScheme:     "",
		HeadersJSON:    `{"anthropic-version":"2023-06-01"}`,
		Weight:         1,
		Enabled:        true,
		Scope:          llms.ProviderScopeSystem,
	}, llms.Model{ID: "claude-test", Name: "claude-test", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("UpsertDefaultLLMConfig returned error: %v", err)
	}
	session := createRunningLLMFacadeSession(t, ctx, service, "bridge-openai-anthropic")
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, "claude-test", "anthropic", llms.APIProtocolResponses, "test", "run-1")
	if err != nil {
		t.Fatalf("llms.NewFacadeToken returned error: %v", err)
	}
	token.ExpiresAt = time.Now().Add(time.Hour)
	if err := service.configDB.SaveLLMFacadeToken(ctx, token); err != nil {
		t.Fatalf("SaveLLMFacadeToken returned error: %v", err)
	}

	app := echo.New()
	registerRuntimeLLMFacadeRoutes(app, service)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.Summary.ID+"/llm/openai/v1/responses", bytes.NewBufferString(`{"model":"claude-test","input":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+tokenValue)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("facade status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages", gotPath)
	}
	if !strings.Contains(gotBody, `"messages"`) || !strings.Contains(gotBody, `"model":"claude-test"`) {
		t.Fatalf("upstream body was not anthropic messages: %s", gotBody)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"object":"response"`) || !strings.Contains(body, "bridged") {
		t.Fatalf("facade body was not openai responses: %s", body)
	}
}

func TestRuntimeLLMAnthropicFacadeBridgesToOpenAIResponsesProvider(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	service, _, _ := newTestServiceAPIHarness(t)

	var gotPath string
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		if r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Errorf("upstream Authorization = %q, want provider auth", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_bridge","object":"response","status":"completed","model":"gpt-test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"bridged"}]}],"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}`))
	}))
	t.Cleanup(upstream.Close)
	service.llm.client = upstream.Client()
	if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID:             "default",
		Name:           "default",
		ProviderType:   llms.ProviderFamilyOpenAI,
		DefaultWireAPI: llms.APIProtocolResponses,
		BaseURL:        upstream.URL,
		APIKey:         "provider-key",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		HeadersJSON:    `{}`,
		Weight:         1,
		Enabled:        true,
		Scope:          llms.ProviderScopeSystem,
	}, llms.Model{ID: "gpt-test", Name: "gpt-test", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("UpsertDefaultLLMConfig returned error: %v", err)
	}
	session := createRunningLLMFacadeSession(t, ctx, service, "bridge-anthropic-openai")
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, "gpt-test", "default", llms.APIProtocolMessages, "test", "run-1")
	if err != nil {
		t.Fatalf("llms.NewFacadeToken returned error: %v", err)
	}
	token.ExpiresAt = time.Now().Add(time.Hour)
	if err := service.configDB.SaveLLMFacadeToken(ctx, token); err != nil {
		t.Fatalf("SaveLLMFacadeToken returned error: %v", err)
	}

	app := echo.New()
	registerRuntimeLLMFacadeRoutes(app, service)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.Summary.ID+"/llm/anthropic/v1/messages", bytes.NewBufferString(`{"model":"gpt-test","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+tokenValue)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("facade status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("upstream path = %q, want /v1/responses", gotPath)
	}
	if !strings.Contains(gotBody, `"input"`) || !strings.Contains(gotBody, `"model":"gpt-test"`) {
		t.Fatalf("upstream body was not openai responses: %s", gotBody)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"type":"message"`) || !strings.Contains(body, "bridged") {
		t.Fatalf("facade body was not anthropic messages: %s", body)
	}
}

func TestAnthropicProviderCanBootstrapFromGenericLLMAPIKey(t *testing.T) {
	ctx := context.Background()
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_API_ENDPOINT", "")
	service, _, _ := newTestServiceAPIHarness(t)
	t.Setenv("LLM_API_KEY", "generic-provider-key")
	t.Setenv("LLM_API_ENDPOINT", "https://anthropic.example.invalid/v1/messages")

	target, err := resolveLLMTargetForProviderFamily(ctx, service.config, service.configDB, llms.ProviderFamilyAnthropic, "claude-test")
	if err != nil {
		t.Fatalf("resolveLLMTargetForProviderFamily returned error: %v", err)
	}
	if target.Provider.ID != "anthropic" {
		t.Fatalf("provider id = %q, want anthropic", target.Provider.ID)
	}
	if target.Headers.Get("x-api-key") != "generic-provider-key" {
		t.Fatalf("x-api-key = %q, want generic provider key", target.Headers.Get("x-api-key"))
	}
	if target.Provider.BaseURL != "https://anthropic.example.invalid/v1" {
		t.Fatalf("provider base url = %q, want normalized anthropic base url", target.Provider.BaseURL)
	}
	if target.Endpoint != "https://anthropic.example.invalid/v1/messages" {
		t.Fatalf("endpoint = %q, want anthropic messages endpoint", target.Endpoint)
	}
}

func TestRuntimeLLMTargetDoesNotTreatGenericOpenAIEndpointAsAnthropic(t *testing.T) {
	ctx := context.Background()
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_API_ENDPOINT", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("CLAUDE_MODEL", "")
	t.Setenv("LLM_API_KEY", "generic-provider-key")
	t.Setenv("LLM_API_ENDPOINT", "https://openai.example.invalid/v1/responses")
	service, _, _ := newTestServiceAPIHarness(t)

	target, err := resolveRuntimeLLMTarget(ctx, service.config, service.configDB, "gpt-test", "")
	if err != nil {
		t.Fatalf("resolveRuntimeLLMTarget returned error: %v", err)
	}
	if target.Provider.ID != "default" {
		t.Fatalf("provider id = %q, want default openai provider", target.Provider.ID)
	}
	if target.Provider.ProviderType != llms.ProviderFamilyOpenAI {
		t.Fatalf("provider type = %q, want openai", target.Provider.ProviderType)
	}
}

func createRunningLLMFacadeSession(t *testing.T, ctx context.Context, service *Service, title string) *Session {
	t.Helper()
	session, err := service.store.CreateSession(ctx, title, "", "boxlite", "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := service.store.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	return session
}

func TestAnthropicProviderCanBootstrapFromAuthToken(t *testing.T) {
	ctx := context.Background()
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "auth-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://anthropic.example.invalid")
	service, _, _ := newTestServiceAPIHarness(t)

	target, err := resolveLLMTargetForProviderFamily(ctx, service.config, service.configDB, llms.ProviderFamilyAnthropic, "kimi-k2.6")
	if err != nil {
		t.Fatalf("resolveLLMTargetForProviderFamily returned error: %v", err)
	}
	if target.Headers.Get("Authorization") != "Bearer auth-token" {
		t.Fatalf("Authorization = %q, want bearer auth token", target.Headers.Get("Authorization"))
	}
	if target.Headers.Get("x-api-key") != "" {
		t.Fatalf("x-api-key = %q, want empty when auth token is used", target.Headers.Get("x-api-key"))
	}
	if target.Provider.BaseURL != "https://anthropic.example.invalid/v1" {
		t.Fatalf("provider base url = %q, want normalized anthropic base url", target.Provider.BaseURL)
	}
}

func TestProviderForwardHeadersFiltersManagedHeaders(t *testing.T) {
	headers, err := llms.ProviderForwardHeaders(llms.Provider{
		APIKey:      "provider-key",
		AuthHeader:  "Authorization",
		AuthScheme:  "Bearer",
		HeadersJSON: `{"Content-Type":"text/plain","Authorization":"Bearer wrong","X-Provider":"ok"}`,
	})
	if err != nil {
		t.Fatalf("llms.ProviderForwardHeaders returned error: %v", err)
	}
	if got := headers.Get("Content-Type"); got != "" {
		t.Fatalf("Content-Type = %q, want filtered", got)
	}
	if got := headers.Get("Authorization"); got != "Bearer provider-key" {
		t.Fatalf("Authorization = %q, want provider auth", got)
	}
	if got := headers.Get("X-Provider"); got != "ok" {
		t.Fatalf("X-Provider = %q, want ok", got)
	}
}

func TestNewLLMFacadeTokenDoesNotExpireByDefault(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newTestServiceAPIHarness(t)
	tokenValue, token, err := llms.NewFacadeToken("session-1", "model-a", "default", llms.APIProtocolResponses, "test", "run-1")
	if err != nil {
		t.Fatalf("llms.NewFacadeToken returned error: %v", err)
	}
	if !token.ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt = %v, want zero for session-scoped token", token.ExpiresAt)
	}
	if err := service.configDB.SaveLLMFacadeToken(ctx, token); err != nil {
		t.Fatalf("SaveLLMFacadeToken returned error: %v", err)
	}
	stored, err := service.configDB.GetLLMFacadeToken(ctx, tokenValue)
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if !stored.ExpiresAt.IsZero() {
		t.Fatalf("stored ExpiresAt = %v, want zero for session-scoped token", stored.ExpiresAt)
	}
}

func TestManagedRuntimeEnvMapKeepsFacadeKeyAliases(t *testing.T) {
	userEnv := llms.RuntimeEnvMap([]SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://provider.example.invalid/v1"},
		{Name: "OPENAI_API_KEY", Value: "provider-key"},
		{Name: "DATABASE_PASSWORD", Value: "db-secret", Secret: true},
	})
	if _, ok := userEnv["OPENAI_API_KEY"]; ok {
		t.Fatalf("OPENAI_API_KEY leaked from user runtime env: %#v", userEnv)
	}
	if userEnv["DATABASE_PASSWORD"] != "db-secret" {
		t.Fatalf("DATABASE_PASSWORD = %q, want db-secret", userEnv["DATABASE_PASSWORD"])
	}

	managedEnv := llms.ManagedRuntimeEnvMap([]SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "http://agent-compose.test/api/runtime/sessions/s1/llm/openai/v1"},
		{Name: "LLM_API_KEY", Value: "facade-token"},
		{Name: "LLM_API_PROTOCOL", Value: llms.APIProtocolResponses},
		{Name: "OPENAI_API_KEY", Value: "facade-token"},
	})
	if managedEnv["OPENAI_API_KEY"] != "facade-token" {
		t.Fatalf("managed OPENAI_API_KEY = %q, want facade token", managedEnv["OPENAI_API_KEY"])
	}
	if managedEnv["LLM_API_KEY"] != "facade-token" {
		t.Fatalf("managed LLM_API_KEY = %q, want facade token", managedEnv["LLM_API_KEY"])
	}
	if managedEnv["LLM_API_ENDPOINT"] != "http://agent-compose.test/api/runtime/sessions/s1/llm/openai/v1" {
		t.Fatalf("managed LLM_API_ENDPOINT = %q, want facade endpoint", managedEnv["LLM_API_ENDPOINT"])
	}
}

func TestEnsureSessionLLMFacadeConfigUsesRequestedModel(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID:             "default",
		Name:           "default",
		ProviderType:   llms.ProviderFamilyOpenAI,
		DefaultWireAPI: llms.APIProtocolResponses,
		BaseURL:        "https://llm.example.invalid/v1",
		APIKey:         "provider-key",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		HeadersJSON:    "{}",
		Weight:         1,
		Enabled:        true,
		Scope:          llms.ProviderScopeSystem,
	}, llms.Model{ID: "model-a", Name: "model-a", DefaultModel: true, Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("UpsertDefaultLLMConfig model-a returned error: %v", err)
	}
	if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID:             "default",
		Name:           "default",
		ProviderType:   llms.ProviderFamilyOpenAI,
		DefaultWireAPI: llms.APIProtocolResponses,
		BaseURL:        "https://llm.example.invalid/v1",
		APIKey:         "provider-key",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		HeadersJSON:    "{}",
		Weight:         1,
		Enabled:        true,
		Scope:          llms.ProviderScopeSystem,
	}, llms.Model{ID: "model-b", Name: "model-b", DefaultModel: false, Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("UpsertDefaultLLMConfig model-b returned error: %v", err)
	}
	session := createRunningLLMFacadeSession(t, ctx, service, "requested-model")

	env, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "codex", "model-b", "test", "run-1")
	if err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig returned error: %v", err)
	}
	if env["AGENT_COMPOSE_SESSION_TOKEN"] == "" {
		t.Fatalf("AGENT_COMPOSE_SESSION_TOKEN missing from managed env: %#v", env)
	}
	if env["LLM_API_KEY"] != env["AGENT_COMPOSE_SESSION_TOKEN"] {
		t.Fatalf("LLM_API_KEY = %q, want facade token alias", env["LLM_API_KEY"])
	}
	if env["LLM_API_ENDPOINT"] != "http://agent-compose.test/api/runtime/sessions/"+session.Summary.ID+"/llm/openai/v1" {
		t.Fatalf("LLM_API_ENDPOINT = %q, want facade endpoint alias", env["LLM_API_ENDPOINT"])
	}
	configPath := filepath.Join(execution.HostSessionHome(session), ".codex", "config.toml")
	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", configPath, err)
	}
	if !strings.Contains(string(payload), `model = "model-b"`) {
		t.Fatalf("codex config did not use requested model-b: %s", string(payload))
	}
}

func TestEnsureSessionOpenCodeCustomProviderWritesConfig(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID:             "chaitin",
		Name:           "chaitin",
		ProviderType:   llms.ProviderFamilyOpenAI,
		DefaultWireAPI: llms.APIProtocolChatCompletions,
		BaseURL:        "https://aiapi.example.invalid/v1",
		APIKey:         "provider-key",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		HeadersJSON:    "{}",
		Weight:         1,
		Enabled:        true,
		Scope:          llms.ProviderScopeSystem,
	}, llms.Model{ID: "kimi-k2.6", Name: "kimi-k2.6", DefaultModel: true, Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("UpsertDefaultLLMConfig returned error: %v", err)
	}
	session := createRunningLLMFacadeSession(t, ctx, service, "opencode-custom")

	env, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "opencode", "chaitin/kimi-k2.6", "test", "run-1")
	if err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig returned error: %v", err)
	}
	if env["AGENT_COMPOSE_SESSION_TOKEN"] == "" || env["AGENT_COMPOSE_SESSION_TOKEN"] != env["LLM_API_KEY"] {
		t.Fatalf("managed facade env missing token aliases: %#v", env)
	}
	if env["OPENCODE_CONFIG"] != "/root/.config/opencode/opencode.json" {
		t.Fatalf("OPENCODE_CONFIG = %q, want guest opencode config path", env["OPENCODE_CONFIG"])
	}
	if strings.Contains(strings.Join([]string{env["LLM_API_KEY"], env["OPENAI_API_KEY"]}, " "), "provider-key") {
		t.Fatalf("provider key leaked into runtime env: %#v", env)
	}
	token, err := service.configDB.GetLLMFacadeToken(ctx, env["AGENT_COMPOSE_SESSION_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if token.ProviderID != "chaitin" || token.Model != "kimi-k2.6" || token.WireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("facade token = %#v, want chaitin/kimi-k2.6 chat facade", token)
	}
	configPath := filepath.Join(execution.HostSessionHome(session), ".config", "opencode", "opencode.json")
	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", configPath, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode opencode config: %v\n%s", err, string(payload))
	}
	providers := decoded["provider"].(map[string]any)
	chaitin := providers["chaitin"].(map[string]any)
	if chaitin["npm"] != "@ai-sdk/openai-compatible" {
		t.Fatalf("opencode provider npm = %#v", chaitin["npm"])
	}
	options := chaitin["options"].(map[string]any)
	if options["baseURL"] != "http://agent-compose.test/api/runtime/sessions/"+session.Summary.ID+"/llm/openai/v1" {
		t.Fatalf("opencode baseURL = %#v", options["baseURL"])
	}
	if options["apiKey"] != "{env:AGENT_COMPOSE_SESSION_TOKEN}" {
		t.Fatalf("opencode apiKey = %#v", options["apiKey"])
	}
	models := chaitin["models"].(map[string]any)
	if _, ok := models["kimi-k2.6"]; !ok {
		t.Fatalf("opencode models = %#v, want kimi-k2.6", models)
	}
}

func TestEnsureSessionOpenCodeCustomProviderBootstrapsFromDefaultEnv(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("LLM_MODEL", "")
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	service.config.LLMAPIEndpoint = "https://aiapi.example.invalid"
	service.config.LLMAPIProtocol = llms.APIProtocolChatCompletions
	service.config.LLMAPIKey = "default-env-key"
	service.config.LLMModel = "kimi-k2.6"
	session := createRunningLLMFacadeSession(t, ctx, service, "opencode-custom-default-env")

	env, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "opencode", "chaitin/kimi-k2.6", "test", "run-1")
	if err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig returned error: %v", err)
	}
	token, err := service.configDB.GetLLMFacadeToken(ctx, env["AGENT_COMPOSE_SESSION_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if token.ProviderID != "chaitin" || token.Model != "kimi-k2.6" {
		t.Fatalf("facade token = %#v, want bootstrapped chaitin/kimi-k2.6", token)
	}
	providers, err := service.configDB.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	if len(providers) != 1 || providers[0].ID != "chaitin" || providers[0].APIKey != "default-env-key" {
		t.Fatalf("providers = %#v, want single chaitin provider from default env", providers)
	}
	if env["OPENCODE_CONFIG"] != "/root/.config/opencode/opencode.json" {
		t.Fatalf("OPENCODE_CONFIG = %q, want custom opencode config path", env["OPENCODE_CONFIG"])
	}
}

func TestEnsureSessionOpenCodeNativeProviderUsesOpenCodeConfig(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	session := createRunningLLMFacadeSession(t, ctx, service, "opencode-native")

	env, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "opencode", "opencode/big-pickle", "test", "run-1")
	if err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig returned error: %v", err)
	}
	if len(env) != 0 {
		t.Fatalf("env = %#v, want no managed facade env for opencode native provider", env)
	}
	if _, err := os.Stat(filepath.Join(execution.HostSessionHome(session), ".config", "opencode", "opencode.json")); !os.IsNotExist(err) {
		t.Fatalf("opencode config file should not exist for native provider, stat err=%v", err)
	}
}

func TestEnsureSessionOpenCodeOpenAIWritesRequestedModelConfig(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID:             "default",
		Name:           "default",
		ProviderType:   llms.ProviderFamilyOpenAI,
		DefaultWireAPI: llms.APIProtocolChatCompletions,
		BaseURL:        "https://aiapi.example.invalid/v1",
		APIKey:         "provider-key",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		HeadersJSON:    "{}",
		Weight:         1,
		Enabled:        true,
		Scope:          llms.ProviderScopeSystem,
	}, llms.Model{ID: "kimi-k2.6", Name: "kimi-k2.6", DefaultModel: true, Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("UpsertDefaultLLMConfig returned error: %v", err)
	}
	session := createRunningLLMFacadeSession(t, ctx, service, "opencode-openai")

	env, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "opencode", "openai/kimi-k2.6", "test", "run-1")
	if err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig returned error: %v", err)
	}
	if env["OPENCODE_CONFIG"] != "/root/.config/opencode/opencode.json" {
		t.Fatalf("OPENCODE_CONFIG = %q, want guest opencode config path", env["OPENCODE_CONFIG"])
	}
	token, err := service.configDB.GetLLMFacadeToken(ctx, env["AGENT_COMPOSE_SESSION_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if token.ProviderID != "default" || token.Model != "kimi-k2.6" || token.WireAPI != llms.APIProtocolResponses {
		t.Fatalf("facade token = %#v, want default/kimi-k2.6 responses facade", token)
	}
	payload, err := os.ReadFile(filepath.Join(execution.HostSessionHome(session), ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode opencode config: %v\n%s", err, string(payload))
	}
	openai := decoded["provider"].(map[string]any)["openai"].(map[string]any)
	if openai["npm"] != "@ai-sdk/openai" {
		t.Fatalf("opencode openai npm = %#v, want @ai-sdk/openai", openai["npm"])
	}
	models := openai["models"].(map[string]any)
	if _, ok := models["kimi-k2.6"]; !ok {
		t.Fatalf("opencode openai models = %#v, want requested model", models)
	}
}

func TestSplitOpenCodeModelPreservesNestedModelID(t *testing.T) {
	providerID, modelName, err := llms.SplitOpenCodeModel("openrouter/meta-llama/llama-3.1-8b")
	if err != nil {
		t.Fatalf("splitOpenCodeModel returned error: %v", err)
	}
	if providerID != "openrouter" || modelName != "meta-llama/llama-3.1-8b" {
		t.Fatalf("splitOpenCodeModel = %q, %q; want provider and nested model id preserved", providerID, modelName)
	}
}

func TestEnsureSessionOpenCodeAnthropicUsesFacadeEnv(t *testing.T) {
	ctx := context.Background()
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("LLM_API_KEY", "")
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID:             "anthropic",
		Name:           "anthropic",
		ProviderType:   llms.ProviderFamilyAnthropic,
		DefaultWireAPI: llms.APIProtocolMessages,
		BaseURL:        "https://anthropic.example.invalid",
		APIKey:         "anthropic-provider-key",
		AuthHeader:     "x-api-key",
		HeadersJSON:    `{"anthropic-version":"2023-06-01"}`,
		Weight:         1,
		Enabled:        true,
		Scope:          llms.ProviderScopeSystem,
	}, llms.Model{ID: "claude-sonnet-4-5", Name: "claude-sonnet-4-5", DefaultModel: true, Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("UpsertDefaultLLMConfig returned error: %v", err)
	}
	session := createRunningLLMFacadeSession(t, ctx, service, "opencode-anthropic")

	env, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "opencode", "anthropic/claude-sonnet-4-5", "test", "run-1")
	if err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig returned error: %v", err)
	}
	if env["ANTHROPIC_API_KEY"] != env["AGENT_COMPOSE_SESSION_TOKEN"] {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want facade token", env["ANTHROPIC_API_KEY"])
	}
	if env["ANTHROPIC_BASE_URL"] != "http://agent-compose.test/api/runtime/sessions/"+session.Summary.ID+"/llm/anthropic" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", env["ANTHROPIC_BASE_URL"])
	}
	if env["OPENCODE_CONFIG"] != "/root/.config/opencode/opencode.json" {
		t.Fatalf("OPENCODE_CONFIG = %q, want guest opencode config path", env["OPENCODE_CONFIG"])
	}
	payload, err := os.ReadFile(filepath.Join(execution.HostSessionHome(session), ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode opencode config: %v\n%s", err, string(payload))
	}
	anthropic := decoded["provider"].(map[string]any)["anthropic"].(map[string]any)
	if anthropic["npm"] != "@ai-sdk/anthropic" {
		t.Fatalf("opencode anthropic npm = %#v, want @ai-sdk/anthropic", anthropic["npm"])
	}
	options := anthropic["options"].(map[string]any)
	if options["baseURL"] != "http://agent-compose.test/api/runtime/sessions/"+session.Summary.ID+"/llm/anthropic/v1" {
		t.Fatalf("opencode anthropic baseURL = %#v", options["baseURL"])
	}
	if options["apiKey"] != "{env:AGENT_COMPOSE_SESSION_TOKEN}" {
		t.Fatalf("opencode anthropic apiKey = %#v", options["apiKey"])
	}
	models := anthropic["models"].(map[string]any)
	if _, ok := models["claude-sonnet-4-5"]; !ok {
		t.Fatalf("opencode anthropic models = %#v, want requested model", models)
	}
	token, err := service.configDB.GetLLMFacadeToken(ctx, env["AGENT_COMPOSE_SESSION_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if token.ProviderID != "anthropic" || token.Model != "claude-sonnet-4-5" || token.WireAPI != llms.APIProtocolMessages {
		t.Fatalf("facade token = %#v, want anthropic messages facade", token)
	}
}

func TestEnsureSessionOpenCodeModelRequiresProviderPrefix(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	session := createRunningLLMFacadeSession(t, ctx, service, "opencode-invalid-model")

	env, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "opencode", "kimi-k2.6", "test", "run-1")
	if err == nil {
		t.Fatalf("expected opencode provider/model error, got env=%#v", env)
	}
	if !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("error = %v, want provider/model guidance", err)
	}
}

func TestEnsureSessionLLMFacadeConfigBootstrapsFromSessionEnvProvider(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("LLM_MODEL", "")
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	service.config.LLMAPIEndpoint = ""
	service.config.LLMAPIKey = ""
	service.config.LLMModel = ""
	session := createRunningLLMFacadeSession(t, ctx, service, "session-provider-env")
	session.EnvItems = []SessionEnvVar{{Name: "SAFE_ENV", Value: "safe"}}
	session.ProviderEnvItems = []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://session-openai.example.invalid/v1"},
		{Name: "LLM_API_KEY", Value: "session-provider-key", Secret: true},
		{Name: "LLM_MODEL", Value: "session-model"},
	}

	env, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "codex", "", "test", "run-1")
	if err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig returned error: %v", err)
	}
	if env["AGENT_COMPOSE_SESSION_TOKEN"] == "" {
		t.Fatalf("AGENT_COMPOSE_SESSION_TOKEN missing from managed env: %#v", env)
	}
	if env["LLM_API_KEY"] != env["AGENT_COMPOSE_SESSION_TOKEN"] || env["OPENAI_API_KEY"] != env["AGENT_COMPOSE_SESSION_TOKEN"] {
		t.Fatalf("managed env did not replace provider keys with facade token: %#v", env)
	}
	if strings.Contains(strings.Join([]string{env["LLM_API_KEY"], env["OPENAI_API_KEY"]}, " "), "session-provider-key") {
		t.Fatalf("session provider key leaked into managed env: %#v", env)
	}
	providers, err := service.configDB.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("provider count = %d, want 1: %#v", len(providers), providers)
	}
	if providers[0].ID != llms.SessionEnvProviderID(session.Summary.ID, llms.ProviderFamilyOpenAI) || providers[0].Scope != llms.ProviderScopeSessionEnv {
		t.Fatalf("provider identity = %#v, want session env provider", providers[0])
	}
	if providers[0].APIKey != "session-provider-key" {
		t.Fatalf("provider API key = %q, want session provider key", providers[0].APIKey)
	}
	if providers[0].BaseURL != "https://session-openai.example.invalid/v1" {
		t.Fatalf("provider base url = %q, want session endpoint", providers[0].BaseURL)
	}
	if envMap := sessionEnvMap(session.EnvItems); envMap["LLM_API_KEY"] != "" || envMap["OPENAI_API_KEY"] != "" {
		t.Fatalf("provider key present in session env: %#v", session.EnvItems)
	}
}

func TestSessionEnvProviderDoesNotOverrideConfiguredProvider(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID:             "default",
		Name:           "default",
		ProviderType:   llms.ProviderFamilyOpenAI,
		DefaultWireAPI: llms.APIProtocolResponses,
		BaseURL:        "https://configured.example.invalid/v1",
		APIKey:         "configured-key",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		HeadersJSON:    "{}",
		Weight:         1,
		Enabled:        true,
		Scope:          llms.ProviderScopeSystem,
	}, llms.Model{ID: "configured-model", Name: "configured-model", DefaultModel: true, Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("UpsertDefaultLLMConfig returned error: %v", err)
	}
	session := createRunningLLMFacadeSession(t, ctx, service, "configured-provider")
	session.ProviderEnvItems = []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://session.example.invalid/v1"},
		{Name: "LLM_API_KEY", Value: "session-key", Secret: true},
		{Name: "LLM_MODEL", Value: "session-model"},
	}

	if _, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "codex", "configured-model", "test", "run-1"); err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig returned error: %v", err)
	}
	providers, err := service.configDB.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("provider count = %d, want 1: %#v", len(providers), providers)
	}
	if providers[0].APIKey != "configured-key" || providers[0].BaseURL != "https://configured.example.invalid/v1" {
		t.Fatalf("configured provider was overridden: %#v", providers[0])
	}
}

func TestSessionEnvGenericMessagesEndpointBootstrapsOnlyAnthropicProvider(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	service.config.LLMAPIEndpoint = ""
	service.config.LLMAPIKey = ""
	service.config.LLMModel = ""
	session := createRunningLLMFacadeSession(t, ctx, service, "session-anthropic-provider-env")
	session.ProviderEnvItems = []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://session-anthropic.example.invalid/v1/messages"},
		{Name: "LLM_API_KEY", Value: "session-provider-key", Secret: true},
		{Name: "LLM_MODEL", Value: "claude-session"},
	}

	env, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "claude", "", "test", "run-1")
	if err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig returned error: %v", err)
	}
	if env["ANTHROPIC_API_KEY"] != env["AGENT_COMPOSE_SESSION_TOKEN"] {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want facade token", env["ANTHROPIC_API_KEY"])
	}
	if env["ANTHROPIC_MODEL"] != "claude-session" || env["CLAUDE_MODEL"] != "claude-session" {
		t.Fatalf("claude model env = ANTHROPIC_MODEL:%q CLAUDE_MODEL:%q, want claude-session", env["ANTHROPIC_MODEL"], env["CLAUDE_MODEL"])
	}
	providers, err := service.configDB.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("provider count = %d, want only anthropic provider: %#v", len(providers), providers)
	}
	if providers[0].ID != llms.SessionEnvProviderID(session.Summary.ID, llms.ProviderFamilyAnthropic) || providers[0].ProviderType != llms.ProviderFamilyAnthropic || providers[0].Scope != llms.ProviderScopeSessionEnv {
		t.Fatalf("provider = %#v, want session-scoped anthropic provider", providers[0])
	}
	if providers[0].BaseURL != "https://session-anthropic.example.invalid/v1" {
		t.Fatalf("provider base url = %q, want normalized anthropic base url", providers[0].BaseURL)
	}
}

func TestDockerClaudeFacadeUsesSessionRuntimeBaseURL(t *testing.T) {
	ctx := context.Background()
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL", "CLAUDE_MODEL", "LLM_API_KEY", "LLM_API_ENDPOINT", "LLM_MODEL", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = ""
	service.config.HttpListen = "0.0.0.0:7410"
	service.config.LLMAPIEndpoint = ""
	service.config.LLMAPIKey = ""
	service.config.LLMModel = ""
	session, err := service.store.CreateSession(ctx, "docker-session-runtime-base", "", "docker", "guest:latest", "", domain.SessionTypeManual, nil, []SessionEnvVar{
		{Name: "AGENT_COMPOSE_RUNTIME_BASE_URL", Value: "http://172.17.0.1:7410"},
	}, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	session.ProviderEnvItems = []SessionEnvVar{
		{Name: "ANTHROPIC_API_KEY", Value: "session-provider-key", Secret: true},
		{Name: "ANTHROPIC_BASE_URL", Value: "https://anthropic.example.invalid"},
		{Name: "ANTHROPIC_MODEL", Value: "claude-session"},
	}

	env, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "claude", "", "test", "run-1")
	if err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig returned error: %v", err)
	}
	if env["AGENT_COMPOSE_SESSION_TOKEN"] == "" {
		t.Fatalf("AGENT_COMPOSE_SESSION_TOKEN missing from env: %#v", env)
	}
	if env["ANTHROPIC_BASE_URL"] != "http://172.17.0.1:7410/api/runtime/sessions/"+session.Summary.ID+"/llm/anthropic" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", env["ANTHROPIC_BASE_URL"])
	}
}

func TestSessionEnvProvidersAreScopedPerSession(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("LLM_MODEL", "")
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	service.config.LLMAPIEndpoint = ""
	service.config.LLMAPIKey = ""
	service.config.LLMModel = ""

	sessionA := createRunningLLMFacadeSession(t, ctx, service, "session-env-a")
	sessionA.ProviderEnvItems = []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://session-a.example.invalid/v1"},
		{Name: "LLM_API_KEY", Value: "session-key-a", Secret: true},
		{Name: "LLM_MODEL", Value: "session-model-a"},
	}
	sessionB := createRunningLLMFacadeSession(t, ctx, service, "session-env-b")
	sessionB.ProviderEnvItems = []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://session-b.example.invalid/v1"},
		{Name: "LLM_API_KEY", Value: "session-key-b", Secret: true},
		{Name: "LLM_MODEL", Value: "session-model-b"},
	}

	if _, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, sessionA, "codex", "", "test", "run-a"); err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig(session A) returned error: %v", err)
	}
	if _, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, sessionB, "codex", "", "test", "run-b"); err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig(session B) returned error: %v", err)
	}

	providers, err := service.configDB.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	byID := map[string]llms.Provider{}
	for _, provider := range providers {
		byID[provider.ID] = provider
	}
	providerA := byID[llms.SessionEnvProviderID(sessionA.Summary.ID, llms.ProviderFamilyOpenAI)]
	providerB := byID[llms.SessionEnvProviderID(sessionB.Summary.ID, llms.ProviderFamilyOpenAI)]
	if providerA.APIKey != "session-key-a" || providerA.BaseURL != "https://session-a.example.invalid/v1" || providerA.Scope != llms.ProviderScopeSessionEnv {
		t.Fatalf("session A provider = %#v", providerA)
	}
	if providerB.APIKey != "session-key-b" || providerB.BaseURL != "https://session-b.example.invalid/v1" || providerB.Scope != llms.ProviderScopeSessionEnv {
		t.Fatalf("session B provider = %#v", providerB)
	}
	models, err := service.configDB.ListEnabledLLMModels(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMModels returned error: %v", err)
	}
	modelsByID := map[string]bool{}
	for _, model := range models {
		modelsByID[model.ID] = true
	}
	if !modelsByID["session-model-a"] || !modelsByID["session-model-b"] {
		t.Fatalf("models = %#v, want both session models", models)
	}
}

func TestSessionEnvProviderSelectionUsesAgentFamilyPreference(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_API_ENDPOINT", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("CLAUDE_MODEL", "")
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	service.config.LLMAPIEndpoint = ""
	service.config.LLMAPIKey = ""
	service.config.LLMModel = ""

	session := createRunningLLMFacadeSession(t, ctx, service, "session-env-agent-family")
	session.ProviderEnvItems = []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://session-openai.example.invalid/v1"},
		{Name: "LLM_API_KEY", Value: "session-openai-key", Secret: true},
		{Name: "LLM_MODEL", Value: "shared-session-model"},
		{Name: "ANTHROPIC_BASE_URL", Value: "https://session-anthropic.example.invalid/v1"},
		{Name: "ANTHROPIC_API_KEY", Value: "session-anthropic-key", Secret: true},
		{Name: "ANTHROPIC_MODEL", Value: "shared-session-model"},
	}

	claudeEnv, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "claude", "", "test", "run-claude")
	if err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig(claude) returned error: %v", err)
	}
	claudeToken, err := service.configDB.GetLLMFacadeToken(ctx, claudeEnv["AGENT_COMPOSE_SESSION_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken(claude) returned error: %v", err)
	}
	if claudeToken.ProviderID != llms.SessionEnvProviderID(session.Summary.ID, llms.ProviderFamilyAnthropic) {
		t.Fatalf("claude provider id = %q, want anthropic session provider", claudeToken.ProviderID)
	}

	codexEnv, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "codex", "", "test", "run-codex")
	if err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig(codex) returned error: %v", err)
	}
	codexToken, err := service.configDB.GetLLMFacadeToken(ctx, codexEnv["AGENT_COMPOSE_SESSION_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken(codex) returned error: %v", err)
	}
	if codexToken.ProviderID != llms.SessionEnvProviderID(session.Summary.ID, llms.ProviderFamilyOpenAI) {
		t.Fatalf("codex provider id = %q, want openai session provider", codexToken.ProviderID)
	}
}

func TestCodexFacadeCanUseAnthropicOnlySessionEnvProvider(t *testing.T) {
	ctx := context.Background()
	for _, k := range []string{"LLM_API_ENDPOINT", "LLM_API_KEY", "OPENAI_API_KEY", "LLM_MODEL", "ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL", "CLAUDE_MODEL"} {
		t.Setenv(k, "")
	}
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	service.config.LLMAPIEndpoint = ""
	service.config.LLMAPIKey = ""
	service.config.LLMModel = ""

	session := createRunningLLMFacadeSession(t, ctx, service, "session-env-anthropic-only-codex")
	session.ProviderEnvItems = []SessionEnvVar{
		{Name: "ANTHROPIC_BASE_URL", Value: "https://session-anthropic.example.invalid"},
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "session-anthropic-token", Secret: true},
		{Name: "ANTHROPIC_MODEL", Value: "kimi-k2.6"},
	}

	env, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "codex", "", "test", "run-codex")
	if err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig(codex) returned error: %v", err)
	}
	token, err := service.configDB.GetLLMFacadeToken(ctx, env["AGENT_COMPOSE_SESSION_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	wantProviderID := llms.SessionEnvProviderID(session.Summary.ID, llms.ProviderFamilyAnthropic)
	if token.ProviderID != wantProviderID || token.Model != "kimi-k2.6" || token.WireAPI != llms.APIProtocolResponses {
		t.Fatalf("facade token = %#v, want provider %q, model kimi-k2.6, responses facade", token, wantProviderID)
	}
}

func TestDaemonEnvProviderSelectionUsesAgentFamilyPreference(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("ANTHROPIC_BASE_URL", "https://anthropic.example.invalid")
	t.Setenv("ANTHROPIC_API_ENDPOINT", "")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_MODEL", "shared-model")
	t.Setenv("CLAUDE_MODEL", "")
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	service.config.LLMAPIEndpoint = "https://openai.example.invalid"
	service.config.LLMAPIKey = "openai-key"
	service.config.LLMAPIProtocol = llms.APIProtocolChatCompletions
	service.config.LLMModel = "shared-model"

	session := createRunningLLMFacadeSession(t, ctx, service, "daemon-env-agent-family")
	env, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "codex", "", "test", "run-codex")
	if err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig(codex) returned error: %v", err)
	}
	if env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses {
		t.Fatalf("LLM_API_PROTOCOL = %q, want responses facade wire API", env["LLM_API_PROTOCOL"])
	}
	token, err := service.configDB.GetLLMFacadeToken(ctx, env["AGENT_COMPOSE_SESSION_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if token.ProviderID != llms.ProviderIDDefaultOpenAI {
		t.Fatalf("codex provider id = %q, want default OpenAI provider", token.ProviderID)
	}
	if token.WireAPI != llms.APIProtocolResponses {
		t.Fatalf("codex token wire api = %q, want responses facade wire API", token.WireAPI)
	}
}

func TestSessionEnvProviderIsNotSelectedWithoutProviderID(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("LLM_MODEL", "")
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	service.config.LLMAPIEndpoint = ""
	service.config.LLMAPIKey = ""
	service.config.LLMModel = ""

	session := createRunningLLMFacadeSession(t, ctx, service, "session-env-isolated")
	session.ProviderEnvItems = []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://session-only.example.invalid/v1"},
		{Name: "LLM_API_KEY", Value: "session-only-key", Secret: true},
		{Name: "LLM_MODEL", Value: "session-only-model"},
	}
	if _, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "codex", "", "test", "run-1"); err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig returned error: %v", err)
	}

	target, err := resolveLLMTarget(ctx, service.config, service.configDB, "session-only-model")
	if err != nil {
		t.Fatalf("resolveLLMTarget returned error: %v", err)
	}
	if target.Provider.ID == llms.SessionEnvProviderID(session.Summary.ID, llms.ProviderFamilyOpenAI) || target.Provider.APIKey == "session-only-key" {
		t.Fatalf("daemon target selected session env provider: %#v", target.Provider)
	}
	if target.Provider.ID != llms.ProviderIDDefaultOpenAI || target.Provider.Scope != llms.ProviderScopeEnvDefault {
		t.Fatalf("daemon target = %#v, want default env provider", target.Provider)
	}
}

func TestSessionEnvProviderResolutionWithProviderIDDoesNotBootstrapDefault(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("LLM_MODEL", "")
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	service.config.LLMAPIEndpoint = ""
	service.config.LLMAPIKey = ""
	service.config.LLMModel = ""

	session := createRunningLLMFacadeSession(t, ctx, service, "session-env-provider-id")
	session.ProviderEnvItems = []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://session-provider-id.example.invalid/v1"},
		{Name: "LLM_API_KEY", Value: "session-provider-id-key", Secret: true},
		{Name: "LLM_MODEL", Value: "session-provider-id-model"},
	}
	if _, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "codex", "", "test", "run-1"); err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig returned error: %v", err)
	}

	providerID := llms.SessionEnvProviderID(session.Summary.ID, llms.ProviderFamilyOpenAI)
	target, err := resolveRuntimeLLMTarget(ctx, service.config, service.configDB, "session-provider-id-model", providerID)
	if err != nil {
		t.Fatalf("resolveRuntimeLLMTarget returned error: %v", err)
	}
	if target.Provider.ID != providerID {
		t.Fatalf("target provider = %#v, want session env provider", target.Provider)
	}
	providers, err := service.configDB.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	if len(providers) != 1 || providers[0].ID != providerID {
		t.Fatalf("providers = %#v, want only session env provider", providers)
	}
}

func TestSessionEnvModelOnlyUsesDefaultEnvProvider(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("LLM_MODEL", "")
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	service.config.LLMAPIEndpoint = "https://default-env.example.invalid/v1"
	service.config.LLMAPIKey = "default-env-key"
	service.config.LLMModel = ""

	session := createRunningLLMFacadeSession(t, ctx, service, "session-env-model-only")
	session.ProviderEnvItems = []SessionEnvVar{{Name: "LLM_MODEL", Value: "session-model-only"}}
	if _, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "codex", "", "test", "run-1"); err != nil {
		t.Fatalf("ensureSessionLLMFacadeConfig returned error: %v", err)
	}

	providers, err := service.configDB.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("provider count = %d, want 1: %#v", len(providers), providers)
	}
	if providers[0].ID != llms.ProviderIDDefaultOpenAI || providers[0].Scope != llms.ProviderScopeEnvDefault || providers[0].APIKey != "default-env-key" {
		t.Fatalf("provider = %#v, want default env provider", providers[0])
	}
	models, err := service.configDB.ListEnabledLLMModels(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMModels returned error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "session-model-only" {
		t.Fatalf("models = %#v, want session model on default env provider", models)
	}
}

func TestConfiguredProviderTakesPriorityOverDefaultEnvProvider(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newTestServiceAPIHarness(t)
	if _, err := service.configDB.ReplaceGlobalEnv(ctx, []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://env-default.example.invalid/v1"},
		{Name: "LLM_API_KEY", Value: "env-default-key", Secret: true},
		{Name: "LLM_MODEL", Value: "shared-model"},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}
	if _, err := resolveLLMTarget(ctx, service.config, service.configDB, "shared-model"); err != nil {
		t.Fatalf("resolveLLMTarget(default env) returned error: %v", err)
	}
	if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID:             "configured-openai",
		Name:           "configured-openai",
		ProviderType:   llms.ProviderFamilyOpenAI,
		DefaultWireAPI: llms.APIProtocolResponses,
		BaseURL:        "https://configured-priority.example.invalid/v1",
		APIKey:         "configured-key",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		HeadersJSON:    "{}",
		Weight:         10,
		Enabled:        true,
		Scope:          llms.ProviderScopeSystem,
	}, llms.Model{ID: "shared-model", Name: "shared-model", DefaultModel: true, Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("UpsertDefaultLLMConfig returned error: %v", err)
	}

	target, err := resolveLLMTarget(ctx, service.config, service.configDB, "shared-model")
	if err != nil {
		t.Fatalf("resolveLLMTarget(configured) returned error: %v", err)
	}
	if target.Provider.ID != "configured-openai" || target.Provider.APIKey != "configured-key" {
		t.Fatalf("target provider = %#v, want configured provider", target.Provider)
	}
}

func TestGuestRuntimeLLMBaseURLDockerRequiresReachableBaseForLoopback(t *testing.T) {
	config := &appconfig.Config{HttpListen: "127.0.0.1:7410"}
	session := &Session{Summary: SessionSummary{Driver: "docker"}}
	if got := guestRuntimeLLMBaseURL(config, session); got != "" {
		t.Fatalf("docker loopback base url = %q, want empty without explicit runtime base url", got)
	}

	config.RuntimeBaseURL = "http://agent-compose:7410"
	if got := guestRuntimeLLMBaseURL(config, session); got != "http://agent-compose:7410" {
		t.Fatalf("explicit docker runtime base url = %q, want compose service URL", got)
	}

	config.RuntimeBaseURL = ""
	config.HttpListen = "192.0.2.10:7410"
	if got := guestRuntimeLLMBaseURL(config, session); got != "http://192.0.2.10:7410" {
		t.Fatalf("docker concrete listen base url = %q, want concrete host URL", got)
	}
}

func TestForbiddenRuntimeLLMHeaderDoesNotOvermatchAuthSubstring(t *testing.T) {
	if llms.ForbiddenRuntimeHeader("X-Authored-By") {
		t.Fatalf("X-Authored-By should not be treated as a sensitive auth header")
	}
	if !llms.ForbiddenRuntimeHeader("X-Runtime-Auth") {
		t.Fatalf("X-Runtime-Auth should be treated as sensitive")
	}
	if !llms.ForbiddenRuntimeHeader("X-Session-Token") {
		t.Fatalf("X-Session-Token should be treated as sensitive")
	}
}

func TestCreateSessionFiltersLLMProviderKeysFromPersistedEnv(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newTestServiceAPIHarness(t)
	if _, err := service.configDB.ReplaceGlobalEnv(ctx, []SessionEnvVar{
		{Name: "OPENAI_API_KEY", Value: "global-provider-key", Secret: false},
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "global-provider-key", Secret: false},
		{Name: "GOOGLE_API_KEY", Value: "global-provider-key", Secret: false},
		{Name: "SAFE_ENV", Value: "safe", Secret: false},
		{Name: "SAFE_SECRET_ENV", Value: "safe-secret", Secret: true},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}
	resp, err := service.sessions.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{
		Title: "env-filter",
		EnvItems: []*agentcomposev1.SessionEnvVar{
			{Name: "LLM_API_KEY", Value: "request-provider-key", Secret: false},
			{Name: "GEMINI_API_KEY", Value: "request-provider-key", Secret: false},
			{Name: "REQUEST_ENV", Value: "request", Secret: false},
			{Name: "REQUEST_SECRET_ENV", Value: "request-secret", Secret: true},
		},
	}))
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session, err := service.store.GetSession(ctx, resp.Msg.GetSession().GetSummary().GetSessionId())
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	env := sessionEnvMap(session.EnvItems)
	for _, name := range []string{"OPENAI_API_KEY", "LLM_API_KEY", "ANTHROPIC_AUTH_TOKEN", "GOOGLE_API_KEY", "GEMINI_API_KEY"} {
		if _, ok := env[name]; ok {
			t.Fatalf("%s persisted in session env: %#v", name, session.EnvItems)
		}
	}
	if env["SAFE_ENV"] != "safe" || env["REQUEST_ENV"] != "request" {
		t.Fatalf("safe env missing after filter: %#v", session.EnvItems)
	}
	if env["SAFE_SECRET_ENV"] != "safe-secret" || env["REQUEST_SECRET_ENV"] != "request-secret" {
		t.Fatalf("non-LLM secret env missing after filter: %#v", session.EnvItems)
	}
	for _, item := range session.EnvItems {
		if strings.Contains(item.Value, "provider-key") {
			t.Fatalf("provider key value persisted: %#v", session.EnvItems)
		}
	}
}

func TestRuntimeEnvMapKeepsNonLLMSecretEnv(t *testing.T) {
	env := llms.RuntimeEnvMap([]SessionEnvVar{
		{Name: "DATABASE_PASSWORD", Value: "db-secret", Secret: true},
		{Name: "OPENAI_API_KEY", Value: "provider-key", Secret: true},
	})
	if env["DATABASE_PASSWORD"] != "db-secret" {
		t.Fatalf("DATABASE_PASSWORD = %q, want non-LLM secret env to be preserved", env["DATABASE_PASSWORD"])
	}
	if _, ok := env["OPENAI_API_KEY"]; ok {
		t.Fatalf("OPENAI_API_KEY leaked into runtime env: %#v", env)
	}
}

func TestEnsureSessionAnthropicEnvProviderAuthUsesSessionEnvOnly(t *testing.T) {
	// A session-env Anthropic provider derives its API key only from session env
	// items, so its auth header choice must depend only on those items too. A
	// daemon-level ANTHROPIC_API_KEY must not flip a session token provider to
	// the x-api-key header.
	t.Setenv("ANTHROPIC_API_KEY", "daemon-api-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	store := newTestConfigStore(t)
	ctx := context.Background()
	sessionID := "sess-anthropic-auth"
	envItems := []SessionEnvVar{
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "session-auth-token"},
		{Name: "ANTHROPIC_MODEL", Value: "claude-test"},
	}
	providerID, err := ensureSessionAnthropicEnvProvider(ctx, store, sessionID, "", envItems)
	if err != nil {
		t.Fatalf("ensureSessionAnthropicEnvProvider returned error: %v", err)
	}
	if providerID == "" {
		t.Fatalf("expected a session anthropic provider to be created")
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	var found *llms.Provider
	for i := range providers {
		if providers[i].ID == providerID {
			found = &providers[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("session anthropic provider %q not found", providerID)
	}
	if found.AuthHeader != "Authorization" || found.AuthScheme != "Bearer" {
		t.Fatalf("session provider auth = %q/%q, want Authorization/Bearer (session env has only ANTHROPIC_AUTH_TOKEN)", found.AuthHeader, found.AuthScheme)
	}
}

func TestRevokeLLMFacadeTokensForSessionPrunesDeadRows(t *testing.T) {
	store := newTestConfigStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Active token for the target session: revoked by the call, but kept (within retention).
	activeVal, active, err := llms.NewFacadeToken("sess-x", "m", "default", llms.APIProtocolResponses, "agent", "r-active")
	if err != nil {
		t.Fatalf("llms.NewFacadeToken: %v", err)
	}
	if err := store.SaveLLMFacadeToken(ctx, active); err != nil {
		t.Fatalf("save active: %v", err)
	}
	// Token revoked long ago: should be pruned.
	oldVal, old, err := llms.NewFacadeToken("sess-y", "m", "default", llms.APIProtocolResponses, "agent", "r-old")
	if err != nil {
		t.Fatalf("llms.NewFacadeToken: %v", err)
	}
	old.RevokedAt = now.Add(-2 * llmFacadeTokenRetention)
	if err := store.SaveLLMFacadeToken(ctx, old); err != nil {
		t.Fatalf("save old: %v", err)
	}
	// Expired token: should be pruned.
	expVal, exp, err := llms.NewFacadeToken("sess-z", "m", "default", llms.APIProtocolResponses, "agent", "r-exp")
	if err != nil {
		t.Fatalf("llms.NewFacadeToken: %v", err)
	}
	exp.ExpiresAt = now.Add(-time.Minute)
	if err := store.SaveLLMFacadeToken(ctx, exp); err != nil {
		t.Fatalf("save expired: %v", err)
	}

	if err := store.RevokeLLMFacadeTokensForSession(ctx, "sess-x"); err != nil {
		t.Fatalf("RevokeLLMFacadeTokensForSession: %v", err)
	}

	got, err := store.GetLLMFacadeToken(ctx, activeVal)
	if err != nil {
		t.Fatalf("active token should remain: %v", err)
	}
	if got.RevokedAt.IsZero() {
		t.Fatalf("active token should be marked revoked")
	}
	if _, err := store.GetLLMFacadeToken(ctx, oldVal); err == nil {
		t.Fatalf("long-revoked token should be pruned")
	}
	if _, err := store.GetLLMFacadeToken(ctx, expVal); err == nil {
		t.Fatalf("expired token should be pruned")
	}
}

func TestDeleteLLMFacadeToken(t *testing.T) {
	store := newTestConfigStore(t)
	ctx := context.Background()
	val, token, err := llms.NewFacadeToken("sess", "m", "default", llms.APIProtocolResponses, "agent", "run-1")
	if err != nil {
		t.Fatalf("llms.NewFacadeToken: %v", err)
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := store.GetLLMFacadeToken(ctx, val); err != nil {
		t.Fatalf("token should exist before delete: %v", err)
	}
	if err := store.DeleteLLMFacadeToken(ctx, val); err != nil {
		t.Fatalf("DeleteLLMFacadeToken: %v", err)
	}
	if _, err := store.GetLLMFacadeToken(ctx, val); err == nil {
		t.Fatalf("token should be gone after delete")
	}
	if err := store.DeleteLLMFacadeToken(ctx, ""); err != nil {
		t.Fatalf("deleting empty token should be a no-op: %v", err)
	}
}

func TestResolveRuntimeLLMTargetByExistingProviderID(t *testing.T) {
	// No LLM env present; resolution must rely purely on the existing provider.
	for _, k := range []string{"LLM_API_KEY", "OPENAI_API_KEY", "LLM_API_ENDPOINT", "LLM_MODEL", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL", "CLAUDE_MODEL"} {
		t.Setenv(k, "")
	}
	store := newTestConfigStore(t)
	ctx := context.Background()
	if err := store.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID: "p1", Name: "p1", ProviderType: llms.ProviderFamilyOpenAI, DefaultWireAPI: llms.APIProtocolResponses,
		BaseURL: "https://api.example.com", APIKey: "k", AuthHeader: "Authorization", AuthScheme: "Bearer",
		Weight: 10, Enabled: true, Scope: llms.ProviderScopeSystem,
	}, llms.Model{ID: "m1", Name: "m1", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("UpsertDefaultLLMConfig: %v", err)
	}

	target, err := resolveRuntimeLLMTarget(ctx, &appconfig.Config{}, store, "m1", "p1")
	if err != nil {
		t.Fatalf("resolveRuntimeLLMTarget: %v", err)
	}
	if target.Provider.ID != "p1" || target.Model.ID != "m1" {
		t.Fatalf("resolved %q/%q, want p1/m1", target.Provider.ID, target.Model.ID)
	}
	// The existing provider must satisfy resolution without fabricating bootstrap providers.
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("expected exactly 1 provider, got %d: %+v", len(providers), providers)
	}
}

// TestE2ERuntimeLLMFacadeRoundTrips drives the full daemon-side facade across
// every inbound protocol, the cross-family bridge, SSE streaming, and the main
// rejection paths. Named with the E2E convention so the coverage gate's e2e
// shape exercises the facade HTTP handlers and provider/token resolution.
func TestE2ERuntimeLLMFacadeRoundTrips(t *testing.T) {
	testRuntimeLLMFacadeRoundTrips(t)
}

func testRuntimeLLMFacadeRoundTrips(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	service, _, _ := newTestServiceAPIHarness(t)

	app := echo.New()
	registerRuntimeLLMFacadeRoutes(app, service)

	upsertProvider := func(t *testing.T, id, family, wireAPI, baseURL, authHeader, authScheme, model string) {
		t.Helper()
		if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
			ID: id, Name: id, ProviderType: family, DefaultWireAPI: wireAPI,
			BaseURL: baseURL, APIKey: "provider-key", AuthHeader: authHeader, AuthScheme: authScheme,
			HeadersJSON: `{"anthropic-version":"2023-06-01"}`, Weight: 10, Enabled: true, Scope: llms.ProviderScopeSystem,
		}, llms.Model{ID: model, Name: model, Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
			t.Fatalf("UpsertDefaultLLMConfig(%s): %v", id, err)
		}
	}
	mintToken := func(t *testing.T, session *Session, model, providerID, wireAPI string) string {
		t.Helper()
		val, token, err := llms.NewFacadeToken(session.Summary.ID, model, providerID, wireAPI, "test", "run-e2e")
		if err != nil {
			t.Fatalf("llms.NewFacadeToken: %v", err)
		}
		token.ExpiresAt = time.Now().Add(time.Hour)
		if err := service.configDB.SaveLLMFacadeToken(ctx, token); err != nil {
			t.Fatalf("SaveLLMFacadeToken: %v", err)
		}
		return val
	}
	post := func(sessionID, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+sessionID+path, bytes.NewBufferString(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}

	t.Run("openai_responses", func(t *testing.T) {
		const requestBody = `{"model":"alias-resp","input":[{"role":"developer","content":[{"text":"follow project rules"}]},{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"metadata":{"keep":"exact"}}`
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer provider-key" {
				t.Errorf("upstream auth = %q", r.Header.Get("Authorization"))
			}
			gotBody, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read upstream body: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(gotBody, &got); err != nil {
				t.Errorf("decode upstream body: %v", err)
			}
			if got["model"] != "m-resp" {
				t.Errorf("upstream model = %v, want resolved provider model", got["model"])
			}
			if metadata, _ := got["metadata"].(map[string]any); metadata["keep"] != "exact" {
				t.Errorf("upstream metadata = %#v, want preserved request fields", got["metadata"])
			}
			input, _ := got["input"].([]any)
			var sawDeveloper bool
			for _, item := range input {
				message, _ := item.(map[string]any)
				if message["role"] == "developer" {
					sawDeveloper = true
				}
				content, _ := message["content"].([]any)
				for _, part := range content {
					part, _ := part.(map[string]any)
					if part["text"] != nil && part["type"] != "input_text" {
						t.Errorf("upstream text content part type = %v, want input_text; body=%s", part["type"], gotBody)
					}
				}
			}
			if !sawDeveloper {
				t.Errorf("upstream body did not preserve developer role: %s", gotBody)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp-e2e","model":"m-resp","status":"completed","output_text":"ok"}`))
		}))
		t.Cleanup(upstream.Close)
		service.llm.client = upstream.Client()
		upsertProvider(t, "e2e-openai-resp", llms.ProviderFamilyOpenAI, llms.APIProtocolResponses, upstream.URL, "Authorization", "Bearer", "alias-resp")
		if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
			ID: "e2e-openai-resp", Name: "e2e-openai-resp", ProviderType: llms.ProviderFamilyOpenAI, DefaultWireAPI: llms.APIProtocolResponses,
			BaseURL: upstream.URL, APIKey: "provider-key", AuthHeader: "Authorization", AuthScheme: "Bearer",
			Weight: 10, Enabled: true, Scope: llms.ProviderScopeSystem,
		}, llms.Model{ID: "alias-resp", Name: "m-resp", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
			t.Fatalf("UpsertDefaultLLMConfig(alias-resp): %v", err)
		}
		session := createRunningLLMFacadeSession(t, ctx, service, "e2e-openai-resp")
		token := mintToken(t, session, "alias-resp", "e2e-openai-resp", llms.APIProtocolResponses)
		rec := post(session.Summary.ID, "/llm/openai/v1/responses", token, requestBody)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("openai_chat_completions", func(t *testing.T) {
		const requestBody = `{"model":"alias-chat","messages":[{"role":"user","content":"hi"}],"metadata":{"keep":"exact"},"stream":false}`
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer provider-key" {
				t.Errorf("upstream auth = %q", r.Header.Get("Authorization"))
			}
			gotBody, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read upstream body: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(gotBody, &got); err != nil {
				t.Errorf("decode upstream body: %v", err)
			}
			if got["model"] != "m-chat" {
				t.Errorf("upstream model = %v, want resolved provider model", got["model"])
			}
			if metadata, _ := got["metadata"].(map[string]any); metadata["keep"] != "exact" {
				t.Errorf("upstream metadata = %#v, want preserved request fields", got["metadata"])
			}
			if _, ok := got["input"]; ok {
				t.Errorf("upstream body was bridged to responses shape: %s", gotBody)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chat-e2e","object":"chat.completion","model":"m-chat","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
		}))
		t.Cleanup(upstream.Close)
		service.llm.client = upstream.Client()
		if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
			ID: "e2e-openai-chat", Name: "e2e-openai-chat", ProviderType: llms.ProviderFamilyOpenAI, DefaultWireAPI: llms.APIProtocolChatCompletions,
			BaseURL: upstream.URL, APIKey: "provider-key", AuthHeader: "Authorization", AuthScheme: "Bearer",
			Weight: 10, Enabled: true, Scope: llms.ProviderScopeSystem,
		}, llms.Model{ID: "alias-chat", Name: "m-chat", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
			t.Fatalf("UpsertDefaultLLMConfig(alias-chat): %v", err)
		}
		session := createRunningLLMFacadeSession(t, ctx, service, "e2e-openai-chat")
		token := mintToken(t, session, "alias-chat", "e2e-openai-chat", llms.APIProtocolChatCompletions)
		rec := post(session.Summary.ID, "/llm/openai/v1/chat/completions", token, requestBody)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("openai_responses_qwen_generic_text_parts", func(t *testing.T) {
		const requestBody = `{"model":"alias-qwen","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"stream":false}`
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBody, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read upstream body: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(gotBody, &got); err != nil {
				t.Errorf("decode upstream body: %v", err)
			}
			input, _ := got["input"].([]any)
			for _, item := range input {
				message, _ := item.(map[string]any)
				content, _ := message["content"].([]any)
				for _, part := range content {
					part, _ := part.(map[string]any)
					if part["text"] != nil && part["type"] != "text" {
						t.Errorf("upstream qwen text content part type = %v, want text; body=%s", part["type"], gotBody)
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chat-qwen","object":"chat.completion","model":"qwen-compatible","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		}))
		t.Cleanup(upstream.Close)
		service.llm.client = upstream.Client()
		if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
			ID: "e2e-qwen-compatible", Name: "e2e-qwen-compatible", ProviderType: llms.ProviderFamilyOpenAI, DefaultWireAPI: llms.APIProtocolResponses,
			BaseURL: upstream.URL, APIKey: "provider-key", AuthHeader: "Authorization", AuthScheme: "Bearer",
			UseGenericResponsesTextParts: true, Weight: 10, Enabled: true, Scope: llms.ProviderScopeSystem,
		}, llms.Model{ID: "alias-qwen", Name: "qwen-compatible", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
			t.Fatalf("UpsertDefaultLLMConfig(alias-qwen): %v", err)
		}
		session := createRunningLLMFacadeSession(t, ctx, service, "e2e-qwen-compatible")
		token := mintToken(t, session, "alias-qwen", "e2e-qwen-compatible", llms.APIProtocolResponses)
		rec := post(session.Summary.ID, "/llm/openai/v1/responses", token, requestBody)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode facade response: %v", err)
		}
		if got["output_text"] != "ok" {
			t.Fatalf("facade output_text = %v, want ok; body=%s", got["output_text"], rec.Body.String())
		}
	})

	t.Run("openai_responses_qwen_sparse_responses_sse", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBody, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read upstream body: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(gotBody, &got); err != nil {
				t.Errorf("decode upstream body: %v", err)
			}
			input, _ := got["input"].([]any)
			for _, item := range input {
				message, _ := item.(map[string]any)
				content, _ := message["content"].([]any)
				for _, part := range content {
					part, _ := part.(map[string]any)
					if part["text"] != nil && part["type"] != "text" {
						t.Errorf("upstream qwen stream text content part type = %v, want text; body=%s", part["type"], gotBody)
					}
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(strings.Join([]string{
				`data: {"type":"response.output_text.delta"}`,
				"",
				`data: {"type":"response.output_text.delta","delta":"ok"}`,
				"",
				`data: {"type":"response.completed","response":{"id":"resp-qwen-stream","object":"response","status":"completed","model":"qwen-compatible","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`,
				"",
				"data: [DONE]",
				"",
			}, "\n")))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}))
		t.Cleanup(upstream.Close)
		service.llm.client = upstream.Client()
		if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
			ID: "e2e-qwen-stream", Name: "e2e-qwen-stream", ProviderType: llms.ProviderFamilyOpenAI, DefaultWireAPI: llms.APIProtocolResponses,
			BaseURL: upstream.URL, APIKey: "provider-key", AuthHeader: "Authorization", AuthScheme: "Bearer",
			UseGenericResponsesTextParts: true, Weight: 10, Enabled: true, Scope: llms.ProviderScopeSystem,
		}, llms.Model{ID: "alias-qwen-stream", Name: "qwen-compatible-stream", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
			t.Fatalf("UpsertDefaultLLMConfig(alias-qwen-stream): %v", err)
		}
		session := createRunningLLMFacadeSession(t, ctx, service, "e2e-qwen-stream")
		token := mintToken(t, session, "alias-qwen-stream", "e2e-qwen-stream", llms.APIProtocolResponses)
		rec := post(session.Summary.ID, "/llm/openai/v1/responses", token, `{"model":"alias-qwen-stream","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, want := range []string{"response.output_text.delta", `"delta":"ok"`, "response.output_text.done", "response.output_item.done", "response.completed", `"text":"ok"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("facade qwen SSE body missing %q: %s", want, body)
			}
		}
	})

	t.Run("anthropic_messages", func(t *testing.T) {
		const requestBody = `{"model":"alias-claude","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":"user-1"},"stream":false}`
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-api-key") != "provider-key" {
				t.Errorf("upstream x-api-key = %q", r.Header.Get("x-api-key"))
			}
			gotBody, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read upstream body: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(gotBody, &got); err != nil {
				t.Errorf("decode upstream body: %v", err)
			}
			if got["model"] != "m-claude" {
				t.Errorf("upstream model = %v, want resolved provider model", got["model"])
			}
			if metadata, _ := got["metadata"].(map[string]any); metadata["user_id"] != "user-1" {
				t.Errorf("upstream metadata = %#v, want preserved request fields", got["metadata"])
			}
			if _, ok := got["input"]; ok {
				t.Errorf("upstream body was bridged to openai shape: %s", gotBody)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg-e2e","type":"message","role":"assistant","model":"m-claude","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		}))
		t.Cleanup(upstream.Close)
		service.llm.client = upstream.Client()
		if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
			ID: "e2e-anthropic", Name: "e2e-anthropic", ProviderType: llms.ProviderFamilyAnthropic, DefaultWireAPI: llms.APIProtocolMessages,
			BaseURL: upstream.URL, APIKey: "provider-key", AuthHeader: "x-api-key", AuthScheme: "",
			HeadersJSON: `{"anthropic-version":"2023-06-01"}`, Weight: 10, Enabled: true, Scope: llms.ProviderScopeSystem,
		}, llms.Model{ID: "alias-claude", Name: "m-claude", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
			t.Fatalf("UpsertDefaultLLMConfig(alias-claude): %v", err)
		}
		session := createRunningLLMFacadeSession(t, ctx, service, "e2e-anthropic")
		token := mintToken(t, session, "alias-claude", "e2e-anthropic", llms.APIProtocolMessages)
		rec := post(session.Summary.ID, "/llm/anthropic/v1/messages", token, requestBody)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("cross_family_openai_to_anthropic", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg-bridge","type":"message","role":"assistant","model":"m-bridge","content":[{"type":"text","text":"bridged"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		}))
		t.Cleanup(upstream.Close)
		service.llm.client = upstream.Client()
		upsertProvider(t, "e2e-bridge", llms.ProviderFamilyAnthropic, llms.APIProtocolMessages, upstream.URL, "x-api-key", "", "m-bridge")
		session := createRunningLLMFacadeSession(t, ctx, service, "e2e-bridge")
		token := mintToken(t, session, "m-bridge", "e2e-bridge", llms.APIProtocolResponses)
		rec := post(session.Summary.ID, "/llm/openai/v1/responses", token, `{"model":"m-bridge","input":"hi"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("openai_responses_to_chat_completions", func(t *testing.T) {
		const requestBody = `{"model":"alias-resp-chat","input":[{"role":"developer","content":[{"type":"input_text","text":"follow project rules"}]},{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/chat/completions" {
				t.Errorf("upstream path = %q, want /v1/chat/completions", r.URL.Path)
			}
			gotBody, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read upstream body: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(gotBody, &got); err != nil {
				t.Errorf("decode upstream body: %v", err)
			}
			if got["model"] != "m-resp-chat" {
				t.Errorf("upstream model = %v, want resolved provider model", got["model"])
			}
			if _, ok := got["messages"]; !ok {
				t.Errorf("upstream body was not chat completions shape: %s", gotBody)
			}
			if _, ok := got["input"]; ok {
				t.Errorf("upstream body still had responses input: %s", gotBody)
			}
			messages, _ := got["messages"].([]any)
			var sawSystem bool
			for _, item := range messages {
				message, _ := item.(map[string]any)
				if message["role"] == "developer" {
					t.Errorf("upstream body kept developer role: %s", gotBody)
				}
				if message["role"] == "system" {
					sawSystem = true
				}
			}
			if !sawSystem {
				t.Errorf("upstream body did not map developer role to system: %s", gotBody)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chat-bridge","object":"chat.completion","model":"m-resp-chat","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		}))
		t.Cleanup(upstream.Close)
		service.llm.client = upstream.Client()
		if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
			ID: "e2e-openai-resp-chat", Name: "e2e-openai-resp-chat", ProviderType: llms.ProviderFamilyOpenAI, DefaultWireAPI: llms.APIProtocolChatCompletions,
			BaseURL: upstream.URL, APIKey: "provider-key", AuthHeader: "Authorization", AuthScheme: "Bearer",
			Weight: 10, Enabled: true, Scope: llms.ProviderScopeSystem,
		}, llms.Model{ID: "alias-resp-chat", Name: "m-resp-chat", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
			t.Fatalf("UpsertDefaultLLMConfig(alias-resp-chat): %v", err)
		}
		session := createRunningLLMFacadeSession(t, ctx, service, "e2e-openai-resp-chat")
		token := mintToken(t, session, "alias-resp-chat", "e2e-openai-resp-chat", llms.APIProtocolResponses)
		rec := post(session.Summary.ID, "/llm/openai/v1/responses", token, requestBody)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode facade response: %v", err)
		}
		if got["output_text"] != "ok" {
			t.Fatalf("facade output_text = %v, want ok; body=%s", got["output_text"], rec.Body.String())
		}
	})

	t.Run("openai_chat_completions_to_responses", func(t *testing.T) {
		const requestBody = `{"model":"alias-chat-resp","messages":[{"role":"user","content":"hi"}],"stream":false}`
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/responses" {
				t.Errorf("upstream path = %q, want /v1/responses", r.URL.Path)
			}
			gotBody, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read upstream body: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(gotBody, &got); err != nil {
				t.Errorf("decode upstream body: %v", err)
			}
			if got["model"] != "m-chat-resp" {
				t.Errorf("upstream model = %v, want resolved provider model", got["model"])
			}
			if _, ok := got["input"]; !ok {
				t.Errorf("upstream body was not responses shape: %s", gotBody)
			}
			if _, ok := got["messages"]; ok {
				t.Errorf("upstream body still had chat messages: %s", gotBody)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp-bridge","object":"response","model":"m-chat-resp","status":"completed","output":[{"id":"msg-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		}))
		t.Cleanup(upstream.Close)
		service.llm.client = upstream.Client()
		if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
			ID: "e2e-openai-chat-resp", Name: "e2e-openai-chat-resp", ProviderType: llms.ProviderFamilyOpenAI, DefaultWireAPI: llms.APIProtocolResponses,
			BaseURL: upstream.URL, APIKey: "provider-key", AuthHeader: "Authorization", AuthScheme: "Bearer",
			Weight: 10, Enabled: true, Scope: llms.ProviderScopeSystem,
		}, llms.Model{ID: "alias-chat-resp", Name: "m-chat-resp", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
			t.Fatalf("UpsertDefaultLLMConfig(alias-chat-resp): %v", err)
		}
		session := createRunningLLMFacadeSession(t, ctx, service, "e2e-openai-chat-resp")
		token := mintToken(t, session, "alias-chat-resp", "e2e-openai-chat-resp", llms.APIProtocolChatCompletions)
		rec := post(session.Summary.ID, "/llm/openai/v1/chat/completions", token, requestBody)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode facade response: %v", err)
		}
		choices, _ := got["choices"].([]any)
		if len(choices) != 1 {
			t.Fatalf("facade choices = %#v, want one choice; body=%s", got["choices"], rec.Body.String())
		}
		choice, _ := choices[0].(map[string]any)
		message, _ := choice["message"].(map[string]any)
		if message["content"] != "ok" {
			t.Fatalf("facade message content = %v, want ok; body=%s", message["content"], rec.Body.String())
		}
	})

	t.Run("sse_stream", func(t *testing.T) {
		const upstreamEvents = "event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\nevent: response.completed\ndata: {\"status\":\"completed\"}\n\n"
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(upstreamEvents))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}))
		t.Cleanup(upstream.Close)
		service.llm.client = upstream.Client()
		upsertProvider(t, "e2e-sse", llms.ProviderFamilyOpenAI, llms.APIProtocolResponses, upstream.URL, "Authorization", "Bearer", "m-sse")
		session := createRunningLLMFacadeSession(t, ctx, service, "e2e-sse")
		token := mintToken(t, session, "m-sse", "e2e-sse", llms.APIProtocolResponses)
		rec := post(session.Summary.ID, "/llm/openai/v1/responses", token, `{"model":"m-sse","input":"hi","stream":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if rec.Body.String() != upstreamEvents {
			t.Fatalf("facade SSE body = %q, want exact upstream events", rec.Body.String())
		}
	})

	t.Run("sse_openai_responses_to_chat_completions", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/chat/completions" {
				t.Errorf("upstream path = %q, want /v1/chat/completions", r.URL.Path)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(strings.Join([]string{
				`data: {"id":"chat-stream","object":"chat.completion.chunk","model":"m-stream-chat","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
				"",
				`data: {"id":"chat-stream","object":"chat.completion.chunk","model":"m-stream-chat","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
				"",
				`data: {"id":"chat-stream","object":"chat.completion.chunk","model":"m-stream-chat","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				"",
				"data: [DONE]",
				"",
			}, "\n")))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}))
		t.Cleanup(upstream.Close)
		service.llm.client = upstream.Client()
		if err := service.configDB.UpsertDefaultLLMConfig(ctx, llms.Provider{
			ID: "e2e-openai-stream-chat", Name: "e2e-openai-stream-chat", ProviderType: llms.ProviderFamilyOpenAI, DefaultWireAPI: llms.APIProtocolChatCompletions,
			BaseURL: upstream.URL, APIKey: "provider-key", AuthHeader: "Authorization", AuthScheme: "Bearer",
			Weight: 10, Enabled: true, Scope: llms.ProviderScopeSystem,
		}, llms.Model{ID: "alias-stream-chat", Name: "m-stream-chat", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
			t.Fatalf("UpsertDefaultLLMConfig(alias-stream-chat): %v", err)
		}
		session := createRunningLLMFacadeSession(t, ctx, service, "e2e-openai-stream-chat")
		token := mintToken(t, session, "alias-stream-chat", "e2e-openai-stream-chat", llms.APIProtocolResponses)
		rec := post(session.Summary.ID, "/llm/openai/v1/responses", token, `{"model":"alias-stream-chat","input":"hi","stream":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, want := range []string{"response.output_text.delta", `"delta":"ok"`, "response.output_text.done", "response.output_item.done", "response.completed", `"text":"ok"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("facade SSE body missing %q: %s", want, body)
			}
		}
	})

	t.Run("rejections", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"x","model":"m-rej","status":"completed","output_text":"ok"}`))
		}))
		t.Cleanup(upstream.Close)
		service.llm.client = upstream.Client()
		upsertProvider(t, "e2e-rej", llms.ProviderFamilyOpenAI, llms.APIProtocolResponses, upstream.URL, "Authorization", "Bearer", "m-rej")
		session := createRunningLLMFacadeSession(t, ctx, service, "e2e-rej")
		token := mintToken(t, session, "m-rej", "e2e-rej", llms.APIProtocolResponses)
		path := "/llm/openai/v1/responses"

		if rec := post(session.Summary.ID, path, "", `{"model":"m-rej","input":"hi"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("missing token: status=%d", rec.Code)
		}
		if rec := post(session.Summary.ID, path, "ac_llm_bogus", `{"model":"m-rej","input":"hi"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("bad token: status=%d", rec.Code)
		}
		if rec := post("other-session", path, token, `{"model":"m-rej","input":"hi"}`); rec.Code != http.StatusForbidden {
			t.Fatalf("wrong session: status=%d", rec.Code)
		}
		if rec := post(session.Summary.ID, path, token, `{"model":"mismatch","input":"hi"}`); rec.Code != http.StatusForbidden {
			t.Fatalf("model mismatch: status=%d", rec.Code)
		}
	})
}

// A session-only Anthropic key with no model anywhere must fail fast at config
// time: request-time resolution runs without session env, so the key can never
// resolve a provider. The facade must not inject an unbound token that defers
// the failure to every runtime request.
func TestEnsureSessionClaudeFacadeFailsFastWhenSessionKeyHasNoModel(t *testing.T) {
	ctx := context.Background()
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL", "CLAUDE_MODEL", "LLM_API_KEY", "LLM_API_ENDPOINT", "LLM_MODEL", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	service, _, _ := newTestServiceAPIHarness(t)
	service.config.RuntimeBaseURL = "http://agent-compose.test"
	service.config.LLMAPIEndpoint = ""
	service.config.LLMAPIKey = ""
	service.config.LLMModel = ""

	session := createRunningLLMFacadeSession(t, ctx, service, "session-claude-key-no-model")
	session.ProviderEnvItems = []SessionEnvVar{
		{Name: "ANTHROPIC_API_KEY", Value: "session-only-key", Secret: true},
	}

	env, err := ensureSessionLLMFacadeConfig(ctx, service.config, service.configDB, session, "claude", "", "test", "run-1")
	if err == nil {
		t.Fatalf("expected fail-fast error, got env=%#v", env)
	}
	if env != nil {
		t.Fatalf("expected no facade env on failure, got %#v", env)
	}
	if !strings.Contains(err.Error(), "model") {
		t.Fatalf("error = %v, want it to mention the missing model", err)
	}
	providers, perr := service.configDB.ListEnabledLLMProviders(ctx)
	if perr != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", perr)
	}
	if len(providers) != 0 {
		t.Fatalf("no provider should be created, got %#v", providers)
	}
}

// A session whose LLM key came only from per-session env must keep working after
// a stop/resume. The raw key env (ProviderEnvItems) is not persisted, so on
// resume resolution sees only the key-filtered env; it must reuse the
// session-env provider persisted at creation (with its key intact) rather than
// skip it or overwrite its key with the now-empty env.
func TestResolveRuntimeLLMTargetReusesSessionProviderAfterResume(t *testing.T) {
	ctx := context.Background()
	for _, k := range []string{"LLM_API_KEY", "OPENAI_API_KEY", "LLM_API_ENDPOINT", "LLM_MODEL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL", "CLAUDE_MODEL"} {
		t.Setenv(k, "")
	}
	store := newTestConfigStore(t)
	cfg := &appconfig.Config{}
	sessionID := "sess-resume"

	// Creation: full env with the session-scoped key (mirrors ProviderEnvItems).
	creationEnv := []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://session-llm.example.invalid"},
		{Name: "LLM_API_KEY", Value: "session-real-key", Secret: true},
		{Name: "LLM_MODEL", Value: "m-resume"},
	}
	target, err := resolveRuntimeLLMTargetWithEnv(ctx, cfg, store, sessionID, llms.ProviderFamilyOpenAI, "", "", creationEnv)
	if err != nil {
		t.Fatalf("creation resolve: %v", err)
	}
	wantID := llms.SessionEnvProviderID(sessionID, llms.ProviderFamilyOpenAI)
	if target.Provider.ID != wantID || target.Provider.APIKey != "session-real-key" {
		t.Fatalf("creation target = %q/%q, want %q/session-real-key", target.Provider.ID, target.Provider.APIKey, wantID)
	}

	// Resume: key-filtered env (llms.FilterPersistedRuntimeEnv stripped LLM_API_KEY).
	resumeEnv := []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://session-llm.example.invalid"},
		{Name: "LLM_MODEL", Value: "m-resume"},
	}
	resumed, err := resolveRuntimeLLMTargetWithEnv(ctx, cfg, store, sessionID, llms.ProviderFamilyOpenAI, "", "", resumeEnv)
	if err != nil {
		t.Fatalf("resume resolve: %v", err)
	}
	if resumed.Provider.ID != wantID {
		t.Fatalf("resume provider = %q, want persisted session provider %q", resumed.Provider.ID, wantID)
	}
	if resumed.Provider.APIKey != "session-real-key" {
		t.Fatalf("resume provider key = %q, want preserved session-real-key (not clobbered)", resumed.Provider.APIKey)
	}
	if resumed.Headers.Get("Authorization") != "Bearer session-real-key" {
		t.Fatalf("resume auth header = %q, want Bearer session-real-key", resumed.Headers.Get("Authorization"))
	}

	// Rotation: a resume-time env that *does* carry a new key must update it,
	// not get pinned to the stale persisted key.
	rotatedEnv := []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://session-llm.example.invalid"},
		{Name: "LLM_API_KEY", Value: "rotated-key", Secret: true},
		{Name: "LLM_MODEL", Value: "m-resume"},
	}
	rotated, err := resolveRuntimeLLMTargetWithEnv(ctx, cfg, store, sessionID, llms.ProviderFamilyOpenAI, "", "", rotatedEnv)
	if err != nil {
		t.Fatalf("rotation resolve: %v", err)
	}
	if rotated.Provider.APIKey != "rotated-key" {
		t.Fatalf("rotation provider key = %q, want rotated-key", rotated.Provider.APIKey)
	}
}
