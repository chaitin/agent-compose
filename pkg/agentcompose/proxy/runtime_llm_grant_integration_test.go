package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/llms/runtimefacade"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
)

func TestRuntimeLLMFacadeUsesExecutionGrantAfterSandboxReload(t *testing.T) {
	for _, key := range []string{"LLM_API_ENDPOINT", "LLM_API_PROTOCOL", "LLM_API_KEY", "OPENAI_API_KEY", "LLM_MODEL"} {
		t.Setenv(key, "")
	}
	var upstreamPath string
	var upstreamAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		upstreamAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(upstream.Close)

	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:       root,
		DbAddr:         filepath.Join(root, "data.db"),
		RuntimeBaseURL: "http://facade.example.test:7410",
		GuestHomePath:  "/root",
	}
	di := do.New()
	do.ProvideValue(di, config)
	store, err := configstore.NewConfigStore(di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	executionSandbox := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            "sandbox-execution-grant",
			Driver:        driverpkg.RuntimeDriverDocker,
			WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-execution-grant", "workspace"),
		},
		ExecutionProviderEnvItems: []domain.SandboxEnvVar{
			{Name: "LLM_API_ENDPOINT", Value: upstream.URL + "/v1"},
			{Name: "LLM_API_PROTOCOL", Value: llms.APIProtocolChatCompletions},
			{Name: "LLM_API_KEY", Value: "execution-key", Secret: true},
		},
	}
	managedEnv, err := runtimefacade.EnsureSessionLLMFacadeConfig(context.Background(), config, store, executionSandbox, "codex", "gpt-test", runtimefacade.TokenSourceAgent, "run-execution")
	if err != nil {
		t.Fatalf("EnsureSessionLLMFacadeConfig returned error: %v", err)
	}
	rawToken := managedEnv["AGENT_COMPOSE_SANDBOX_TOKEN"]
	if rawToken == "" {
		t.Fatal("facade token is empty")
	}

	// The request handler sees the authoritative persisted sandbox, not the
	// execution clone that supplied the Provider Env overlay.
	reloadedSandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: executionSandbox.Summary.ID, VMStatus: domain.VMStatusRunning}}
	e := echo.New()
	RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
		Tokens:    store,
		Sandboxes: runtimeLLMGrantSandboxStore{sandbox: reloadedSandbox},
		ResolveTarget: func(ctx context.Context, model, providerID string) (llms.ResolvedTarget, error) {
			return llms.ResolveFacadeRuntimeTarget(ctx, config, store, model, providerID)
		},
		Client: upstream.Client(),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/"+executionSandbox.Summary.ID+"/llm/openai/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("facade status = %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamPath != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
	if upstreamAuthorization != "Bearer execution-key" {
		t.Fatalf("upstream authorization was not sourced from the execution grant")
	}
}

type runtimeLLMGrantSandboxStore struct {
	sandbox *domain.Sandbox
}

func (s runtimeLLMGrantSandboxStore) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	return s.sandbox, nil
}
