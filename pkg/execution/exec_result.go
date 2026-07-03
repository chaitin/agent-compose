package execution

import (
	"strings"

	domain "agent-compose/pkg/model"
)

type ExecStreamAccumulator struct {
	stdout strings.Builder
	stderr strings.Builder
	output strings.Builder
}

func (a *ExecStreamAccumulator) WriteChunk(chunk domain.ExecChunk) {
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

func (a *ExecStreamAccumulator) Result(exitCode int, success bool) domain.ExecResult {
	return domain.ExecResult{
		ExitCode: exitCode,
		Stdout:   a.stdout.String(),
		Stderr:   a.stderr.String(),
		Output:   a.output.String(),
		Success:  success,
	}
}

func MergeExecResults(primary, fallback domain.ExecResult) domain.ExecResult {
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
