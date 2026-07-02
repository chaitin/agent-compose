package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func ContextWithOptionalTimeout(ctx context.Context, timeoutMs uint32) (context.Context, context.CancelFunc) {
	if timeoutMs == 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
}

func NormalizeCellType(cellType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(cellType)) {
	case "", CellTypeJavaScript:
		return CellTypeJavaScript, nil
	case CellTypeShell:
		return CellTypeShell, nil
	case CellTypePython:
		return CellTypePython, nil
	default:
		return "", fmt.Errorf("unsupported cell type %q", cellType)
	}
}

func CellExecSpec(cellType, guestCellDir string) (scriptName, command string, args []string) {
	switch cellType {
	case CellTypeShell:
		return "cell.sh", "bash", []string{filepath.Join(guestCellDir, "cell.sh")}
	case CellTypePython:
		return "cell.py", "python3", []string{"-u", filepath.Join(guestCellDir, "cell.py")}
	default:
		return "cell.js", "node", []string{filepath.Join(guestCellDir, "cell.js")}
	}
}

func WriteCellArtifacts(cellDir, source string, result Result) error {
	files := map[string]string{
		"source.txt":   source,
		"stdout.txt":   result.Stdout,
		"stderr.txt":   result.Stderr,
		"output.txt":   result.Output,
		"exitcode.txt": fmt.Sprintf("%d\n", result.ExitCode),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(cellDir, name), []byte(content), 0o644); err != nil {
			return fmt.Errorf("write cell artifact %s: %w", name, err)
		}
	}
	return nil
}

func WriteJSONArtifact(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json artifact: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write json artifact: %w", err)
	}
	return nil
}

func RecoverResultFromCellArtifacts(cellDir string, fallback Result) Result {
	recovered := fallback
	for _, item := range []struct {
		name string
		set  func(string)
	}{
		{name: "stdout.txt", set: func(value string) { recovered.Stdout = value }},
		{name: "stderr.txt", set: func(value string) { recovered.Stderr = value }},
		{name: "output.txt", set: func(value string) { recovered.Output = value }},
	} {
		data, err := os.ReadFile(filepath.Join(cellDir, item.name))
		if err != nil {
			continue
		}
		item.set(string(data))
	}
	if data, err := os.ReadFile(filepath.Join(cellDir, "exitcode.txt")); err == nil {
		if exitCode, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil {
			recovered.ExitCode = exitCode
			recovered.Success = exitCode == 0
		}
	}
	if strings.TrimSpace(recovered.Output) == "" {
		recovered.Output = recovered.Stdout + recovered.Stderr
	}
	return recovered
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func FirstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func MergeResults(primary, fallback Result) Result {
	merged := primary
	if strings.TrimSpace(merged.Stdout) == "" {
		merged.Stdout = fallback.Stdout
	}
	if strings.TrimSpace(merged.Stderr) == "" {
		merged.Stderr = fallback.Stderr
	}
	if strings.TrimSpace(merged.Output) == "" {
		merged.Output = fallback.Output
	}
	if merged.ExitCode == 0 {
		merged.ExitCode = fallback.ExitCode
	}
	if !merged.Success {
		merged.Success = fallback.Success
	}
	return merged
}

type StreamAccumulator struct {
	stdout strings.Builder
	stderr strings.Builder
	output strings.Builder
}

func (a *StreamAccumulator) WriteChunk(chunk Chunk) {
	if chunk.Text == "" {
		return
	}
	a.output.WriteString(chunk.Text)
	if chunk.IsStderr {
		a.stderr.WriteString(chunk.Text)
		return
	}
	a.stdout.WriteString(chunk.Text)
}

func (a *StreamAccumulator) Result(exitCode int, success bool) Result {
	return Result{
		ExitCode: exitCode,
		Stdout:   a.stdout.String(),
		Stderr:   a.stderr.String(),
		Output:   a.output.String(),
		Success:  success,
	}
}
