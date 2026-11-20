package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/agentinput"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func stagedUpload(t *testing.T, content string) (string, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "staged")
	if err := os.WriteFile(path, []byte(content), 0o444); err != nil {
		t.Fatal(err)
	}
	return path, int64(len(content))
}

func uploadInput(name, path string, size int64) []agentinput.Part {
	return []agentinput.Part{
		agentinput.TextPart("read"),
		agentinput.FilePart(agentinput.File{Name: name, MediaType: "text/csv", Source: agentinput.SourceUpload, UploadID: "up-1", UploadPath: path, UploadSize: size}),
	}
}

func TestWriteAgentInputFilesCopiesUploadIntoWorkspace(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	staged, size := stagedUpload(t, "a,b\n1,2\n")
	files, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-1", Input: uploadInput("big.csv", staged, size),
		// The inline turn budget must not apply to an upload.
		Limits: agentinput.Limits{MaxFileBytes: 1, MaxTurnBytes: 1},
	})
	if err != nil {
		t.Fatalf("WriteAgentInputFiles: %v", err)
	}
	want := "/workspace/.agent-compose/uploads/run-1/turn-0/big.csv"
	if len(files) != 1 || files[0].GuestPath != want || files[0].SizeBytes != size {
		t.Fatalf("files = %+v, want %s", files, want)
	}
	copied := filepath.Join(workspace, ".agent-compose", "uploads", "run-1", "turn-0", "big.csv")
	if err := os.WriteFile(copied, []byte("agent edit"), 0o600); err != nil {
		t.Fatalf("agent cannot write its copy: %v", err)
	}
	if data, err := os.ReadFile(staged); err != nil || string(data) != "a,b\n1,2\n" {
		t.Fatalf("staged object = %q, %v; an agent edit must not reach it", data, err)
	}
}

func TestWriteAgentInputFilesReportsExpiredUpload(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	_, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-1",
		Input: uploadInput("big.csv", filepath.Join(t.TempDir(), "gone"), 4),
	})
	if err == nil || !strings.Contains(err.Error(), "upload expired") {
		t.Fatalf("err = %v, want an explicit expired upload", err)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, ".agent-compose", "uploads", "run-1", "turn-0", "big.csv")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial file left behind: %v", statErr)
	}
}

func TestWriteAgentInputFilesRejectsTruncatedUpload(t *testing.T) {
	session, config, workspace := inputSandbox(t)
	staged, size := stagedUpload(t, "abc")
	_, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-1", Input: uploadInput("big.csv", staged, size+1),
	})
	if err == nil || !strings.Contains(err.Error(), "expected 4") {
		t.Fatalf("err = %v, want a size mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, ".agent-compose", "uploads", "run-1", "turn-0", "big.csv")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial file left behind: %v", statErr)
	}
}

func TestWriteAgentInputFilesRejectsUploadWithoutSharedWorkspace(t *testing.T) {
	session, config, _ := inputSandbox(t)
	staged, size := stagedUpload(t, "abc")
	_, err := WriteAgentInputFiles(context.Background(), AgentInputFileRequest{
		Config: config, Sandbox: session, RunID: "run-1", Input: uploadInput("big.csv", staged, size),
		WriteGuestFile: func(context.Context, string, []byte) error { return nil },
	})
	if !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("err = %v, want unsupported on a guest-writer driver", err)
	}
}
