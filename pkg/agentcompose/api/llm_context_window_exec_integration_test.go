package api_test

import (
	"context"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/chaitin/agent-compose/pkg/agentcompose/adapters"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"google.golang.org/protobuf/proto"
)

func TestCodexContextWindowResetIntegration(t *testing.T) {
	for _, clearWindow := range []bool{true, false} {
		name := "switch to unconfigured model"
		if clearWindow {
			name = "clear window through RPC"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			config := &appconfig.Config{
				DataRoot: root, DbAddr: filepath.Join(root, "data.db"), SandboxRoot: filepath.Join(root, "sandboxes"),
				RuntimeBaseURL: "http://agent-compose.test:7410", GuestHomePath: "/root",
			}
			store, sandboxes, err := testutil.OpenStores(t, config)
			if err != nil {
				t.Fatal(err)
			}
			handler := api.NewLLMHandler(adapters.NewLLMClient(nil, store), store)
			ctx := context.Background()
			models := &agentcomposev2.LLMProviderModels{Models: []*agentcomposev2.LLMModelSpec{
				{Id: "configured", ContextWindow: proto.Int32(128_000), MaxOutputTokens: proto.Int32(2048)},
				{Id: "unconfigured"},
			}}
			if _, err := handler.CreateProvider(ctx, connect.NewRequest(&agentcomposev2.CreateProviderRequest{
				Provider: &agentcomposev2.LLMProviderSpec{
					Id: "gateway", BaseUrl: "https://llm.example/v1", Protocol: "responses", ApiKey: proto.String("test-key"), Models: models,
				},
			})); err != nil {
				t.Fatal(err)
			}
			sandbox, err := sandboxes.CreateSandbox(ctx, "context window", "", "docker", "guest:test", "", domain.SandboxTypeManual, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			req := llms.AgentLLMRequest{Config: config, Store: store, Sandbox: sandbox, AgentKind: "codex", Model: "configured"}
			initial, err := llms.PrepareAgentLLM(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			const windowEnv = "AGENT_COMPOSE_CODEX_CONTEXT_WINDOW"
			if initial.Env[windowEnv] != "128000" {
				t.Fatalf("initial window = %q", initial.Env[windowEnv])
			}
			sandbox.RuntimeEnvItems = llms.EnvItemsFromMap(initial.Env, true)
			if clearWindow {
				models.Models[0].ContextWindow = proto.Int32(0)
				if _, err := handler.UpdateProvider(ctx, connect.NewRequest(&agentcomposev2.UpdateProviderRequest{
					Provider: &agentcomposev2.LLMProviderSpec{Id: "gateway", Models: models},
				})); err != nil {
					t.Fatal(err)
				}
			} else {
				req.Model = "unconfigured"
			}
			for _, reloaded := range []bool{false, true} {
				copy := *sandbox
				session := &copy
				if reloaded {
					// RuntimeEnvItems are transient; the container still has its original environment.
					session, err = sandboxes.GetSandbox(ctx, sandbox.Summary.ID)
					if err != nil {
						t.Fatal(err)
					}
					if len(session.RuntimeEnvItems) != 0 {
						t.Fatal("reloaded sandbox retained transient runtime environment")
					}
				}
				req.Sandbox = session
				prepared, err := llms.PrepareAgentLLM(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				if prepared.Target.ContextWindow != 0 {
					t.Fatalf("resolved window = %d", prepared.Target.ContextWindow)
				}
				base := execution.BuildSandboxExecEnv(ctx, config, session, config.GuestHomePath)
				env := llms.MergeManagedExecEnv(base, prepared.Env)
				if value, ok := env[windowEnv]; !ok || value != "0" {
					t.Fatalf("reloaded=%v: exec window = %q, present=%v; must override the inherited window", reloaded, value, ok)
				}
				session.RuntimeEnvItems = domain.MergeEnvItems(session.RuntimeEnvItems, llms.EnvItemsFromMap(prepared.Env, true))
				if got := domain.SandboxEnvMap(session.RuntimeEnvItems)[windowEnv]; got != "0" {
					t.Fatalf("reloaded=%v: startup window = %q", reloaded, got)
				}
			}
		})
	}
}
