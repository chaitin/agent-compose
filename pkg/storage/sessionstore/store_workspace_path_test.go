package sessionstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
)

func TestGetSandboxRebasesPersistedWorkspacePathToActiveRoot(t *testing.T) {
	testGetSandboxRebasesPersistedWorkspacePathToActiveRoot(t)
}

func TestIntegrationGetSandboxRebasesPersistedWorkspacePathToActiveRoot(t *testing.T) {
	testGetSandboxRebasesPersistedWorkspacePathToActiveRoot(t)
}

func TestE2EGetSandboxRebasesPersistedWorkspacePathToActiveRoot(t *testing.T) {
	testGetSandboxRebasesPersistedWorkspacePathToActiveRoot(t)
}

func testGetSandboxRebasesPersistedWorkspacePathToActiveRoot(t *testing.T) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "sessions")
	store, err := NewWithConfig(&appconfig.Config{SandboxRoot: root, RuntimeDriver: driverpkg.RuntimeDriverDocker})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	const sandboxID = "legacy-sandbox"
	sandboxDir := store.SandboxDir(sandboxID)
	if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
		t.Fatalf("create sandbox dir: %v", err)
	}
	data, err := json.Marshal(Sandbox{Summary: SandboxSummary{
		ID:            sandboxID,
		Driver:        driverpkg.RuntimeDriverDocker,
		WorkspacePath: "/old-daemon/data/sessions/legacy-sandbox/workspace",
	}})
	if err != nil {
		t.Fatalf("encode sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sandboxDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatalf("write sandbox metadata: %v", err)
	}

	sandbox, err := store.GetSandbox(context.Background(), sandboxID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	want := filepath.Join(sandboxDir, "workspace")
	if sandbox.Summary.WorkspacePath != want {
		t.Fatalf("workspace path = %q, want %q", sandbox.Summary.WorkspacePath, want)
	}
}
