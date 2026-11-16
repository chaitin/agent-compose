package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/agentinput"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func inputSandbox(t *testing.T) (*domain.Sandbox, *appconfig.Config, string) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: workspace}}
	config := &appconfig.Config{GuestStateRoot: "/sandbox/state", GuestWorkspacePath: "/workspace"}
	return session, config, workspace
}

func TestWriteAgentInputFilesWritesIntoWorkspace(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	if err := os.MkdirAll(filepath.Join(workspace, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "reports", "q3.pdf"), []byte("%PDF-1.7"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
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
		agentinput.FilePart(agentinput.File{Name: "data.csv", MediaType: "text/csv", Source: agentinput.SourceInline, Data: []byte("a,b\n1,2\n")}),
		agentinput.FilePart(agentinput.File{Name: "q3.pdf", MediaType: "application/pdf", Source: agentinput.SourceWorkspacePath, WorkspacePath: "reports/q3.pdf"}),
	}
	files, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-1", Input: input,
		Limits:         agentinput.Limits{MaxFileBytes: 1 << 20},
		WriteGuestFile: writer,
	})
	if err != nil {
		t.Fatalf("WriteAgentInputFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %+v, want two files", files)
	}
	wantInline := "/workspace/.agent-compose/uploads/run-1/turn-0/data.csv"
	if files[0].GuestPath != wantInline || files[0].SizeBytes != int64(len("a,b\n1,2\n")) {
		t.Errorf("inline file = %+v, want %s", files[0], wantInline)
	}
	hostInline := filepath.Join(workspace, ".agent-compose", "uploads", "run-1", "turn-0", "data.csv")
	if data, err := os.ReadFile(hostInline); err != nil || string(data) != "a,b\n1,2\n" {
		t.Fatalf("inline file in workspace = %q, %v", data, err)
	}
	if string(pushed[wantInline]) != "a,b\n1,2\n" {
		t.Errorf("inline file was not pushed to the guest")
	}
	if files[1].GuestPath != "/workspace/reports/q3.pdf" || files[1].Name != "q3.pdf" {
		t.Errorf("workspace file = %+v", files[1])
	}

	message := AppendFileReferences("compare", files)
	for _, want := range []string{"Attached files", wantInline, "/workspace/reports/q3.pdf"} {
		if !strings.Contains(message, want) {
			t.Errorf("prompt %q does not contain %q", message, want)
		}
	}
}

func TestWriteAgentInputFilesRejectsOversizedWorkspaceFile(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	if err := os.WriteFile(filepath.Join(workspace, "big.bin"), make([]byte, 32), 0o644); err != nil {
		t.Fatalf("write big file: %v", err)
	}
	_, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-2",
		Input:  []agentinput.Part{agentinput.TextPart("read"), agentinput.FilePart(agentinput.File{Name: "big.bin", Source: agentinput.SourceWorkspacePath, WorkspacePath: "big.bin"})},
		Limits: agentinput.Limits{MaxFileBytes: 8},
	})
	if err == nil || !strings.Contains(err.Error(), "limit is 8") {
		t.Fatalf("err = %v, want an oversize error", err)
	}
}

func TestWriteAgentInputFilesWithoutInputWritesNothing(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	files, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-3",
	})
	if err != nil {
		t.Fatalf("WriteAgentInputFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("files = %v, want none", files)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".agent-compose")); !os.IsNotExist(err) {
		t.Fatalf("upload dir exists for a text-only turn: %v", err)
	}
}

func TestWriteAgentInputFilesRejectsMissingWorkspace(t *testing.T) {
	_, config, _ := inputSandbox(t)
	_, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: &domain.Sandbox{}, RunID: "run-4",
		Input: []agentinput.Part{agentinput.TextPart("x")},
	})
	if err == nil {
		t.Fatal("WriteAgentInputFiles() error = nil, want a missing-workspace error")
	}
}

func TestAppendFileReferencesNoFiles(t *testing.T) {
	if got := AppendFileReferences("hello", nil); got != "hello" {
		t.Errorf("message = %q, want unchanged", got)
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

func TestWriteAgentInputFilesRejectsWorkspaceSymlink(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	if err := os.WriteFile(filepath.Join(workspace, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Symlink(filepath.Join(workspace, "secret.txt"), filepath.Join(workspace, "alias.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-5",
		Input:  []agentinput.Part{agentinput.TextPart("read"), agentinput.FilePart(agentinput.File{Name: "alias.txt", Source: agentinput.SourceWorkspacePath, WorkspacePath: "alias.txt"})},
		Limits: agentinput.Limits{MaxFileBytes: 1024},
	})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want a symlink error", err)
	}
}

func TestWriteAgentInputFilesRejectsMissingWorkspaceFile(t *testing.T) {
	session, config, _ := inputSandbox(t)
	_, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-6",
		Input:  []agentinput.Part{agentinput.TextPart("read"), agentinput.FilePart(agentinput.File{Name: "missing.csv", Source: agentinput.SourceWorkspacePath, WorkspacePath: "missing.csv"})},
		Limits: agentinput.Limits{MaxFileBytes: 1024},
	})
	if err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Fatalf("err = %v, want a missing-file error", err)
	}
}

func TestWriteAgentInputFilesNumbersTurnDirectories(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	files, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-7", Turn: 2,
		Input:  []agentinput.Part{agentinput.TextPart("x"), agentinput.FilePart(agentinput.File{Name: "a.csv", Source: agentinput.SourceInline, Data: []byte("a")})},
		Limits: agentinput.Limits{MaxFileBytes: 1024},
	})
	if err != nil {
		t.Fatalf("WriteAgentInputFiles: %v", err)
	}
	if len(files) != 1 || !strings.Contains(files[0].GuestPath, "/turn-2/") {
		t.Fatalf("files = %+v, want a turn-2 path", files)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".agent-compose", "uploads", "run-7", "turn-2", "a.csv")); err != nil {
		t.Fatalf("turn-2 file missing: %v", err)
	}
}
