package sessionstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

func TestSandboxProviderEnvPersistsWithoutExecutionOverlay(t *testing.T) {
	store := newCoverageStore(t)
	sandbox, err := store.CreateSandbox(context.Background(), "provider env", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	llms.SetSandboxProviderEnvItems(sandbox, []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example/v1"},
		{Name: "LLM_API_KEY", Value: "sandbox-key", Secret: true},
	})
	sandbox.ExecutionProviderEnvItems = []domain.SandboxEnvVar{{Name: "LLM_API_KEY", Value: "execution-key", Secret: true}}
	if err := store.UpdateSandbox(context.Background(), sandbox); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}

	loaded, err := store.GetSandbox(context.Background(), sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	if llms.EnvItemValue(loaded.ProviderEnvItems, "LLM_API_KEY") != "sandbox-key" {
		t.Fatalf("persisted Provider Env = %#v", loaded.ProviderEnvItems)
	}
	if len(loaded.ExecutionProviderEnvItems) != 0 {
		t.Fatalf("execution Provider Env was persisted: %#v", loaded.ExecutionProviderEnvItems)
	}
	info, err := os.Stat(filepath.Join(store.SandboxDir(sandbox.Summary.ID), "metadata.json"))
	if err != nil {
		t.Fatalf("stat metadata: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("metadata mode = %o, want 600", got)
	}
}
