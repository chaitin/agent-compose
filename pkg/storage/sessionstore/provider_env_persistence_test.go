package sessionstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

func TestSandboxProviderEnvPersistsOnlyProvenance(t *testing.T) {
	store := newCoverageStore(t)
	sandbox, err := store.CreateSandbox(context.Background(), "provider env", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	llms.SetSandboxProviderEnvItems(sandbox, []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example/v1"},
		{Name: "LLM_API_KEY", Value: "sandbox-key", Secret: true},
		{Name: "ORDINARY", Value: "ordinary"},
	})
	if err := store.UpdateSandbox(context.Background(), sandbox); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}

	loaded, err := store.GetSandbox(context.Background(), sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	if len(loaded.ProviderEnvItems) != 0 {
		t.Fatalf("transient ProviderEnvItems were persisted: %#v", loaded.ProviderEnvItems)
	}
	if len(loaded.ProviderEnvOverrideNames) != 2 || loaded.ProviderEnvOverrideNames[0] != "LLM_API_ENDPOINT" || loaded.ProviderEnvOverrideNames[1] != "LLM_API_KEY" {
		t.Fatalf("persisted provider provenance = %#v", loaded.ProviderEnvOverrideNames)
	}

	metadata, err := os.ReadFile(filepath.Join(store.SandboxDir(sandbox.Summary.ID), "metadata.json"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if strings.Contains(string(metadata), "sandbox-key") || strings.Contains(string(metadata), "ordinary") {
		t.Fatalf("metadata contains transient provider values: %s", metadata)
	}
}
