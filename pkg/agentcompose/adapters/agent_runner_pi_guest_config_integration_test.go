package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestIntegrationAgentRunnerPublishesPiCatalogBeforeEachGuestExecution(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot: root, DbAddr: filepath.Join(root, "data.db"), SandboxRoot: filepath.Join(root, "sandboxes"),
		RuntimeDriver: "k8s", GuestHomePath: "/root", GuestWorkspacePath: "/workspace",
		GuestStateRoot: "/data/state", RuntimeBaseURL: "http://agent-compose.test:7410", LLMAPIKey: "fixture-key",
	}
	configDB, store, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sandbox, err := store.CreateSandbox(ctx, "Pi guest", "", "k8s", "guest:test", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sandbox.ProviderEnvItems = []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://openai.example.test/v1"},
		{Name: "LLM_API_KEY", Value: "fixture-upstream-key"},
		{Name: "LLM_API_PROTOCOL", Value: "responses"},
		{Name: "LLM_MODEL", Value: "first"},
	}
	runtime := &filesystemGuestAgentRuntime{root: t.TempDir()}
	runtime.result = domain.ExecResult{Success: true, Stdout: execution.AgentResultPrefix + `{"provider":"pi","threadId":"pi-fixture","finalText":"done","stopReason":"completed"}`}
	runner := NewAgentRunner(AgentRunnerDeps{Config: config, Store: store, ConfigDB: configDB, Runtimes: fakeRuntimeProvider{runtime: runtime}})
	if err := runner.PrepareSandboxAgentEnvironment(ctx, sandbox, execution.AgentConfig{Provider: "pi", Model: "openai/first"}, nil); err != nil {
		t.Fatal(err)
	}
	assertGuestPiCatalog(t, runtime, "first")
	if len(runtime.dirWrites) != 2 || runtime.dirWrites[0] != "/workspace" || runtime.dirWrites[1] != "/root" {
		t.Fatalf("initial private directory seeding = %v", runtime.dirWrites)
	}
	history := runtime.guestPath("/root/.pi/agent/history.jsonl")
	if err := os.WriteFile(history, []byte("private guest history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusRunning
	for _, model := range []string{"second", "third"} {
		_, _, err := runner.ExecuteAgentRun(ctx, AgentRunRequest{Session: sandbox, Agent: "pi", Model: "openai/" + model, RunID: model, Message: "probe"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertGuestPiCatalog(t, runtime, model)
		if data, err := os.ReadFile(history); err != nil || string(data) != "private guest history\n" {
			t.Fatalf("private history changed: %q, %v", data, err)
		}
		if len(runtime.dirWrites) != 2 {
			t.Fatalf("reused guest home was reseeded: %v", runtime.dirWrites)
		}
	}
	pushErr := errors.New("guest catalog push failed")
	runtime.fileErr = pushErr
	executions := len(runtime.specs)
	_, _, err = runner.ExecuteAgentRun(ctx, AgentRunRequest{Session: sandbox, Agent: "pi", Model: "openai/fourth", RunID: "failed", Message: "probe"}, nil)
	if !errors.Is(err, pushErr) || len(runtime.specs) != executions {
		t.Fatalf("failed Pi push executed stale config: %v", err)
	}
	assertGuestPiCatalog(t, runtime, "third")
	runtime.fileErr = nil
	if _, _, err := runner.ExecuteAgentRun(ctx, AgentRunRequest{Session: sandbox, Agent: "pi", Model: "openai/fourth", RunID: "retry", Message: "probe"}, nil); err != nil {
		t.Fatal(err)
	}
	assertGuestPiCatalog(t, runtime, "fourth")
}

func assertGuestPiCatalog(t *testing.T, runtime *filesystemGuestAgentRuntime, model string) {
	t.Helper()
	data, err := os.ReadFile(runtime.guestPath("/root/.pi/agent/models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Providers map[string]struct {
			APIKey string `json:"apiKey"`
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	provider := catalog.Providers["agent-compose"]
	if len(provider.Models) != 1 || provider.Models[0].ID != model || provider.APIKey != "$AGENT_COMPOSE_SANDBOX_TOKEN" {
		t.Fatalf("guest Pi catalog = %s, expected model %s and environment token reference", data, model)
	}
	if strings.Contains(string(data), "fixture-upstream-key") {
		t.Fatal("upstream credential leaked into Pi catalog")
	}
}

// Pin every provider's generated home file surface. Moving initial home seeding
// earlier is safe only if every later write reaches a guest through a file push.
// A new file in any provider must update this inventory deliberately.
func TestIntegrationAgentRunnerFreshGuestHomeIncludesEveryGeneratedProviderFile(t *testing.T) {
	for _, test := range []struct{ provider, model, generated string }{
		{"codex", "first", ".codex/config.toml"},
		{"claude", "first", ""},
		{"opencode", "openai/first", ".config/opencode/opencode.json"},
		{"pi", "openai/first", ".pi/agent/models.json"},
		{"dsh", "openai/first", ""},
		{"gemini", "first", ""},
	} {
		t.Run(test.provider, func(t *testing.T) {
			root := t.TempDir()
			config := &appconfig.Config{DataRoot: root, DbAddr: filepath.Join(root, "data.db"), SandboxRoot: filepath.Join(root, "sandboxes"), RuntimeDriver: "k8s", GuestHomePath: "/root", GuestWorkspacePath: "/workspace", GuestStateRoot: "/data/state", RuntimeBaseURL: "http://agent-compose.test:7410"}
			configDB, store, err := testutil.OpenStores(t, config)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			sandbox, err := store.CreateSandbox(ctx, "provider inventory", "", "k8s", "guest:test", "", domain.SandboxTypeManual, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			protocol := "responses"
			if test.provider == "claude" {
				protocol = "messages"
			}
			sandbox.ProviderEnvItems = []domain.SandboxEnvVar{{Name: "LLM_API_ENDPOINT", Value: "https://upstream.example.test/v1"}, {Name: "LLM_API_KEY", Value: "fixture-upstream-key"}, {Name: "LLM_API_PROTOCOL", Value: protocol}, {Name: "LLM_MODEL", Value: "first"}}
			hostHome := execution.HostSandboxHome(sandbox)
			if err := os.MkdirAll(hostHome, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(hostHome, ".private-history"), []byte("sandbox private seed"), 0o600); err != nil {
				t.Fatal(err)
			}
			runtime := &filesystemGuestAgentRuntime{root: t.TempDir()}
			runner := NewAgentRunner(AgentRunnerDeps{Config: config, Store: store, ConfigDB: configDB, Runtimes: fakeRuntimeProvider{runtime: runtime}})
			if err := runner.PrepareSandboxAgentEnvironment(ctx, sandbox, execution.AgentConfig{Provider: test.provider, Model: test.model}, nil); err != nil {
				t.Fatal(err)
			}
			files := []string{}
			err = filepath.WalkDir(hostHome, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					return nil
				}
				relative, err := filepath.Rel(hostHome, path)
				if err != nil {
					return err
				}
				// This host process lock is not provider input; the guest owns
				// its own publication locks. Empty skills require no delivery;
				// their host-only empty manifest is covered by skill tests.
				if filepath.ToSlash(relative) == ".agents/.skills.lock" || filepath.ToSlash(relative) == ".agents/skills/.agent-compose-skills.json" {
					return nil
				}
				files = append(files, filepath.ToSlash(relative))
				hostData, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				guestData, err := os.ReadFile(runtime.guestPath(filepath.Join("/root", relative)))
				if err != nil || !bytes.Equal(hostData, guestData) {
					return fmt.Errorf("provider %s file %s absent or stale in guest: %w", test.provider, relative, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			expected := []string{".private-history"}
			if test.generated != "" {
				expected = append(expected, test.generated)
			}
			slices.Sort(expected)
			if !slices.Equal(files, expected) {
				t.Fatalf("provider generated file inventory changed: got=%v want=%v", files, expected)
			}
			if !slices.Equal(runtime.dirWrites, []string{"/workspace", "/root"}) {
				t.Fatalf("initial seeding order = %v", runtime.dirWrites)
			}
			// Startup facade environment is retained after the initial file transfer.
			key := "OPENAI_API_KEY"
			if test.provider == "claude" {
				key = "ANTHROPIC_API_KEY"
			}
			environment := map[string]string{}
			for _, item := range sandbox.RuntimeEnvItems {
				environment[item.Name] = item.Value
			}
			if environment[key] == "" || environment[key] == "fixture-upstream-key" {
				t.Fatalf("startup facade token missing or not scoped for %s", test.provider)
			}
			if test.provider != "gemini" && environment["AGENT_COMPOSE_SANDBOX_TOKEN"] == "" {
				t.Fatalf("selected provider facade token missing for %s", test.provider)
			}
		})
	}
}

func TestIntegrationAgentRunnerGuestDefaultingDoesNotMutateSharedConfiguration(t *testing.T) {
	runner, sandbox, _, _ := newGuestSkillsRunner(t)
	runner.config.GuestHomePath = "/caller-owned-value"
	before := *runner.config
	_ = BuildAgentExecSpec(runner.config, AgentExecSpecRequest{Session: sandbox, Agent: "claude", PromptPath: "/prompt"})
	if !reflect.DeepEqual(before, *runner.config) {
		t.Fatal("command construction changed caller configuration")
	}
	if err := runner.PrepareSandboxAgentEnvironment(context.Background(), sandbox, execution.AgentConfig{Provider: "claude"}, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, *runner.config) {
		t.Fatal("guest preparation changed shared configuration")
	}
}
