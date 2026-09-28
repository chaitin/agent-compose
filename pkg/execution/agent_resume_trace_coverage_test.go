package execution

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestAgentThreadLogSelectionAndResumeInfo(t *testing.T) {
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

	historyPath := filepath.Join(sessionDir, "home", ".codex", "history.jsonl")
	matchingThreadPath := filepath.Join(sessionDir, "home", ".codex", "sessions", "run-thread-1.jsonl")
	otherThreadPath := filepath.Join(sessionDir, "home", ".codex", "sessions", "run-other.jsonl")
	for _, path := range []string{historyPath, matchingThreadPath, otherThreadPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	txtPath := filepath.Join(sessionDir, "home", ".codex", "sessions", "run-thread-1.txt")
	if err := os.WriteFile(txtPath, []byte("skip"), 0o644); err != nil {
		t.Fatalf("write txt thread: %v", err)
	}

	if ShouldIncludeAgentJSONL(txtPath, "codex", "thread-1") {
		t.Fatal("txt path included as jsonl")
	}
	if !ShouldIncludeAgentJSONL(historyPath, "codex", "thread-1") {
		t.Fatal("codex history jsonl excluded")
	}
	if !ShouldIncludeAgentJSONL(matchingThreadPath, "codex", "thread-1") {
		t.Fatal("matching codex thread jsonl excluded")
	}
	if ShouldIncludeAgentJSONL(otherThreadPath, "codex", "thread-1") {
		t.Fatal("non-matching codex thread jsonl included")
	}
	if !ShouldIncludeAgentJSONL(filepath.Join(root, "claude.jsonl"), "claude", "thread-1") {
		t.Fatal("claude jsonl excluded")
	}

	paths := FindAgentThreadLogPaths(HostSandboxHome(session), "codex", "thread-1")
	wantPaths := []string{historyPath, matchingThreadPath}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("FindAgentThreadLogPaths = %#v, want %#v", paths, wantPaths)
	}
	if got := FindAgentThreadLogPaths(HostSandboxHome(session), "unknown", "thread-1"); got != nil {
		t.Fatalf("unknown provider paths = %#v, want nil", got)
	}

	info := CollectAgentResumeInfo(context.Background(), AgentResumeInfoRequest{Config: &appconfig.Config{}, Sandbox: session, Agent: "codex", ManifestPath: "/guest/agent-thread.json"})
	if info == nil {
		t.Fatal("CollectAgentResumeInfo returned nil")
		return
	}
	if info.Provider != "codex" || info.ThreadID != "thread-1" || info.ThreadStatePath != statePath || info.ThreadManifestPath != "/guest/agent-thread.json" {
		t.Fatalf("resume info = %#v", info)
	}
	if !reflect.DeepEqual(info.ProviderLogPaths, wantPaths) {
		t.Fatalf("provider log paths = %#v, want %#v", info.ProviderLogPaths, wantPaths)
	}

	emptySession := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "empty", "workspace")}}
	if got := CollectAgentResumeInfo(context.Background(), AgentResumeInfoRequest{Config: &appconfig.Config{}, Sandbox: emptySession}); got != nil {
		t.Fatalf("empty resume info = %#v, want nil", got)
	}
}

// TestCollectAgentResumeInfoWithGuestFileReader covers the no-shared-mount
// path (k8s - see docs/design/k8s_pod_runtime_driver_design.md §2.1): the
// thread ID comes from a GuestFileReaderFunc pull instead of a local
// os.ReadFile, and ThreadStatePath/ProviderLogPaths - which need a
// host-local path to mean anything - stay unset rather than pointing at
// something that was never written to local disk.
func TestCollectAgentResumeInfoWithGuestFileReader(t *testing.T) {
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: "/irrelevant/workspace"}}
	config := &appconfig.Config{}

	var gotPath string
	reader := func(_ context.Context, guestPath string) ([]byte, error) {
		gotPath = guestPath
		return []byte(`{"threadId":"thread-remote"}`), nil
	}

	info := CollectAgentResumeInfo(context.Background(), AgentResumeInfoRequest{
		Config: config, Sandbox: session, Agent: "codex", ManifestPath: "/guest/agent-thread.json", ReadGuestFile: reader,
	})
	if info == nil {
		t.Fatal("CollectAgentResumeInfo returned nil")
	}
	if info.ThreadID != "thread-remote" {
		t.Fatalf("ThreadID = %q, want %q", info.ThreadID, "thread-remote")
	}
	if info.ThreadStatePath != "" {
		t.Fatalf("ThreadStatePath = %q, want empty (no local mount to point at)", info.ThreadStatePath)
	}
	if info.ProviderLogPaths != nil {
		t.Fatalf("ProviderLogPaths = %#v, want nil (no directory scan without a mount)", info.ProviderLogPaths)
	}
	appconfig.ApplyDefaultGuestPaths(config)
	wantPath := filepath.Join(config.GuestStateRoot, "agents", "providers", "codex.json")
	if gotPath != wantPath {
		t.Fatalf("guest path pulled = %q, want %q", gotPath, wantPath)
	}
}

func TestCollectAgentResumeInfoSkipsGuestReadWhenThreadIDAlreadyKnown(t *testing.T) {
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: "/irrelevant/workspace"}}
	reader := func(context.Context, string) ([]byte, error) {
		t.Fatal("readGuestFile should not be called when threadID is already known")
		return nil, nil
	}

	info := CollectAgentResumeInfo(context.Background(), AgentResumeInfoRequest{
		Config: &appconfig.Config{}, Sandbox: session, Agent: "codex", ThreadID: "thread-already-known", ManifestPath: "/guest/agent-thread.json", ReadGuestFile: reader,
	})
	if info == nil || info.ThreadID != "thread-already-known" {
		t.Fatalf("resume info = %#v, want ThreadID = thread-already-known", info)
	}
}

func TestAgentSandboxRootsAndTraceDetails(t *testing.T) {
	home := t.TempDir()
	if roots := AgentThreadLogRoots(home, "claude"); len(roots) != 3 || !strings.Contains(roots[0], ".claude") {
		t.Fatalf("claude roots = %#v", roots)
	}
	if roots := AgentThreadLogRoots(home, "opencode"); roots != nil {
		t.Fatalf("opencode roots = %#v, want nil", roots)
	}

	details, consumed := CollectAgentTraceDetails("agent.tool", []string{"one", "  ", "two"})
	if details != "one" || consumed != 2 {
		t.Fatalf("tool details=%q consumed=%d, want one/2", details, consumed)
	}
	details, consumed = CollectAgentTraceDetails("agent.assistant", []string{"one", "", "[tool: run]", "two"})
	if details != "one\n" || consumed != 2 {
		t.Fatalf("assistant details=%q consumed=%d, want one newline/2", details, consumed)
	}
	details, consumed = CollectAgentTraceDetails("agent.assistant", []string{"one", "two"})
	if details != "one\ntwo" || consumed != 2 {
		t.Fatalf("terminal details=%q consumed=%d, want all lines/2", details, consumed)
	}

	events := AgentTraceEvents("[tool: run]\n$ go test\n[hook: done]\nfinished", time.Unix(10, 0).UTC())
	if len(events) != 2 {
		t.Fatalf("events = %#v, want two events", events)
	}
	if events[0].Type != "agent.tool" || events[0].Message != "run\n$ go test" {
		t.Fatalf("tool event = %#v", events[0])
	}
	if events[1].Type != "agent.hook" || events[1].Message != "done\nfinished" {
		t.Fatalf("hook event = %#v", events[1])
	}
	if _, _, ok := ParseAgentTraceMarker("[tool: ]"); ok {
		t.Fatal("empty tool marker parsed")
	}
	if _, _, ok := ParseAgentTraceMarker("[hook: ]"); ok {
		t.Fatal("empty hook marker parsed")
	}
}
