package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestLoadStoredAgentThreadIDHandlesMissingInvalidAndValidFiles(t *testing.T) {
	root := t.TempDir()
	if got := LoadStoredAgentThreadID(filepath.Join(root, "missing.json")); got != "" {
		t.Fatalf("missing thread id = %q, want empty", got)
	}

	invalidPath := filepath.Join(root, "invalid.json")
	if err := os.WriteFile(invalidPath, []byte("{broken"), 0o644); err != nil {
		t.Fatalf("write invalid state: %v", err)
	}
	if got := LoadStoredAgentThreadID(invalidPath); got != "" {
		t.Fatalf("invalid thread id = %q, want empty", got)
	}

	blankPath := filepath.Join(root, "blank.json")
	if err := os.WriteFile(blankPath, []byte(`{"threadId":"   "}`), 0o644); err != nil {
		t.Fatalf("write blank state: %v", err)
	}
	if got := LoadStoredAgentThreadID(blankPath); got != "" {
		t.Fatalf("blank thread id = %q, want empty", got)
	}

	validPath := filepath.Join(root, "valid.json")
	if err := os.WriteFile(validPath, []byte(`{"threadId":"  thread-123  "}`), 0o644); err != nil {
		t.Fatalf("write valid state: %v", err)
	}
	if got := LoadStoredAgentThreadID(validPath); got != "thread-123" {
		t.Fatalf("valid thread id = %q, want thread-123", got)
	}

	testLoadStoredAgentThreadIDReadsLegacySessionID(t)
}

func TestIntegrationLoadStoredAgentThreadIDReadsLegacySessionID(t *testing.T) {
	testLoadStoredAgentThreadIDReadsLegacySessionID(t)
}

func TestE2ELoadStoredAgentThreadIDReadsLegacySessionID(t *testing.T) {
	testLoadStoredAgentThreadIDReadsLegacySessionID(t)
}

func testLoadStoredAgentThreadIDReadsLegacySessionID(t *testing.T) {
	t.Helper()
	legacyPath := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(legacyPath, []byte(`{"sessionId":" legacy-thread "}`), 0o644); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}
	if got := LoadStoredAgentThreadID(legacyPath); got != "legacy-thread" {
		t.Fatalf("legacy thread id = %q, want legacy-thread", got)
	}
}

func TestResolveAgentThreadIDFromHostState(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sandbox")
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(sessionDir, "workspace")}}

	statePath := filepath.Join(sessionDir, "state", "agents", "providers", "codex.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(statePath, []byte(`{"threadId":" thread-1 "}`), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	if got := ResolveAgentThreadID(context.Background(), AgentThreadIDRequest{Config: &appconfig.Config{}, Sandbox: session, Agent: "codex"}); got != "thread-1" {
		t.Fatalf("thread id from host state = %q, want thread-1", got)
	}
	if got := ResolveAgentThreadID(context.Background(), AgentThreadIDRequest{Config: &appconfig.Config{}, Sandbox: session, Agent: "codex", ThreadID: " reported "}); got != "reported" {
		t.Fatalf("reported thread id = %q, want reported", got)
	}
	if got := ResolveAgentThreadID(context.Background(), AgentThreadIDRequest{Config: &appconfig.Config{}, Sandbox: session, Agent: "claude"}); got != "" {
		t.Fatalf("thread id without provider state = %q, want empty", got)
	}
	if got := ResolveAgentThreadID(context.Background(), AgentThreadIDRequest{Config: &appconfig.Config{}, Agent: "codex"}); got != "" {
		t.Fatalf("thread id without sandbox = %q, want empty", got)
	}
}

// TestResolveAgentThreadIDWithGuestFileReader covers the no-shared-mount
// path (k8s): the
// thread ID comes from a GuestFileReaderFunc pull instead of a local
// os.ReadFile.
func TestResolveAgentThreadIDWithGuestFileReader(t *testing.T) {
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: "/irrelevant/workspace"}}
	config := &appconfig.Config{}

	var gotPath string
	reader := func(_ context.Context, guestPath string) ([]byte, error) {
		gotPath = guestPath
		return []byte(`{"threadId":"thread-remote"}`), nil
	}

	got := ResolveAgentThreadID(context.Background(), AgentThreadIDRequest{
		Config: config, Sandbox: session, Agent: "codex", ReadGuestFile: reader,
	})
	if got != "thread-remote" {
		t.Fatalf("ThreadID = %q, want %q", got, "thread-remote")
	}
	appconfig.ApplyDefaultGuestPaths(config)
	wantPath := filepath.Join(config.GuestStateRoot, "agents", "providers", "codex.json")
	if gotPath != wantPath {
		t.Fatalf("guest path pulled = %q, want %q", gotPath, wantPath)
	}

	failing := func(context.Context, string) ([]byte, error) { return nil, os.ErrNotExist }
	if got := ResolveAgentThreadID(context.Background(), AgentThreadIDRequest{Config: &appconfig.Config{}, Sandbox: session, Agent: "codex", ReadGuestFile: failing}); got != "" {
		t.Fatalf("thread id after failed guest read = %q, want empty", got)
	}
}

func TestResolveAgentThreadIDSkipsGuestReadWhenThreadIDAlreadyKnown(t *testing.T) {
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: "/irrelevant/workspace"}}
	reader := func(context.Context, string) ([]byte, error) {
		t.Fatal("readGuestFile should not be called when threadID is already known")
		return nil, nil
	}

	got := ResolveAgentThreadID(context.Background(), AgentThreadIDRequest{
		Config: &appconfig.Config{}, Sandbox: session, Agent: "codex", ThreadID: "thread-already-known", ReadGuestFile: reader,
	})
	if got != "thread-already-known" {
		t.Fatalf("thread id = %q, want thread-already-known", got)
	}
}
