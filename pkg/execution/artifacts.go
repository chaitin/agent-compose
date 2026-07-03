package execution

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	domain "agent-compose/pkg/model"
)

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

func WriteCellArtifacts(cellDir, source string, result domain.ExecResult) error {
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

func RecoverExecResultFromCellArtifacts(cellDir string, fallback domain.ExecResult) domain.ExecResult {
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

func FirstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
