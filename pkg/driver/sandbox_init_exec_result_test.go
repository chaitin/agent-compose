//go:build k8scompose || microsandboxcgo || boxlitecgo

package driver

import (
	"strings"
	"testing"
)

func TestRequireSandboxInitExecSuccess(t *testing.T) {
	if err := requireSandboxInitExecSuccess(ExecResult{Success: true}, "bootstrap"); err != nil {
		t.Fatalf("successful exec returned error: %v", err)
	}
	err := requireSandboxInitExecSuccess(ExecResult{ExitCode: 7, Stderr: "mkdir failed"}, "bootstrap")
	if err == nil || !strings.Contains(err.Error(), "mkdir failed") || !strings.Contains(err.Error(), "bootstrap") {
		t.Fatalf("error = %v, want operation and stderr", err)
	}
	err = requireSandboxInitExecSuccess(ExecResult{ExitCode: 7}, "signal")
	if err == nil || !strings.Contains(err.Error(), "exit code 7") {
		t.Fatalf("error = %v, want exit code fallback", err)
	}
}
