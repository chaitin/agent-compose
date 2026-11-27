package execution

import (
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestExecStreamAccumulatorStdioStreams(t *testing.T) {
	var accumulator ExecStreamAccumulator
	accumulator.WriteChunk(domain.ExecChunk{Text: "first-stderr", Stream: domain.StdioStderr})
	accumulator.WriteChunk(domain.ExecChunk{Text: "zero"})
	accumulator.WriteChunk(domain.ExecChunk{Text: "-stdout", Stream: domain.StdioStdout})
	accumulator.WriteChunk(domain.ExecChunk{Text: "-stderr", Stream: domain.StdioStderr})
	accumulator.WriteChunk(domain.ExecChunk{Text: "-unknown", Stream: domain.StdioStream("unknown")})
	accumulator.WriteChunk(domain.ExecChunk{Stream: domain.StdioStderr})

	result := accumulator.Result(7, false)
	if result.Stdout != "zero-stdout-unknown" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if result.Stderr != "first-stderr-stderr" {
		t.Fatalf("stderr = %q", result.Stderr)
	}
	if result.Output != "first-stderrzero-stdout-stderr-unknown" {
		t.Fatalf("output = %q", result.Output)
	}
	if result.ExitCode != 7 || result.Success {
		t.Fatalf("result metadata = %#v", result)
	}
}

func TestExecStreamAccumulatorRestoresInterleavedHistory(t *testing.T) {
	var accumulator ExecStreamAccumulator
	accumulator.Restore(domain.ExecResult{Stdout: "first\n", Stderr: "warning\n", Output: "warning\nfirst\n"})
	accumulator.WriteChunk(domain.ExecChunk{Text: "tail", Stream: domain.StdioStdout})
	result := accumulator.Result(0, true)
	if result.Stdout != "first\ntail" || result.Stderr != "warning\n" || result.Output != "warning\nfirst\ntail" {
		t.Fatalf("result=%+v", result)
	}
}
