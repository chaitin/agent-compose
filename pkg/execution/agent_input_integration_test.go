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
)

func TestIntegrationAgentInputFileMaterialization(t *testing.T) {
	runAgentInputFileMaterialization(t)
}

func TestE2EAgentInputFileMaterialization(t *testing.T) {
	runAgentInputFileMaterialization(t)
}

// runAgentInputFileMaterialization exercises the daemon-side input path as one
// workflow: normalize prompt plus text/file parts, admit the limits, write
// inline bytes, resolve a workspace reference with a digest, and append the
// paths the harness reads.
func runAgentInputFileMaterialization(t *testing.T) {
	t.Helper()
	session, config, workspace := inputSandbox(t)
	if err := os.MkdirAll(filepath.Join(workspace, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	report := []byte("%PDF-1.7\n")
	if err := os.WriteFile(filepath.Join(workspace, "reports", "q3.pdf"), report, 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	sum := sha256.Sum256(report)
	parts, warnings, err := agentinput.Normalize("Summarize both files.", []agentinput.Part{
		agentinput.TextPart("Then compare them."),
		agentinput.FilePart(agentinput.File{Name: "data.csv", MediaType: "text/csv", Source: agentinput.SourceInline, Data: []byte("a,b\n1,2\n")}),
		agentinput.FilePart(agentinput.File{Name: "q3.pdf", MediaType: "application/pdf", Source: agentinput.SourceWorkspacePath, WorkspacePath: "reports/q3.pdf", SHA256: hex.EncodeToString(sum[:])}),
	})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "prompt was prepended") {
		t.Fatalf("warnings = %v, want the prompt merge warning", warnings)
	}
	limits := agentinput.Limits{MaxFileBytes: 1 << 20, MaxTurnBytes: 1 << 20, MaxFiles: 2}
	if err := agentinput.Validate(parts, limits); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	files, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-integration", Input: parts, Limits: limits,
	})
	if err != nil {
		t.Fatalf("WriteAgentInputFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %+v, want two files", files)
	}
	if files[1].SizeBytes != int64(len(report)) {
		t.Fatalf("workspace file size = %d, want %d", files[1].SizeBytes, len(report))
	}
	message := AppendFileReferences(agentinput.Text(parts), files)
	for _, want := range []string{"Summarize both files.", "Then compare them.", files[0].GuestPath, files[1].GuestPath} {
		if !strings.Contains(message, want) {
			t.Fatalf("prompt %q does not contain %q", message, want)
		}
	}
	if data, err := os.ReadFile(filepath.Join(workspace, ".agent-compose", "uploads", "run-integration", "turn-0", "data.csv")); err != nil || string(data) != "a,b\n1,2\n" {
		t.Fatalf("inline file = %q, err = %v", data, err)
	}
}
