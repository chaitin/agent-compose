package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

const (
	sandboxHostSIGPIPEHelperEnv    = "AGENT_COMPOSE_TEST_SANDBOX_HOST_SIGPIPE_HELPER"
	sandboxHostSIGPIPEHelperMarker = "sandbox-host survived a broken pipe"
)

// A SIGPIPE raised on a thread the Go runtime did not create, which is how the
// native VM drivers trigger it, cannot be produced without cgo in a test. A
// write to a broken stderr goes through the same runtime decision: the process
// dies unless it has registered for SIGPIPE. So the test runs the host startup
// in a child process whose stderr is a pipe with no reader.
func TestSandboxHostIntegrationBrokenPipeDoesNotTerminateHost(t *testing.T) {
	if os.Getenv(sandboxHostSIGPIPEHelperEnv) == "1" {
		runSandboxHostSIGPIPEHelper()
		return
	}

	dataRoot := t.TempDir()
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	// With the read end closed, every write the child makes to stderr hits a
	// broken pipe, starting with the host's own startup log lines.
	if err := stderrRead.Close(); err != nil {
		t.Fatalf("close stderr read end: %v", err)
	}

	var stdout bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestSandboxHostIntegrationBrokenPipeDoesNotTerminateHost$")
	cmd.Env = append(os.Environ(), sandboxHostSIGPIPEHelperEnv+"=1", "DATA_ROOT="+dataRoot)
	// Run outside the package directory so the host does not load a .env from it.
	cmd.Dir = dataRoot
	cmd.Stdout = &stdout
	cmd.Stderr = stderrWrite
	runErr := cmd.Run()
	if err := stderrWrite.Close(); err != nil {
		t.Fatalf("close stderr write end: %v", err)
	}

	if runErr != nil {
		t.Fatalf("sandbox-host child did not exit cleanly: %v; stdout:\n%s", runErr, stdout.String())
	}
	if !strings.Contains(stdout.String(), sandboxHostSIGPIPEHelperMarker) {
		t.Fatalf("sandbox-host child stdout = %q, want marker %q", stdout.String(), sandboxHostSIGPIPEHelperMarker)
	}
}

// runSandboxHostSIGPIPEHelper runs in the child process and always exits it.
// It reports failures on stdout because stderr is the broken pipe under test.
func runSandboxHostSIGPIPEHelper() {
	// Holding the host lock makes runSandboxHost go through its startup and
	// then return instead of serving.
	lockFile, err := acquireSandboxHostLock(os.Getenv("DATA_ROOT"))
	if err != nil {
		fmt.Printf("hold sandbox-host lock: %v\n", err)
		os.Exit(3)
	}
	// The lock is released when the process exits below.
	_ = lockFile

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runSandboxHost(ctx); err == nil {
		fmt.Println("runSandboxHost returned nil while another host held the lock")
		os.Exit(4)
	}

	if _, err := fmt.Fprintln(os.Stderr, "write after startup"); !errors.Is(err, syscall.EPIPE) {
		fmt.Printf("stderr write error = %v, want EPIPE\n", err)
		os.Exit(5)
	}
	fmt.Println(sandboxHostSIGPIPEHelperMarker)
	os.Exit(0)
}
