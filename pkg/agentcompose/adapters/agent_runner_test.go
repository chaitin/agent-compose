package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/internal/testutil"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/sessionstore"
)

type fakeAgentDefinitionStore struct {
	agent domain.AgentDefinition
	err   error
}

func (s fakeAgentDefinitionStore) GetAgentDefinition(context.Context, string) (domain.AgentDefinition, error) {
	if s.err != nil {
		return domain.AgentDefinition{}, s.err
	}
	return s.agent, nil
}

type fakeAgentRuntime struct {
	specs        []domain.ExecSpec
	streamChunks []domain.ExecChunk
	result       domain.ExecResult
	err          error
}

func (r *fakeAgentRuntime) EnsureSandbox(context.Context, *domain.Sandbox, domain.VMState, domain.ProxyState) (domain.SandboxVMInfo, error) {
	return domain.SandboxVMInfo{}, nil
}

func (r *fakeAgentRuntime) StopSandbox(context.Context, *domain.Sandbox, domain.VMState) (bool, error) {
	return false, nil
}

func (r *fakeAgentRuntime) RemoveSandbox(context.Context, *domain.Sandbox, domain.VMState) error {
	return nil
}

func (r *fakeAgentRuntime) Exec(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

func (r *fakeAgentRuntime) ExecStream(_ context.Context, _ *domain.Sandbox, _ domain.VMState, spec domain.ExecSpec, stream domain.ExecStreamWriter) (domain.ExecResult, error) {
	r.specs = append(r.specs, spec)
	for _, chunk := range r.streamChunks {
		if stream != nil {
			stream(chunk)
		}
	}
	if r.err != nil {
		return domain.ExecResult{}, r.err
	}
	if r.result.Stdout != "" || r.result.Stderr != "" || r.result.Output != "" || r.result.ExitCode != 0 || r.result.Success {
		return r.result, nil
	}
	payload := execution.AgentResultPrefix + `{"provider":"codex","threadId":"agent-thread-1","finalText":"done","transcript":"trace","stopReason":"completed"}`
	return domain.ExecResult{Stdout: payload, Output: payload, ExitCode: 0, Success: true}, nil
}

func TestAgentRunnerRetainsFacadeTokenOnlyWhenExecTerminationIsUnconfirmed(t *testing.T) {
	tests := []struct {
		name         string
		runtimeErr   error
		wantRetained bool
	}{
		{name: "confirmed cancellation", runtimeErr: context.Canceled},
		{name: "unconfirmed termination", runtimeErr: domain.ErrExecTerminationUnconfirmed, wantRetained: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			config := &appconfig.Config{
				DataRoot:            root,
				DbAddr:              filepath.Join(root, "data.db"),
				SandboxRoot:         filepath.Join(root, "sandboxes"),
				RuntimeDriver:       driverpkg.RuntimeDriverDocker,
				DefaultImage:        "guest:latest",
				GuestWorkspacePath:  "/workspace",
				GuestStateRoot:      "/data/state",
				GuestRuntimeRoot:    "/data/runtime",
				GuestHomePath:       "/root",
				RuntimeBaseURL:      "http://agent-compose.test:7410",
				LLMAPIKey:           "provider-key",
				SandboxStartTimeout: 2 * time.Second,
			}
			configDB, store, err := testutil.OpenStores(t, config)
			if err != nil {
				t.Fatalf("OpenStores returned error: %v", err)
			}
			session, err := store.CreateSandbox(context.Background(), "token lifecycle", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
			if err != nil {
				t.Fatalf("CreateSandbox returned error: %v", err)
			}
			session.Summary.VMStatus = domain.VMStatusRunning
			if err := store.UpdateSandbox(context.Background(), session); err != nil {
				t.Fatalf("UpdateSandbox returned error: %v", err)
			}
			if err := store.SaveVMState(session.Summary.ID, domain.VMState{Driver: driverpkg.RuntimeDriverDocker, BoxID: "container-1"}); err != nil {
				t.Fatalf("SaveVMState returned error: %v", err)
			}
			runtime := &fakeAgentRuntime{err: tt.runtimeErr}
			runner := NewAgentRunner(config, store, configDB, nil, fakeRuntimeProvider{runtime: runtime})
			_, _, err = runner.ExecuteAgentRun(context.Background(), session, "claude", "", "", "run-1", "wait", "", nil)
			if !errors.Is(err, tt.runtimeErr) {
				t.Fatalf("ExecuteAgentRun error = %v, want %v", err, tt.runtimeErr)
			}
			if len(runtime.specs) != 1 {
				t.Fatalf("runtime specs = %#v", runtime.specs)
			}
			token := runtime.specs[0].Env["AGENT_COMPOSE_SANDBOX_TOKEN"]
			if token == "" {
				t.Fatal("runtime spec missing facade token")
			}
			_, tokenErr := configDB.GetLLMFacadeToken(context.Background(), token)
			if tt.wantRetained && tokenErr != nil {
				t.Fatalf("facade token was removed after unconfirmed termination: %v", tokenErr)
			}
			if !tt.wantRetained && !errors.Is(tokenErr, domain.ErrNotFound) {
				t.Fatalf("facade token after confirmed termination error = %v, want not found", tokenErr)
			}
		})
	}
}

func TestAgentRunnerExecuteAgentRunWritesSystemPromptAndParsesResult(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/data/state",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "agent session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	runtime := &fakeAgentRuntime{}
	skillSource := filepath.Join(root, "skill-source", "pdf")
	if err := os.MkdirAll(skillSource, 0o755); err != nil {
		t.Fatalf("create skill source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillSource, "SKILL.md"), []byte("---\nname: pdf\ndescription: PDF skill\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill source: %v", err)
	}
	runner := NewAgentRunner(config, store, nil, fakeAgentDefinitionStore{agent: domain.AgentDefinition{
		ID:           "agent-1",
		SystemPrompt: "Reply only in Chinese",
		Skills:       []domain.AgentSkill{{Name: "pdf", Provider: "file", Path: skillSource}},
	}}, fakeRuntimeProvider{runtime: runtime})

	result, parsed, err := runner.ExecuteAgentRun(ctx, session, "codex", "agent-1", "", "", "hello", "", nil)
	if err != nil {
		t.Fatalf("ExecuteAgentRun returned error: %v", err)
	}
	if !result.Success || !parsed.Success || parsed.FinalText != "done" {
		t.Fatalf("result = %#v parsed = %#v", result, parsed)
	}
	contentBytes, err := os.ReadFile(execution.HostAgentSystemPromptPath(session))
	if err != nil {
		t.Fatalf("ReadFile(system prompt) returned error: %v", err)
	}
	content := string(contentBytes)
	if content != "Reply only in Chinese" {
		t.Fatalf("system prompt = %q", content)
	}
	if len(runtime.specs) != 1 || !strings.Contains(runtime.specs[0].Args[1], "agent-compose-runtime prompt") {
		t.Fatalf("runtime specs = %#v", runtime.specs)
	}
	if !strings.Contains(runtime.specs[0].Args[1], " --skill 'pdf'") {
		t.Fatalf("runtime command missing skill flag: %s", runtime.specs[0].Args[1])
	}
	if _, err := os.Stat(filepath.Join(execution.HostAgentSkillsDir(session), "pdf", "SKILL.md")); err != nil {
		t.Fatalf("projected skill missing: %v", err)
	}
}

func TestAgentRunnerExecuteAgentRunFallsBackToDefinitionModel(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/data/state",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "agent session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	runtime := &fakeAgentRuntime{}
	runner := NewAgentRunner(config, store, nil, fakeAgentDefinitionStore{agent: domain.AgentDefinition{
		ID:       "agent-1",
		Provider: "opencode",
		Model:    "openai/qwen3-coder-plus",
	}}, fakeRuntimeProvider{runtime: runtime})

	if _, _, err := runner.ExecuteAgentRun(ctx, session, "opencode", "agent-1", "", "", "hello", "", nil); err != nil {
		t.Fatalf("ExecuteAgentRun returned error: %v", err)
	}
	if len(runtime.specs) != 1 {
		t.Fatalf("runtime specs = %#v", runtime.specs)
	}
	if !strings.Contains(runtime.specs[0].Args[1], " --model 'openai/qwen3-coder-plus'") {
		t.Fatalf("runtime command missing definition model: %s", runtime.specs[0].Args[1])
	}
}

func TestAgentRunnerExecuteAgentRunContinuesWhenDefinitionLookupFails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/data/state",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "agent session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	runtime := &fakeAgentRuntime{}
	runner := NewAgentRunner(config, store, nil, fakeAgentDefinitionStore{err: errors.New("store unavailable")}, fakeRuntimeProvider{runtime: runtime})

	result, parsed, err := runner.ExecuteAgentRun(ctx, session, "codex", "agent-1", "", "", "hello", "", nil)
	if err != nil {
		t.Fatalf("ExecuteAgentRun returned error: %v", err)
	}
	if !result.Success || !parsed.Success || parsed.FinalText != "done" {
		t.Fatalf("result = %#v parsed = %#v", result, parsed)
	}
	if len(runtime.specs) != 1 {
		t.Fatalf("runtime specs = %#v", runtime.specs)
	}
	contentBytes, err := os.ReadFile(execution.HostAgentSystemPromptPath(session))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("ReadFile(system prompt) returned error: %v", err)
	}
	if string(contentBytes) != "" {
		t.Fatalf("system prompt = %q, want empty", string(contentBytes))
	}
}

func TestAgentSkillEnvReturnsScopedMap(t *testing.T) {
	env := agentSkillEnv([]domain.SandboxEnvVar{{Name: "GIT_TOKEN", Value: "agent-token"}})
	if env["GIT_TOKEN"] != "agent-token" {
		t.Fatalf("agentSkillEnv = %#v", env)
	}
	empty := agentSkillEnv(nil)
	if empty == nil {
		t.Fatalf("agentSkillEnv(nil) returned nil")
	}
}

func TestAgentRunnerResolveAgentSystemPromptBranches(t *testing.T) {
	ctx := context.Background()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{Tags: []domain.SandboxTag{
		{Name: domain.AgentSandboxTagID, Value: "agent-tagged"},
		{Name: domain.AgentSandboxTagSource, Value: domain.AgentSandboxTagSourceVal},
	}}}
	runner := NewAgentRunner(nil, nil, nil, fakeAgentDefinitionStore{agent: domain.AgentDefinition{SystemPrompt: "  tagged prompt  "}}, nil)
	if prompt, err := runner.ResolveAgentSystemPrompt(ctx, session, ""); err != nil || prompt != "tagged prompt" {
		t.Fatalf("tagged prompt = %q err=%v", prompt, err)
	}
	runner.agents = fakeAgentDefinitionStore{err: errors.New("store unavailable")}
	if prompt, err := runner.ResolveAgentSystemPrompt(ctx, session, "agent-tagged"); err != nil || prompt != "" {
		t.Fatalf("store error prompt = %q err=%v", prompt, err)
	}
	if prompt, err := (*AgentRunner)(nil).ResolveAgentSystemPrompt(ctx, session, "agent-tagged"); err != nil || prompt != "" {
		t.Fatalf("nil runner prompt = %q err=%v", prompt, err)
	}
	if prompt, err := NewAgentRunner(nil, nil, nil, nil, nil).ResolveAgentSystemPrompt(ctx, session, "agent-tagged"); err != nil || prompt != "" {
		t.Fatalf("nil store prompt = %q err=%v", prompt, err)
	}
	if prompt, err := runner.ResolveAgentSystemPrompt(ctx, nil, "agent-tagged"); err != nil || prompt != "" {
		t.Fatalf("nil session prompt = %q err=%v", prompt, err)
	}
	untagged := &domain.Sandbox{Summary: domain.SandboxSummary{Tags: []domain.SandboxTag{{Name: domain.AgentSandboxTagID, Value: "agent-tagged"}}}}
	if prompt, err := runner.ResolveAgentSystemPrompt(ctx, untagged, ""); err != nil || prompt != "" {
		t.Fatalf("untagged prompt = %q err=%v", prompt, err)
	}
}

func TestAgentRunnerPrepareManagedMCPConfigForProviders(t *testing.T) {
	t.Run("codex writes state and native config", func(t *testing.T) {
		root := t.TempDir()
		session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
		payload, err := json.Marshal(map[string]any{
			"mcp_servers": map[string]any{
				"filesystem": map[string]any{"type": "local", "command": "npx", "args": []string{"-y", "server"}},
				"docs":       map[string]any{"type": "remote", "transport": "http", "url": "https://docs.example/mcp"},
			},
		})
		if err != nil {
			t.Fatalf("Marshal returned error: %v", err)
		}
		runner := &AgentRunner{agents: fakeAgentDefinitionStore{agent: domain.AgentDefinition{ConfigJSON: string(payload)}}}
		if err := runner.prepareAgentMCPConfig(context.Background(), session, "agent-1", "codex"); err != nil {
			t.Fatalf("prepareAgentMCPConfig returned error: %v", err)
		}
		stateConfig, err := os.ReadFile(execution.HostAgentMCPConfigPath(session))
		if err != nil || !strings.Contains(string(stateConfig), `"filesystem"`) {
			t.Fatalf("state mcp config=%q err=%v", string(stateConfig), err)
		}
		codexConfig, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml"))
		if err != nil || !strings.Contains(string(codexConfig), `[mcp_servers.filesystem]`) {
			t.Fatalf("codex mcp config=%q err=%v", string(codexConfig), err)
		}
	})

	t.Run("opencode writes state and native config", func(t *testing.T) {
		root := t.TempDir()
		session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
		payload, err := json.Marshal(map[string]any{
			"mcp_servers": map[string]any{
				"filesystem": map[string]any{"type": "local", "command": "npx", "args": []string{"-y", "server"}, "env": map[string]any{"TOKEN": map[string]any{"value": "secret"}}},
			},
		})
		if err != nil {
			t.Fatalf("Marshal returned error: %v", err)
		}
		runner := &AgentRunner{agents: fakeAgentDefinitionStore{agent: domain.AgentDefinition{ConfigJSON: string(payload)}}}
		if err := runner.prepareAgentMCPConfig(context.Background(), session, "agent-1", "opencode"); err != nil {
			t.Fatalf("prepareAgentMCPConfig returned error: %v", err)
		}
		openCodeConfig, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".config", "opencode", "opencode.json"))
		if err != nil || !strings.Contains(string(openCodeConfig), `"mcp"`) || !strings.Contains(string(openCodeConfig), `"filesystem"`) {
			t.Fatalf("opencode mcp config=%q err=%v", string(openCodeConfig), err)
		}
	})

	t.Run("claude writes only unified state config", func(t *testing.T) {
		root := t.TempDir()
		session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
		payload, err := json.Marshal(map[string]any{
			"mcp_servers": map[string]any{
				"docs": map[string]any{"type": "remote", "transport": "sse", "url": "https://docs.example/sse"},
			},
		})
		if err != nil {
			t.Fatalf("Marshal returned error: %v", err)
		}
		runner := &AgentRunner{agents: fakeAgentDefinitionStore{agent: domain.AgentDefinition{ConfigJSON: string(payload)}}}
		if err := runner.prepareAgentMCPConfig(context.Background(), session, "agent-1", "claude"); err != nil {
			t.Fatalf("prepareAgentMCPConfig returned error: %v", err)
		}
		if _, err := os.Stat(execution.HostAgentMCPConfigPath(session)); err != nil {
			t.Fatalf("expected state mcp config, err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml")); !os.IsNotExist(err) {
			t.Fatalf("unexpected codex config stat err=%v", err)
		}
	})

	t.Run("missing agent clears stale provider config", func(t *testing.T) {
		root := t.TempDir()
		session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
		if err := os.MkdirAll(filepath.Join(execution.HostSandboxHome(session), ".codex"), 0o755); err != nil {
			t.Fatalf("MkdirAll returned error: %v", err)
		}
		_ = os.WriteFile(execution.HostAgentMCPConfigPath(session), []byte(`{"mcp_servers":{"old":{}}}`), 0o644)
		if err := os.WriteFile(filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml"), []byte("# agent-compose managed mcp start\n[mcp_servers.old]\ncommand = \"old\"\n# agent-compose managed mcp end\n"), 0o644); err != nil {
			t.Fatalf("WriteFile returned error: %v", err)
		}
		runner := &AgentRunner{agents: fakeAgentDefinitionStore{err: errors.New("boom")}}
		if err := runner.prepareAgentMCPConfig(context.Background(), session, "agent-1", "codex"); err != nil {
			t.Fatalf("prepareAgentMCPConfig returned error: %v", err)
		}
		if _, err := os.Stat(execution.HostAgentMCPConfigPath(session)); !os.IsNotExist(err) {
			t.Fatalf("expected state mcp config removed, err=%v", err)
		}
		data, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml"))
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatalf("ReadFile returned error: %v", err)
		}
		if strings.Contains(string(data), "mcp_servers.old") {
			t.Fatalf("stale codex config not cleared: %q", string(data))
		}
	})
}
