package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chaitin/agent-compose/pkg/agentinput"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func pngFixture() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
}

func inputSandbox(t *testing.T) (*domain.Sandbox, *appconfig.Config, string) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: workspace}}
	config := &appconfig.Config{GuestStateRoot: "/sandbox/state", GuestWorkspacePath: "/sandbox/workspace"}
	return session, config, workspace
}

func TestWriteAgentInputFilesMaterializesInlineAndWorkspaceMedia(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	runID := "run-1"
	var pushed map[string][]byte
	writer := func(_ context.Context, guestPath string, content []byte) error {
		if pushed == nil {
			pushed = make(map[string][]byte)
		}
		pushed[guestPath] = append([]byte(nil), content...)
		return nil
	}
	input := []agentinput.Part{
		agentinput.TextPart("compare"),
		agentinput.MediaPart(agentinput.Media{Type: agentinput.MediaTypePNG, Filename: "before.png", Source: agentinput.SourceInline, Data: pngFixture()}),
		agentinput.MediaPart(agentinput.Media{Type: agentinput.MediaTypePDF, Source: agentinput.SourceWorkspacePath, WorkspacePath: "reports/q3.pdf"}),
	}
	guestManifest, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: runID, Input: input, WriteGuestFile: writer,
	})
	if err != nil {
		t.Fatalf("WriteAgentInputFiles: %v", err)
	}
	wantGuestManifest := "/sandbox/state/agents/attachments/run-1/turn-0/content.json"
	if guestManifest != wantGuestManifest {
		t.Fatalf("guest manifest = %q, want %q", guestManifest, wantGuestManifest)
	}
	hostDir := filepath.Join(workspace, "..", "state", "agents", "attachments", runID, "turn-0")
	if _, err := os.Stat(filepath.Join(hostDir, "part-1.png")); err != nil {
		t.Fatalf("inline part was not written: %v", err)
	}
	if !bytes.Equal(pushed[wantGuestManifest], mustReadFile(t, filepath.Join(hostDir, "content.json"))) {
		t.Fatalf("pushed manifest differs from the host copy")
	}
	if _, ok := pushed["/sandbox/state/agents/attachments/run-1/turn-0/part-1.png"]; !ok {
		t.Fatalf("inline part was not pushed to the guest: %v", keys(pushed))
	}

	var manifest agentinput.Manifest
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(hostDir, "content.json")), &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Version != agentinput.ManifestVersion || len(manifest.Parts) != 3 {
		t.Fatalf("manifest = %+v", manifest)
	}
	if manifest.Parts[0].Text != "compare" {
		t.Errorf("first part = %+v", manifest.Parts[0])
	}
	inline := manifest.Parts[1].Media
	if inline == nil || inline.Path != "/sandbox/state/agents/attachments/run-1/turn-0/part-1.png" || inline.SizeBytes != int64(len(pngFixture())) {
		t.Errorf("inline media = %+v", inline)
	}
	if inline.SHA256 == "" || len(inline.SHA256) != 64 {
		t.Errorf("inline digest = %q, want a computed sha256", inline.SHA256)
	}
	workspaceMedia := manifest.Parts[2].Media
	if workspaceMedia == nil || workspaceMedia.Path != "/sandbox/workspace/reports/q3.pdf" || workspaceMedia.WorkspacePath != "reports/q3.pdf" {
		t.Errorf("workspace media = %+v", workspaceMedia)
	}
}

func TestWriteAgentInputFilesReferencesNonNativeMediaByPath(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	input := []agentinput.Part{
		agentinput.TextPart("read the file"),
		agentinput.MediaPart(agentinput.Media{Type: "text/csv", Filename: "data.csv", Source: agentinput.SourceInline, Data: []byte("a,b\n1,2\n")}),
	}
	guestManifest, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-4", Input: input,
		NativeMediaTypes: agentinput.ImageMediaTypes(),
	})
	if err != nil {
		t.Fatalf("WriteAgentInputFiles: %v", err)
	}
	if guestManifest == "" {
		t.Fatal("guest manifest is empty")
	}
	hostManifest := filepath.Join(workspace, "..", "state", "agents", "attachments", "run-4", "turn-0", "content.json")
	var manifest agentinput.Manifest
	if err := json.Unmarshal(mustReadFile(t, hostManifest), &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(manifest.Parts) != 2 || manifest.Parts[1].Media == nil {
		t.Fatalf("parts = %+v, want the generic file kept as a reference part", manifest.Parts)
	}
	generic := manifest.Parts[1].Media
	wantPath := "/sandbox/state/agents/attachments/run-4/turn-0/part-1.csv"
	if !generic.Reference || generic.Path != wantPath {
		t.Errorf("generic media = %+v, want a reference to %q", generic, wantPath)
	}
	if _, err := os.Stat(filepath.Join(workspace, "..", "state", "agents", "attachments", "run-4", "turn-0", "part-1.csv")); err != nil {
		t.Fatalf("generic file was not materialized with its extension: %v", err)
	}
	if manifest.Limits.MaxPartBytes != agentinput.DefaultMaxPartBytes || manifest.Limits.MaxTurnBytes != agentinput.DefaultMaxTurnBytes {
		t.Errorf("manifest limits = %+v, want the defaults", manifest.Limits)
	}
}

func TestWriteAgentInputFilesWithoutInputWritesNothing(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	guestManifest, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-2",
	})
	if err != nil {
		t.Fatalf("WriteAgentInputFiles: %v", err)
	}
	if guestManifest != "" {
		t.Fatalf("guest manifest = %q, want empty", guestManifest)
	}
	if _, err := os.Stat(filepath.Join(workspace, "..", "state")); !os.IsNotExist(err) {
		t.Fatalf("state dir exists for a text-only turn: %v", err)
	}
}

func TestWriteAgentInputFilesRejectsMissingWorkspace(t *testing.T) {
	_, config, _ := inputSandbox(t)
	_, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: &domain.Sandbox{}, RunID: "run-3",
		Input: []agentinput.Part{agentinput.TextPart("x")},
	})
	if err == nil {
		t.Fatal("WriteAgentInputFiles() error = nil, want a missing-workspace error")
	}
}

func TestRunStateSegmentKeepsIDsPathSafe(t *testing.T) {
	cases := map[string]string{
		"run-1":        "run-1",
		"":             "run",
		"a/b":          "a_b",
		"run_01.abc":   "run_01.abc",
		"../../escape": ".._.._escape",
	}
	for input, want := range cases {
		if got := runStateSegment(input); got != want {
			t.Errorf("runStateSegment(%q) = %q, want %q", input, got, want)
		}
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func keys(values map[string][]byte) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}
