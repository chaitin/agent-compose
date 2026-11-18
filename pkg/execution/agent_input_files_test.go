package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	input := []agentinput.Part{
		agentinput.TextPart("compare"),
		agentinput.FilePart(agentinput.File{Name: "data.csv", MediaType: "text/csv", Source: agentinput.SourceInline, Data: []byte("a,b\n1,2\n")}),
		agentinput.FilePart(agentinput.File{Name: "q3.pdf", MediaType: "application/pdf", Source: agentinput.SourceWorkspacePath, WorkspacePath: "reports/q3.pdf"}),
	}
	files, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-1", Input: input,
		Limits: agentinput.Limits{MaxFileBytes: 1 << 20},
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

func TestWriteAgentInputFilesPushesInlineFileToGuest(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	var pushed map[string][]byte
	writer := func(_ context.Context, guestPath string, content []byte) error {
		if pushed == nil {
			pushed = make(map[string][]byte)
		}
		pushed[guestPath] = append([]byte(nil), content...)
		return nil
	}
	files, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-guest",
		Input: []agentinput.Part{
			agentinput.TextPart("read"),
			agentinput.FilePart(agentinput.File{Name: "data.csv", Source: agentinput.SourceInline, Data: []byte("a,b\n")}),
		},
		Limits:         agentinput.Limits{MaxFileBytes: 1024},
		WriteGuestFile: writer,
	})
	if err != nil {
		t.Fatalf("WriteAgentInputFiles: %v", err)
	}
	wantGuest := "/workspace/.agent-compose/uploads/run-guest/turn-0/data.csv"
	if len(files) != 1 || files[0].GuestPath != wantGuest {
		t.Fatalf("files = %+v, want %s", files, wantGuest)
	}
	if string(pushed[wantGuest]) != "a,b\n" {
		t.Errorf("pushed content = %q, want the inline bytes", pushed[wantGuest])
	}
	if _, err := os.Stat(filepath.Join(workspace, ".agent-compose", "uploads", "run-guest", "turn-0", "data.csv")); err != nil {
		t.Errorf("daemon-side bookkeeping copy missing: %v", err)
	}
}

func TestWriteAgentInputFilesSkipsWorkspaceChecksWithoutSharedFilesystem(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: workspace}}
	config := &appconfig.Config{GuestStateRoot: "/sandbox/state", GuestWorkspacePath: "/workspace"}
	writer := func(_ context.Context, _ string, _ []byte) error { return nil }
	// The first inline file creates a daemon-local workspace root. That must not
	// make the daemon treat the pod's workspace reference as locally readable.
	files, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-k8s",
		Input: []agentinput.Part{
			agentinput.TextPart("read"),
			agentinput.FilePart(agentinput.File{Name: "new.csv", Source: agentinput.SourceInline, Data: []byte("a,b\n")}),
			agentinput.FilePart(agentinput.File{Name: "pod-only.csv", Source: agentinput.SourceWorkspacePath, WorkspacePath: "pod-only.csv", SHA256: strings.Repeat("0", 64)}),
		},
		Limits:         agentinput.Limits{MaxFileBytes: 1024},
		WriteGuestFile: writer,
	})
	if err != nil {
		t.Fatalf("WriteAgentInputFiles: %v", err)
	}
	if len(files) != 2 || files[1].GuestPath != "/workspace/pod-only.csv" {
		t.Fatalf("files = %+v, want the pod path returned unchecked", files)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("inline upload did not create the daemon-side bookkeeping root: %v", err)
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

func TestWriteAgentInputFilesRejectsSymlinkedUploadParent(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, ".agent-compose")); err != nil {
		t.Fatalf("symlink upload parent: %v", err)
	}
	_, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-symlink",
		Input:  []agentinput.Part{agentinput.TextPart("read"), agentinput.FilePart(agentinput.File{Name: "data.csv", Source: agentinput.SourceInline, Data: []byte("a,b\n")})},
		Limits: agentinput.Limits{MaxFileBytes: 1024},
	})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want a symlinked-directory error", err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatalf("read outside directory: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("upload escaped the workspace and wrote %v", entries)
	}
}

func TestWriteAgentInputFilesVerifiesWorkspaceDigest(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	data := []byte("a,b\n1,2\n")
	if err := os.WriteFile(filepath.Join(workspace, "data.csv"), data, 0o644); err != nil {
		t.Fatalf("write data: %v", err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	cases := []struct {
		name    string
		sha256  string
		wantErr bool
	}{
		{name: "matching digest", sha256: digest},
		{name: "mismatched digest", sha256: strings.Repeat("0", 64), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
				Config: config, Sandbox: session, RunID: "run-digest",
				Input: []agentinput.Part{
					agentinput.TextPart("read"),
					agentinput.FilePart(agentinput.File{Name: "data.csv", Source: agentinput.SourceWorkspacePath, WorkspacePath: "data.csv", SHA256: tc.sha256}),
				},
				Limits: agentinput.Limits{MaxFileBytes: 1024, MaxTurnBytes: 1024},
			})
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "sha256 does not match") {
					t.Fatalf("err = %v, want a sha256 mismatch", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("WriteAgentInputFiles: %v", err)
			}
		})
	}
}

func TestWriteAgentInputFilesEnforcesTurnLimitAcrossWorkspaceFiles(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	for _, name := range []string{"a.csv", "b.csv"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("123456"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	_, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-turn-limit",
		Input: []agentinput.Part{
			agentinput.TextPart("read both"),
			agentinput.FilePart(agentinput.File{Name: "a.csv", Source: agentinput.SourceWorkspacePath, WorkspacePath: "a.csv"}),
			agentinput.FilePart(agentinput.File{Name: "b.csv", Source: agentinput.SourceWorkspacePath, WorkspacePath: "b.csv"}),
		},
		Limits: agentinput.Limits{MaxFileBytes: 10, MaxTurnBytes: 10},
	})
	if err == nil || !strings.Contains(err.Error(), "turn files are 12 bytes, limit is 10") {
		t.Fatalf("err = %v, want a turn-size error", err)
	}
}
