package runs

import (
	"fmt"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func projectRunCommandArtifactsDir(run domain.ProjectRunRecord, sandbox *domain.Sandbox) string {
	return filepath.Join(execution.HostSandboxDir(sandbox), "state", "runs", run.RunID)
}

// projectRunCommandTranscriptPath is the single source of truth for where a
// project-run command's streamed output lives, shared by the live execution and
// the re-attach path so a restart appends to the same file.
func projectRunCommandTranscriptPath(run domain.ProjectRunRecord, sandbox *domain.Sandbox) string {
	return filepath.Join(projectRunCommandArtifactsDir(run, sandbox), "transcript.txt")
}

func (c *Controller) publishRunLogChunk(runID string, chunk domain.ExecChunk, offset uint64) {
	if c == nil {
		return
	}
	publishRunLogChunk(c.runLogs, runID, chunk, offset)
}

func publishRunLogChunk(hub *RunLogHub, runID string, chunk domain.ExecChunk, offset uint64) {
	if hub == nil {
		return
	}
	_ = hub.Publish(RunLogEvent{
		RunID:     runID,
		Data:      chunk.Text,
		Offset:    offset,
		CreatedAt: time.Now().UTC(),
	})
}

func appendProjectRunLogChunk(path string, chunk domain.ExecChunk) (uint64, error) {
	path = strings.TrimSpace(path)
	if path == "" || chunk.Text == "" {
		return 0, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("create run log dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open run log %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, fmt.Errorf("seek run log %s: %w", path, err)
	}
	n, err := file.WriteString(chunk.Text)
	if err != nil {
		return 0, fmt.Errorf("append run log %s: %w", path, err)
	}
	return uint64(offset) + uint64(n), nil
}
